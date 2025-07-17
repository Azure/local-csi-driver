// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm

import (
	"context"
	"fmt"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"go.opentelemetry.io/otel/codes"
	grpcCodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"local-csi-driver/internal/csi/capability"
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
		return nil, status.Error(grpcCodes.InvalidArgument, "volume id is required")
	}

	id, err := newIdFromString(req.GetVolumeId())
	if err != nil {
		span.SetStatus(codes.Error, "failed to parse volume id")
		return nil, status.Errorf(grpcCodes.InvalidArgument, "failed to parse volume id %s: %v", req.GetVolumeId(), err)
	}

	lv, err := l.lvm.GetLogicalVolume(ctx, id.VolumeGroup, id.LogicalVolume)
	if err != nil {
		if lvm.IgnoreNotFound(err) != nil {
			span.SetStatus(codes.Error, "failed to find volume")
			return nil, status.Errorf(grpcCodes.Internal, "failed to find volume %s: %v", req.GetVolumeId(), err)
		}
	}

	if err != nil || lv == nil {
		span.SetStatus(codes.Error, "unable to find volume")
		return nil, status.Errorf(grpcCodes.NotFound, "volume %s not found", req.GetVolumeId())
	}

	if req.GetCapacityRange() == nil || req.GetCapacityRange().GetRequiredBytes() == 0 {
		return nil, status.Error(grpcCodes.InvalidArgument, "capacity range is required")
	}

	capacityRequest := resource.NewQuantity(req.GetCapacityRange().GetRequiredBytes(), resource.BinarySI)
	if capacityRequest == nil {
		span.SetStatus(codes.Error, "invalid capacity range")
		return nil, status.Error(grpcCodes.InvalidArgument, "invalid capacity range")
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

	// Check if the requested size is greater than the current size.
	if capacityRequest.CmpInt64(int64(lv.Size)) < 0 {
		span.SetStatus(codes.Error, "requested size is less than current size")
		return nil, status.Errorf(grpcCodes.FailedPrecondition, "requested size %d is less than current size %d", capacityRequest.Value(), lv.Size)
	}

	// Check if the volume is already expanded.
	if capacityRequest.CmpInt64(int64(lv.Size)) >= 0 {
		span.SetStatus(codes.Error, "volume is already expanded")
		return &csi.NodeExpandVolumeResponse{
			CapacityBytes: req.GetCapacityRange().GetRequiredBytes(),
		}, nil
	}

	recorder := events.FromContext(ctx)
	recorder.Eventf(corev1.EventTypeNormal, expandingLogicalVolume, "Expanding volume %s/%s to %d", id.VolumeGroup, id.LogicalVolume, capacityRequest.Value())
	if err := l.lvm.ExtendLogicalVolume(ctx, extendOps); err != nil {
		span.SetStatus(codes.Error, "unable to expand volume")
		recorder.Eventf(corev1.EventTypeWarning, expandingLogicalVolumeFailed, "Failed to expand volume %s/%s to %d: %v", id.VolumeGroup, id.LogicalVolume, capacityRequest.Value(), err)
		return nil, err
	}
	recorder.Eventf(corev1.EventTypeNormal, expandedLogicalVolume, "Expanded volume %s/%s to %d", id.VolumeGroup, id.LogicalVolume, capacityRequest.Value())
	return &csi.NodeExpandVolumeResponse{
		CapacityBytes: req.GetCapacityRange().GetRequiredBytes(),
	}, nil
}

// NodeEnsureVolume ensures that the volume exists on the node.
// It will create the volume if it does not exist.
func (l *LVM) NodeEnsureVolume(ctx context.Context, volumeId string, capacity int64, limit int64) error {
	_, err := l.EnsureVolume(ctx, volumeId, capacity, limit, true)
	return err
}
