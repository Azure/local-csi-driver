// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm

import (
	"context"
	"errors"
)

var (
	// ErrUnsupported is returned when lvm is not supported.
	ErrUnsupported = errors.New("lvm not supported")
)

// Noop satisfies the LVM interface but returns an unimplemented error for all
// methods.
type Noop struct{}

var _ Manager = &Noop{}

// Unsupported creates a new LVM manager that only returns unsupported errors.
func Unsupported() *Noop {
	return &Noop{}
}

// IsSupported returns false.
func (l *Noop) IsSupported() bool {
	return false
}

// CreateLogicalVolume implements Manager.
func (l *Noop) CreateLogicalVolume(ctx context.Context, opts CreateLVOptions) (int64, error) {
	return 0, ErrUnsupported
}

// CreatePhysicalVolume implements Manager.
func (l *Noop) CreatePhysicalVolume(ctx context.Context, opts CreatePVOptions) error {
	return ErrUnsupported
}

// CreateVolumeGroup implements Manager.
func (l *Noop) CreateVolumeGroup(ctx context.Context, opts CreateVGOptions) error {
	return ErrUnsupported
}

// ListLogicalVolumes implements Manager.
func (l *Noop) ListLogicalVolumes(ctx context.Context, opts *ListLVOptions) ([]LogicalVolume, error) {
	return nil, ErrUnsupported
}

// GetLogicalVolume implements Manager.
func (l *Noop) GetLogicalVolume(ctx context.Context, vgName string, lvName string) (*LogicalVolume, error) {
	return nil, ErrUnsupported
}

// ListPhysicalVolumes implements Manager.
func (l *Noop) ListPhysicalVolumes(ctx context.Context, opts *ListPVOptions) ([]PhysicalVolume, error) {
	return nil, ErrUnsupported
}

// GetPhysicalVolume implements Manager.
func (l *Noop) GetPhysicalVolume(ctx context.Context, pvName string) (*PhysicalVolume, error) {
	return nil, ErrUnsupported
}

// ListVolumeGroups implements Manager.
func (l *Noop) ListVolumeGroups(ctx context.Context, opts *ListVGOptions) ([]VolumeGroup, error) {
	return nil, ErrUnsupported
}

// GetVolumeGroups implements Manager.
func (l *Noop) GetVolumeGroup(ctx context.Context, vgName string) (*VolumeGroup, error) {
	return nil, ErrUnsupported
}

// RemoveLogicalVolume implements Manager.
func (l *Noop) RemoveLogicalVolume(ctx context.Context, opts RemoveLVOptions) error {
	return ErrUnsupported
}

// RemovePhysicalVolume implements Manager.
func (l *Noop) RemovePhysicalVolume(ctx context.Context, opts RemovePVOptions) error {
	return ErrUnsupported
}

// RemoveVolumeGroup implements Manager.
func (l *Noop) RemoveVolumeGroup(ctx context.Context, opts RemoveVGOptions) error {
	return ErrUnsupported
}

// ExtendLogicalVolume implements Manager.
func (l *Noop) ExtendLogicalVolume(ctx context.Context, opts ExtendLVOptions) error {
	return ErrUnsupported
}
