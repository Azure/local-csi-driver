// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package gc

import (
	"context"
	"errors"
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

	// Take the volume out of service rather than removing it.
	//
	// Garbage collection reclaims volumes whose data still belongs to a
	// tenant, so its extents must be zeroed before they return to the volume
	// group's free pool, exactly as on the CSI deletion path. Quarantine keeps
	// them allocated until the wipe reaper has cleared them.
	if _, err := a.lvmCore.Quarantine(ctx, vgName, lvName); err != nil {
		// If the volume doesn't exist, consider it a success. A volume group
		// that cannot currently be seen does not qualify: that is usually
		// temporary, and treating it as proof of absence would let the caller
		// drop its record of a volume that still exists on disk.
		if lvmMgr.IgnoreNotFound(err) == nil && !errors.Is(err, lvmMgr.ErrVolumeGroupNotFound) {
			return nil
		}
		return fmt.Errorf("failed to quarantine logical volume %s/%s: %w", vgName, lvName, err)
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
