// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/types/known/wrapperspb"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"local-csi-driver/internal/csi/capability"
	"local-csi-driver/internal/csi/core"
	"local-csi-driver/internal/pkg/events"
	"local-csi-driver/internal/pkg/lvm"
	"local-csi-driver/internal/pkg/probe"
)

const (
	// Logical Volume (LV) related event reasons.
	deletingLogicalVolume       = "DeletingLogicalVolume"
	deletedLogicalVolume        = "DeletedLogicalVolume"
	deletingLogicalVolumeFailed = "DeletingLogicalVolumeFailed"

	defaultPeSize        = 4 * 1024 * 1024 // 4 MiB
	defaultDataAlignment = 1024 * 1024     // 1 MiB
)

// GetControllerDriverCapabilities returns the controller service.
func (l *LVM) GetControllerDriverCapabilities() []*csi.ControllerServiceCapability {
	caps := make([]*csi.ControllerServiceCapability, 0, len(controllerCapabilities))
	for _, cap := range controllerCapabilities {
		caps = append(caps, capability.NewControllerServiceCapability(cap))
	}
	return caps
}

// Create implements the csi.ControllerServer interface.
// It provisions and returns a new logical volume with lvm.
func (l *LVM) Create(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.Volume, error) {
	ctx, span := l.tracer.Start(ctx, "volume.lvm.csi/Create", trace.WithAttributes(
		attribute.String("vol.name", req.Name),
	))
	defer span.End()

	log := log.FromContext(ctx)
	log.V(1).Info("creating volume")

	span.SetAttributes(attribute.String("vol.group", DefaultVolumeGroup))

	// Validate capacity.
	capacity := req.GetCapacityRange().GetRequiredBytes()
	limit := req.GetCapacityRange().GetLimitBytes()
	if capacity == 0 && limit > 0 {
		capacity = limit
	}
	if capacity == 0 {
		log.Info("invalid volume size, defaulting to 1G")
		capacity = 1024 * 1024 * 1024 // 1 GiB
	}
	span.SetAttributes(attribute.Int64("capacity.bytes", capacity))

	// Validate request parameters.
	params := req.GetParameters()
	if params == nil {
		params = make(map[string]string)
	}

	id, err := newVolumeId(DefaultVolumeGroup, req.Name)
	if err != nil {
		log.Error(err, "failed to create volume id", "name", req.Name)
		span.SetStatus(codes.Error, "failed to create volume id")
		span.RecordError(err)
		return nil, fmt.Errorf("failed to create volume id: %w", err)
	}

	allocatedSize, err := l.EnsureVolume(ctx, id.String(), capacity, limit, false)
	if err != nil {
		// Check for existing volume on the node.
		log.Error(err, "failed to ensure volume", "name", id.String())
		span.SetStatus(codes.Error, "failed to ensure volume")
		span.RecordError(err)
		return nil, fmt.Errorf("failed to ensure volume: %w", err)
	}

	// Set the size in the VolumeContext so it can be used for PV recovery.
	params[CapacityParam] = fmt.Sprint(allocatedSize)
	params[LimitParam] = fmt.Sprint(limit)

	return &csi.Volume{
		CapacityBytes: allocatedSize,
		VolumeId:      id.String(),
		VolumeContext: params,
		ContentSource: req.GetVolumeContentSource(),
		AccessibleTopology: []*csi.Topology{
			{
				Segments: map[string]string{
					TopologyKey: l.nodeName,
				},
			},
		},
	}, nil
}

// func (i *Internal) Get(ctx context.Context, name string) (*csi.Volume, error) {
// 	vol, err := i.client.IntV1alpha1().Volumes(namespace).Get(ctx, name, metav1.GetOptions{})
// 	if err != nil {
// 		return nil, err
// 	}
// 	return &csi.Volume{
// 		VolumeId:      vol.Name,
// 		CapacityBytes: vol.Spec.Capacity.Storage().Value(),
// 		VolumeContext: vol.Spec.Parameters,
// 	}, nil
// }

// Delete implements the csi.ControllerServer interface.
// It deletes the lvm logical volume from the node.
func (l *LVM) Delete(ctx context.Context, req *csi.DeleteVolumeRequest) error {
	ctx, span := l.tracer.Start(ctx, "volume.lvm.csi/Delete")
	defer span.End()

	recorder := events.FromContext(ctx)

	id, err := newIdFromString(req.GetVolumeId())
	if err != nil {
		// DeleteVolume calls for this Delete to return OK if the volume
		// doesn't exist, so we ignore the error. By definition, the
		// volume doesn't exist if we can't parse the ID.
		return nil
	}

	// The volume name includes both the volume group and logical volume name.
	volumeId := fmt.Sprintf("%s/%s", id.VolumeGroup, id.LogicalVolume)
	span.SetAttributes(
		attribute.String("vol.id", volumeId),
		attribute.String("vol.group", id.VolumeGroup),
		attribute.String("vol.name", id.LogicalVolume),
	)

	// Cleanup the volume on the node.
	deleteOps := lvm.RemoveLVOptions{Name: volumeId}

	ctx, cancel := context.WithTimeout(ctx, removeVolumeRetryTimeout)
	defer cancel()

	ticker := time.NewTicker(removeVolumeRetryPoll)
	defer ticker.Stop()

	recorder.Eventf(corev1.EventTypeNormal, deletingLogicalVolume, "Starting deletion of logical volume %s", volumeId)
	var lastErr error
	for {
		select {
		case <-ctx.Done():
			err := errors.Join(lastErr, ctx.Err())
			recorder.Eventf(corev1.EventTypeWarning, deletingLogicalVolumeFailed, "Failed to delete logical volume %s: %s", volumeId, err.Error())
			span.SetStatus(codes.Error, "failed to delete logical volume")
			span.RecordError(err)
			return fmt.Errorf("failed to remove logical volume %s: %w", volumeId, err)
		case <-ticker.C:
			err := l.lvm.RemoveLogicalVolume(ctx, deleteOps)
			if lvm.IgnoreNotFound(err) == nil {
				recorder.Eventf(corev1.EventTypeNormal, deletedLogicalVolume, "Successfully deleted logical volume %s", volumeId)
				span.AddEvent("deleted logical volume")
				span.SetStatus(codes.Ok, "deleted logical volume")
				return nil
			}
			if !errors.Is(err, lvm.ErrInUse) {
				recorder.Eventf(corev1.EventTypeWarning, deletingLogicalVolumeFailed, "Failed to delete logical volume %s: %s", volumeId, err.Error())
				span.AddEvent("failed to delete logical volume", trace.WithAttributes(
					attribute.String("error", err.Error()),
				))
				return err
			}
			span.AddEvent("failed to delete logical volume, retrying", trace.WithAttributes(
				attribute.String("error", err.Error()),
			))
			lastErr = err
		}
	}
}

func (l *LVM) List(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	_, span := l.tracer.Start(ctx, "volume.lvm.csi/List")
	defer span.End()

	// TODO(sc): list volumes.

	// vols := &lvmv1alpha1.VolumeList{}
	// if err := l.client.List(ctx, vols, &client.ListOptions{}); err != nil {
	// 	span.SetStatus(codes.Error, "List failed")
	// 	span.RecordError(err)
	// 	return nil, err
	// }

	// entries := make([]*csi.ListVolumesResponse_Entry, 0, len(vols.Items))
	// for _, vol := range vols.Items {
	// 	capacity := vol.Spec.Capacity[corev1.ResourceStorage]
	// 	entries = append(entries, &csi.ListVolumesResponse_Entry{
	// 		Volume: &csi.Volume{
	// 			VolumeId:      vol.GetName(),
	// 			CapacityBytes: capacity.Value(),
	// 			VolumeContext: vol.Spec.Parameters,
	// 		},
	// 	})
	// }

	return &csi.ListVolumesResponse{
		// Entries: entries,
	}, nil
}

// GetCapacity implements the csi.ControllerServer interface.
// It returns the available capacity for the lvm volume group.
func (l *LVM) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	ctx, span := l.tracer.Start(ctx, "volume.lvm.csi/GetCapacity")
	defer span.End()

	params := req.GetParameters()
	if params == nil {
		params = make(map[string]string)
	}

	span.SetAttributes(attribute.String("vol.group", DefaultVolumeGroup))

	for k, v := range params {
		span.SetAttributes(attribute.String(k, v))
	}

	accessibleTopology := req.GetAccessibleTopology()
	if accessibleTopology == nil {
		return &csi.GetCapacityResponse{
			AvailableCapacity: 0,
		}, nil
	}
	if len(accessibleTopology.GetSegments()) == 0 {
		return &csi.GetCapacityResponse{
			AvailableCapacity: 0,
		}, nil
	}
	node := req.GetAccessibleTopology().GetSegments()[TopologyKey]
	if node == "" {
		return &csi.GetCapacityResponse{
			AvailableCapacity: 0,
		}, nil
	}

	// Fetch the available capacity for the volume group, or the matching disks
	// if not created yet.
	availableCapacity, err := l.AvailableCapacity(ctx, DefaultVolumeGroup)
	if err != nil {
		return nil, err
	}

	return &csi.GetCapacityResponse{
		AvailableCapacity: availableCapacity,
		MaximumVolumeSize: wrapperspb.Int64(availableCapacity),
	}, nil
}

// TODO(sc): use a cache.
func (l *LVM) AvailableCapacity(ctx context.Context, vgName string) (int64, error) {
	log := log.FromContext(ctx)
	ctx, span := l.tracer.Start(ctx, "volume.lvm.csi/AvailableCapacity", trace.WithAttributes(
		attribute.String("vol.group", vgName),
	))
	defer span.End()

	// If the volume group exists, use its size directly.
	vg, err := l.lvm.GetVolumeGroup(ctx, vgName)
	if lvm.IgnoreNotFound(err) != nil {
		span.SetStatus(codes.Error, "failed to get volume group")
		span.RecordError(err)
		return 0, fmt.Errorf("failed to get volume group: %w", err)
	}
	if vg != nil {
		span.AddEvent("volume group exists", trace.WithAttributes(
			attribute.Int64("vg.size", int64(vg.Size)),
			attribute.Int64("vg.free", int64(vg.Free)),
		))
		return int64(vg.Free), nil
	}

	// Otherwise, the volume group hasn't been created yet, so we can return the
	// total size of all available disks matching the device filter.
	filtered, err := l.probe.ScanAvailableDevices(ctx)
	if err != nil {
		if errors.Is(err, probe.ErrNoDevicesFound) {
			span.SetStatus(codes.Ok, "no devices found matching filter")
			return 0, nil
		}
		span.SetStatus(codes.Error, "failed to scan devices")
		span.RecordError(err)
		return 0, fmt.Errorf("failed to scan devices: %w", err)
	}

	// Get the list of physical volumes to filter out devices that are already
	// allocated to a volume group.
	pvs, err := l.lvm.ListPhysicalVolumes(ctx, nil)
	if err != nil {
		span.SetStatus(codes.Error, "failed to list physical volumes")
		span.RecordError(err)
		return 0, fmt.Errorf("failed to list physical volumes: %w", err)
	}
	isPhysicalVolume := map[string]struct{}{}
	for _, pv := range pvs {
		isPhysicalVolume[pv.Name] = struct{}{}
	}

	// Calculate the available capacity from unallocated disks.
	var availableCapacity int64
	for _, device := range filtered.Devices {
		if _, ok := isPhysicalVolume[device.Path]; ok {
			log.V(2).Info("physical volume already allocated to a volume group", "device", device)
			continue
		}

		usableCapacity := max(device.Size-defaultDataAlignment, 0)
		usableCapacity = (usableCapacity / defaultPeSize) * defaultPeSize
		availableCapacity += usableCapacity
		span.AddEvent("device size", trace.WithAttributes(
			attribute.String("device", device.Path),
			attribute.Int64("size", device.Size),
			attribute.Int64("usableCapacity", usableCapacity),
		))
	}

	span.AddEvent("available disk capacity", trace.WithAttributes(
		attribute.Int64("disks.size", availableCapacity),
	))
	return availableCapacity, nil
}

// ValidateCapabilities the volume capabilities specifically for LVM volumes.
// This doesn't do much now but check if the volume exists.
func (l *LVM) ValidateCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	ctx, span := l.tracer.Start(ctx, "volume.lvm.csi/Validate", trace.WithAttributes(
		attribute.String("vol.name", req.GetVolumeId()),
	))
	defer span.End()

	log := log.FromContext(ctx)
	log.V(1).Info("validating volume")

	id, err := newIdFromString(req.GetVolumeId())
	if err != nil {
		log.Error(err, "failed to parse volume id", "name", req.GetVolumeId())
		span.SetStatus(codes.Error, "failed to parse volume id")
		span.RecordError(err)
		return nil, fmt.Errorf("failed to parse volume id %s: %w", req.GetVolumeId(), err)
	}

	// Volume group is part of the first part of the volume ID. If the volume ID
	// VG is not the default (we do not support multiple volume groups),
	// we return an error.
	if id.VolumeGroup != DefaultVolumeGroup {
		err := fmt.Errorf("invalid volume group %s, expected %s", id.VolumeGroup, DefaultVolumeGroup)
		log.Error(err, "invalid volume group", "expected", DefaultVolumeGroup, "got", id.VolumeGroup)
		span.SetStatus(codes.Error, "invalid volume group")
		span.RecordError(err)
		return nil, err
	}

	span.SetAttributes(attribute.String("vol.group", id.VolumeGroup))

	if _, err := l.lvm.GetLogicalVolume(ctx, id.VolumeGroup, id.LogicalVolume); err != nil {
		if errors.Is(err, lvm.ErrNotFound) {
			return nil, core.ErrVolumeNotFound
		}
		return nil, err
	}
	return &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
			VolumeContext:      req.GetVolumeContext(),
			VolumeCapabilities: req.GetVolumeCapabilities(),
			Parameters:         req.GetParameters(),
			MutableParameters:  req.GetMutableParameters(),
		},
	}, nil
}
