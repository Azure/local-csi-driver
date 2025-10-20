// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package gc

import (
	"context"
	"fmt"

	"local-csi-driver/internal/csi/core/lvm"
	"local-csi-driver/internal/csi/mounter"
	lvmMgr "local-csi-driver/internal/pkg/lvm"
)

// LVMVolumeManager provides an interface for LVM volume operations.
type LVMVolumeManager interface {
	// DeleteVolume deletes an LVM logical volume by volume ID
	DeleteVolume(ctx context.Context, volumeID string) error
	// GetVolumeName extracts the volume name from a volume ID
	GetVolumeName(volumeID string) (string, error)
	// GetNodeDevicePath returns the device path for a volume ID
	GetNodeDevicePath(volumeID string) (string, error)
	// UnmountVolume unmounts a volume at the specified device path
	UnmountVolume(ctx context.Context, devicePath string) error
	// ListLogicalVolumes lists logical volumes for the cleanup controller
	ListLogicalVolumes(ctx context.Context, opts *lvmMgr.ListLVOptions) ([]lvmMgr.LogicalVolume, error)
	// ListVolumeGroups lists volume groups
	ListVolumeGroups(ctx context.Context, opts *lvmMgr.ListVGOptions) ([]lvmMgr.VolumeGroup, error)
}

// lvmVolumeManagerAdapter adapts the LVM core interface to our controller needs.
type lvmVolumeManagerAdapter struct {
	lvmCore    *lvm.LVM
	lvmManager lvmMgr.Manager
	mounter    mounter.Interface
}

func (a *lvmVolumeManagerAdapter) DeleteVolume(ctx context.Context, volumeID string) error {
	// Parse the volume ID to get volume group and logical volume names
	// Volume ID format is: <volume-group>#<logical-volume>
	vgName, lvName, err := parseVolumeID(volumeID)
	if err != nil {
		return fmt.Errorf("failed to parse volume ID %s: %w", volumeID, err)
	}

	// Format volume ID for LVM operations (vg/lv format)
	lvmVolumeID := fmt.Sprintf("%s/%s", vgName, lvName)

	// Use the LVM manager to remove the logical volume directly
	removeOpts := lvmMgr.RemoveLVOptions{Name: lvmVolumeID}

	if err := a.lvmManager.RemoveLogicalVolume(ctx, removeOpts); err != nil {
		// If the volume doesn't exist, consider it a success
		if lvmMgr.IgnoreNotFound(err) == nil {
			return nil
		}
		return fmt.Errorf("failed to remove logical volume %s: %w", lvmVolumeID, err)
	}

	return nil
}

func (a *lvmVolumeManagerAdapter) GetVolumeName(volumeID string) (string, error) {
	return a.lvmCore.GetVolumeName(volumeID)
}

func (a *lvmVolumeManagerAdapter) GetNodeDevicePath(volumeID string) (string, error) {
	return a.lvmCore.GetNodeDevicePath(volumeID)
}

func (a *lvmVolumeManagerAdapter) UnmountVolume(ctx context.Context, devicePath string) error {
	return a.mounter.CleanupStagingDir(ctx, devicePath)
}

func (a *lvmVolumeManagerAdapter) ListLogicalVolumes(ctx context.Context, opts *lvmMgr.ListLVOptions) ([]lvmMgr.LogicalVolume, error) {
	return a.lvmManager.ListLogicalVolumes(ctx, opts)
}

func (a *lvmVolumeManagerAdapter) ListVolumeGroups(ctx context.Context, opts *lvmMgr.ListVGOptions) ([]lvmMgr.VolumeGroup, error) {
	return a.lvmManager.ListVolumeGroups(ctx, opts)
}
