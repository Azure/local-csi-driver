// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm

import (
	"context"
	"errors"
	"fmt"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"go.opentelemetry.io/otel/codes"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"local-csi-driver/internal/csi/capability"
	"local-csi-driver/internal/csi/core"
	"local-csi-driver/internal/pkg/events"
	"local-csi-driver/internal/pkg/lvm"
)

const (
	// expandingLogicalVolume is the event reason for expanding a logical volume.
	expandingLogicalVolume = "ExpandingLogicalVolume"
	// expandingLogicalVolumeFailed is the event reason for failing to expand a logical volume.
	expandingLogicalVolumeFailed = "ExpandingLogicalVolumeFailed"
	// expandedLogicalVolume is the event reason for successfully expanding a logical volume.
	expandedLogicalVolume = "ExpandedLogicalVolume"
)

// GetNodeDriverCapabilities returns the node service capabilities.
func (l *LVM) GetNodeDriverCapabilities() []*csi.NodeServiceCapability {
	caps := make([]*csi.NodeServiceCapability, 0, len(nodeCapabilities))
	for _, cap := range nodeCapabilities {
		caps = append(caps, capability.NewNodeServiceCapability(cap))
	}
	return caps
}

// GetNodeAccessModes returns the access modes supported by the driver.
func (l *LVM) GetNodeAccessModes() []*csi.VolumeCapability_AccessMode {
	return accessModes
}

// NodeExpandVolume implements the csi.NodeServer interface.
// It expands the logical volume with lvm.
func (l *LVM) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	ctx, span := l.tracer.Start(ctx, "volume.lvm.csi/NodeExpandVolume")
	defer span.End()

	if req.GetVolumeId() == "" {
		return nil, fmt.Errorf("%w: volume id is required", core.ErrInvalidArgument)
	}

	id, err := newIdFromString(req.GetVolumeId())
	if err != nil {
		span.SetStatus(codes.Error, "failed to parse volume id")
		return nil, fmt.Errorf("%w: failed to parse volume id %s: %v", core.ErrInvalidArgument, req.GetVolumeId(), err)
	}

	lv, err := l.lvm.GetLogicalVolume(ctx, id.VolumeGroup, id.LogicalVolume)
	if err != nil {
		if lvm.IgnoreNotFound(err) != nil {
			span.SetStatus(codes.Error, "failed to find volume")
			return nil, fmt.Errorf("failed to find volume %s: %w", req.GetVolumeId(), err)
		}
	}

	if err != nil || lv == nil {
		span.SetStatus(codes.Error, "unable to find volume")
		return nil, fmt.Errorf("%w: volume %s not found", core.ErrVolumeNotFound, req.GetVolumeId())
	}

	if req.GetCapacityRange() == nil || req.GetCapacityRange().GetRequiredBytes() == 0 {
		return nil, fmt.Errorf("%w: capacity range is required", core.ErrOutOfRange)
	}

	capacityRequest := resource.NewQuantity(req.GetCapacityRange().GetRequiredBytes(), resource.BinarySI)
	if capacityRequest == nil {
		span.SetStatus(codes.Error, "invalid capacity range")
		return nil, fmt.Errorf("%w: invalid capacity range", core.ErrOutOfRange)
	}

	// Expand the volume on the node.
	// TODO: use default volume group name for now
	extendOps := lvm.ExtendLVOptions{
		Name: fmt.Sprintf("%s/%s", id.VolumeGroup, id.LogicalVolume),
		Size: capacityRequest.String(),
	}

	if req.GetVolumeCapability() != nil {
		if _, ok := req.GetVolumeCapability().GetAccessType().(*csi.VolumeCapability_Mount); ok {
			// only pass ResizeFS config if the volume mode is Filesystem, otherwise
			// the resize operation will fail.
			extendOps.ResizeFS = true
		}
	}

	if capacityRequest.CmpInt64(int64(lv.Size)) <= 0 {
		span.SetStatus(codes.Ok, "volume is already at or above the requested size")
		return &csi.NodeExpandVolumeResponse{
			CapacityBytes: int64(lv.Size),
		}, nil
	}

	recorder := events.FromContext(ctx)
	recorder.Eventf(corev1.EventTypeNormal, expandingLogicalVolume, "Expanding volume %s/%s to %d", id.VolumeGroup, id.LogicalVolume, capacityRequest.Value())
	if err := l.lvm.ExtendLogicalVolume(ctx, extendOps); err != nil {
		span.SetStatus(codes.Error, "unable to expand volume")
		recorder.Eventf(corev1.EventTypeWarning, expandingLogicalVolumeFailed, "Failed to expand volume %s/%s to %d: %v", id.VolumeGroup, id.LogicalVolume, capacityRequest.Value(), err)
		if errors.Is(err, lvm.ErrResourceExhausted) {
			return nil, fmt.Errorf("%w: %v", core.ErrOutOfRange, err)
		}
		return nil, fmt.Errorf("failed to expand volume: %w", err)
	}

	// Re-query the LV to get the actual allocated size after extend.
	expandedLV, err := l.lvm.GetLogicalVolume(ctx, id.VolumeGroup, id.LogicalVolume)
	if err != nil {
		span.SetStatus(codes.Error, "failed to get volume after expand")
		return nil, fmt.Errorf("failed to get volume after expand: %w", err)
	}
	actualSize := int64(expandedLV.Size)

	recorder.Eventf(corev1.EventTypeNormal, expandedLogicalVolume, "Expanded volume %s/%s to %d", id.VolumeGroup, id.LogicalVolume, actualSize)
	return &csi.NodeExpandVolumeResponse{
		CapacityBytes: actualSize,
	}, nil
}

// NodeEnsureVolume ensures that the volume exists on the node.
// It will create the volume if it does not exist.
func (l *LVM) NodeEnsureVolume(ctx context.Context, volumeId string, capacity int64, limit int64) error {
	_, err := l.EnsureVolume(ctx, volumeId, capacity, limit, true)
	return err
}
