// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package lvm implements the LVM Manager interface.
// Only lvm2 format is supported.
package lvm

import (
	"context"
)

const (
	// SupportedFormat is the supported LVM format.
	SupportedFormat = "lvm2"
)

// Manager is an interface for managing LVM.
//
// This is used to manage LVM resources like PV, VG, and LV and contains methods
// to create, remove, and list them.
//
// We do not have the expand capabilities listed here. In case it is required,
// we can add it later. context is used to pass the context to the functions. It
// is used to cancel the operation if required.
//
//go:generate mockgen -copyright_file ../../../hack/mockgen_copyright.txt -source=manager.go -destination=mock.go -package=lvm
type Manager interface {
	// IsSupported returns true if LVM is supported on the current node.
	IsSupported() bool
	// CreatePhysicalVolume creates a PV for each block device.
	CreatePhysicalVolume(ctx context.Context, opts CreatePVOptions) error
	// RemovePhysicalVolume removes PVs for each block device.
	RemovePhysicalVolume(ctx context.Context, opts RemovePVOptions) error
	// GetPhysicalVolume returns the list of PVs.
	ListPhysicalVolumes(ctx context.Context, opts *ListPVOptions) ([]PhysicalVolume, error)
	// ListPhysicalVolumes returns named PV.
	GetPhysicalVolume(ctx context.Context, pvName string) (*PhysicalVolume, error)
	// CreateVolumeGroup Creates a VG on the PVs.
	CreateVolumeGroup(ctx context.Context, opts CreateVGOptions) error
	// ListVolumeGroups list the specified VGs.
	ListVolumeGroups(ctx context.Context, opts *ListVGOptions) ([]VolumeGroup, error)
	// GetVolumeGroups returns named VG.
	GetVolumeGroup(ctx context.Context, vgName string) (*VolumeGroup, error)
	// RemoveVolumeGroup removes a VG.
	RemoveVolumeGroup(ctx context.Context, opts RemoveVGOptions) error
	// CreateLogicalVolume creates an LV on a VG.
	CreateLogicalVolume(ctx context.Context, opts CreateLVOptions) (int64, error)
	// RemoveLogicalVolume removes a LV from a VG
	RemoveLogicalVolume(ctx context.Context, opts RemoveLVOptions) error
	// ListLogicalVolumes lists the specified LVs.
	ListLogicalVolumes(ctx context.Context, opts *ListLVOptions) ([]LogicalVolume, error)
	// GetLogicalVolumes returns named LV.
	GetLogicalVolume(ctx context.Context, vgName string, lvName string) (*LogicalVolume, error)
	// ExtendLogicalVolume extends the LV to the specified size.
	ExtendLogicalVolume(ctx context.Context, opts ExtendLVOptions) error
	// IsLogicalVolumeCorrupted checks if a logical volume is corrupted.
	// A logical volume is considered corrupted if it exists in LVM metadata
	// but the device file (/dev/<vg>/<lv>) does not exist.
	IsLogicalVolumeCorrupted(ctx context.Context, vgName string, lvName string) (bool, error)
}
