// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	storagev1 "k8s.io/api/storage/v1"
)

type Fake struct {
	Volume           *csi.Volume
	DiskPoolCapacity int64
	Err              error
	BaseDir          string
}

func NewFake() *Fake {
	return &Fake{}
}

func (f *Fake) Create(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.Volume, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return f.Volume, nil
}

func (f *Fake) Delete(ctx context.Context, req *csi.DeleteVolumeRequest) error {
	if f.Err != nil {
		return f.Err
	}
	if f.Volume != nil {
		f.Volume = nil
	}
	return nil
}

func (f *Fake) List(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return &csi.ListVolumesResponse{
		Entries: []*csi.ListVolumesResponse_Entry{
			{
				Volume: f.Volume,
			},
		},
	}, nil
}

func (f *Fake) ValidateCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
			VolumeContext:      req.GetVolumeContext(),
			VolumeCapabilities: req.GetVolumeCapabilities(),
			Parameters:         req.GetParameters(),
			MutableParameters:  req.GetMutableParameters(),
		}}, nil

}

func (f *Fake) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return &csi.GetCapacityResponse{
		AvailableCapacity: f.DiskPoolCapacity,
	}, nil
}

func (f *Fake) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return &csi.NodeExpandVolumeResponse{
		CapacityBytes: f.DiskPoolCapacity,
	}, nil
}

func (f *Fake) NodeEnsureVolume(ctx context.Context, volumeId string, capacity int64, limit int64) error {
	if f.Err != nil {
		return f.Err
	}
	return nil
}

func (f *Fake) GetDriverName() string {
	return "fake-driver"
}

func (f *Fake) GetVolumeName(volumeId string) (string, error) {
	parts := strings.Split(volumeId, "#")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid volume ID: %s", volumeId)
	}
	return parts[1], nil
}

func (f *Fake) GetCSIDriver() *storagev1.CSIDriver {
	return &storagev1.CSIDriver{}
}

func (f *Fake) GetControllerDriverCapabilities() []*csi.ControllerServiceCapability {
	return []*csi.ControllerServiceCapability{}
}

func (f *Fake) GetNodeAccessModes() []*csi.VolumeCapability_AccessMode {
	return []*csi.VolumeCapability_AccessMode{}
}

func (f *Fake) GetNodeDriverCapabilities() []*csi.NodeServiceCapability {
	return []*csi.NodeServiceCapability{}
}

func (f *Fake) GetNodeDevicePath(volumeId string) (string, error) {
	parts := strings.Split(volumeId, "#")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid volume ID: %s", volumeId)
	}
	return f.BaseDir + "/" + strings.Join(parts, "/"), nil
}

func (f *Fake) Cleanup(ctx context.Context) error {
	return nil
}
