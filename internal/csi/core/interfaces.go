// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package core

import (
	"context"
	"fmt"

	"github.com/container-storage-interface/spec/lib/go/csi"
	storagev1 "k8s.io/api/storage/v1"
)

const (
	// PVCNameParam and PVCNamespaceParam are the keys for the PVC name and
	// namespaces parameters set in the volume create request when
	// --extra-create-metadata is set on the CSI external-provisioner.
	PVCNameParam      = "csi.storage.k8s.io/pvc/name"
	PVCNamespaceParam = "csi.storage.k8s.io/pvc/namespace"
)

var (
	ErrVolumeNotFound     = fmt.Errorf("volume not found")
	ErrInvalidArgument    = fmt.Errorf("invalid argument")
	ErrResourceExhausted  = fmt.Errorf("resource exhausted")
	ErrAlreadyExists      = fmt.Errorf("volume already exists")
	ErrVolumeSizeMismatch = fmt.Errorf("volume size mismatch")
)

type Interface interface {
	ControllerInterface
	NodeInterface
	GetDriverName() string
	GetCSIDriver() *storagev1.CSIDriver
}

type ControllerInterface interface {
	GetControllerDriverCapabilities() []*csi.ControllerServiceCapability
	Create(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.Volume, error)
	Delete(ctx context.Context, req *csi.DeleteVolumeRequest) error
	List(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error)
	GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error)
	ValidateCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error)
	GetVolumeName(volumeId string) (string, error)
	GetNodeDevicePath(volumeId string) (string, error)
	// ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error)
	// ControllerPublish(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error)
	// ControllerUnpublish(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error)
	// ControllerGet(ctx context.Context, req *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error)
	// ControllerModify(ctx context.Context, req *csi.ControllerModifyVolumeRequest) (*csi.ControllerModifyVolumeResponse, error)
}

type NodeInterface interface {
	GetNodeAccessModes() []*csi.VolumeCapability_AccessMode
	GetNodeDriverCapabilities() []*csi.NodeServiceCapability
	GetNodeDevicePath(volumeId string) (string, error)
	NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error)
	NodeEnsureVolume(ctx context.Context, volumeId string, capacity int64, limit int64) error
	GetVolumeName(volumeId string) (string, error)
	// NodeStage(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error)
	// NodeUnstage(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error)
	// NodeUnpublish(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error)
	// NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error)
	// NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error)
	// NodeGetVolumeStats(ctx context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error)
}
