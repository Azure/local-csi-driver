// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm

import (
	"context"
	"sort"
	"strconv"
	"strings"
)

type Fake struct {
	PVs map[string]PhysicalVolume
	VGs map[string]VolumeGroup
	LVs map[string]LogicalVolume
	Err error

	// Sanitized records the volumes passed to SanitizeLogicalVolume, in call
	// order, as "vgName/lvName".
	Sanitized []string
}

// Construct a new fakelvm2 client.
func NewFake() *Fake {
	return &Fake{
		PVs: make(map[string]PhysicalVolume),
		VGs: make(map[string]VolumeGroup),
		LVs: make(map[string]LogicalVolume),
		Err: nil,
	}
}

// IsSupported returns true if LVM is supported on the current node.
func (f *Fake) IsSupported() bool {
	return true
}

// CreatePhysicalVolume creates a PV for each block device.
func (f *Fake) CreatePhysicalVolume(ctx context.Context, opts CreatePVOptions) error {
	pv := PhysicalVolume{
		Name: opts.Name,
	}
	f.PVs[opts.Name] = pv
	return nil
}

// RemovePhysicalVolume removes PVs for each block device.
func (f *Fake) RemovePhysicalVolume(ctx context.Context, opts RemovePVOptions) error {
	delete(f.PVs, opts.Name)
	return nil
}

// GetPhysicalVolume returns the list of PVs.
func (f *Fake) ListPhysicalVolumes(ctx context.Context, opts *ListPVOptions) ([]PhysicalVolume, error) {
	pvs := make([]PhysicalVolume, 0, len(f.PVs))
	for _, pv := range f.PVs {
		pvs = append(pvs, pv)
	}
	return pvs, nil
}

// ListPhysicalVolumes returns named PV.
func (f *Fake) GetPhysicalVolume(ctx context.Context, pvName string) (*PhysicalVolume, error) {
	pv, ok := f.PVs[pvName]
	if !ok {
		return nil, ErrNotFound
	}
	return &pv, nil
}

// CreateVolumeGroup Creates a VG on the PVs.
func (f *Fake) CreateVolumeGroup(ctx context.Context, opts CreateVGOptions) error {
	vg := VolumeGroup{
		Name: opts.Name,
	}
	f.VGs[opts.Name] = vg
	return nil
}

// ListVolumeGroups list the specified VGs.
func (f *Fake) ListVolumeGroups(ctx context.Context, opts *ListVGOptions) ([]VolumeGroup, error) {
	vgs := make([]VolumeGroup, 0, len(f.VGs))
	for _, vg := range f.VGs {
		vgs = append(vgs, vg)
	}
	return vgs, nil
}

// GetVolumeGroups returns named VG.
func (f *Fake) GetVolumeGroup(ctx context.Context, vgName string) (*VolumeGroup, error) {
	vg, ok := f.VGs[vgName]
	if !ok {
		return nil, ErrNotFound
	}
	return &vg, nil
}

// RemoveVolumeGroup removes a VG.
func (f *Fake) RemoveVolumeGroup(ctx context.Context, opts RemoveVGOptions) error {
	delete(f.VGs, opts.Name)
	return nil
}

// CreateLogicalVolume creates an LV on a VG.
func (f *Fake) CreateLogicalVolume(ctx context.Context, opts CreateLVOptions) (int64, error) {
	// Parse the requested size (format: "123456B")
	sizeStr := opts.Size
	sizeStr = strings.TrimSuffix(sizeStr, "B")

	requestedSize, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil {
		return 0, err
	}

	// Simulate LVM extent allocation (default 4MiB extents)
	const extentSize = 4 * 1024 * 1024                             // 4 MiB
	extentsNeeded := (requestedSize + extentSize - 1) / extentSize // Round up
	actualSize := extentsNeeded * extentSize

	lv := LogicalVolume{
		Name: opts.Name,
		Size: Int64String(actualSize),
	}
	f.LVs[opts.Name] = lv

	return actualSize, nil
}

// RemoveLogicalVolume removes a LV from a VG.
func (f *Fake) RemoveLogicalVolume(ctx context.Context, opts RemoveLVOptions) error {
	delete(f.LVs, opts.Name)
	return nil
}

// SanitizeLogicalVolume records the volume as sanitized.
//
// The record lets tests assert that extents were zeroed, and that it happened
// before the volume was removed, which is the ordering the guarantee depends
// on.
func (f *Fake) SanitizeLogicalVolume(ctx context.Context, vgName string, lvName string) error {
	if _, ok := f.LVs[lvName]; !ok {
		return ErrNotFound
	}
	f.Sanitized = append(f.Sanitized, vgName+"/"+lvName)
	return nil
}

// UpdateLogicalVolume applies tag changes to a LV.
func (f *Fake) UpdateLogicalVolume(ctx context.Context, opts UpdateLVOptions) error {
	key := fakeLVKey(opts.Name)
	lv, ok := f.LVs[key]
	if !ok {
		return ErrNotFound
	}

	tags := splitTags(lv.Tags)
	for _, del := range opts.DelTags {
		delete(tags, del)
	}
	for _, add := range opts.AddTags {
		tags[add] = struct{}{}
	}

	lv.Tags = joinTags(tags)
	f.LVs[key] = lv
	return nil
}

// RenameLogicalVolume renames a LV within its VG.
func (f *Fake) RenameLogicalVolume(ctx context.Context, opts RenameLVOptions) error {
	from, to := fakeLVKey(opts.From), fakeLVKey(opts.To)

	lv, ok := f.LVs[from]
	if !ok {
		return ErrNotFound
	}
	if _, exists := f.LVs[to]; exists {
		return ErrAlreadyExists
	}

	lv.Name = to
	delete(f.LVs, from)
	f.LVs[to] = lv
	return nil
}

// fakeLVKey normalises a "vgName/lvName" reference to the bare LV name used to
// key the fake's LV map.
func fakeLVKey(name string) string {
	if _, lvName, ok := strings.Cut(name, "/"); ok {
		return lvName
	}
	return name
}

// splitTags parses the comma-separated tag list that LVM reports.
func splitTags(tags string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, tag := range strings.Split(tags, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			set[tag] = struct{}{}
		}
	}
	return set
}

// joinTags renders a tag set the way LVM reports it.
func joinTags(tags map[string]struct{}) string {
	out := make([]string, 0, len(tags))
	for tag := range tags {
		out = append(out, tag)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// ListLogicalVolumes lists the specified LVs.
func (f *Fake) ListLogicalVolumes(ctx context.Context, opts *ListLVOptions) ([]LogicalVolume, error) {
	lvs := make([]LogicalVolume, 0, len(f.LVs))
	for _, lv := range f.LVs {
		lvs = append(lvs, lv)
	}
	return lvs, nil
}

// GetLogicalVolumes returns named LV.
func (f *Fake) GetLogicalVolume(ctx context.Context, vgName string, lvName string) (*LogicalVolume, error) {
	lv, ok := f.LVs[lvName]
	if !ok {
		return nil, ErrNotFound
	}
	return &lv, nil
}

// ExtendLogicalVolume extends the LV to the specified size.
func (f *Fake) ExtendLogicalVolume(ctx context.Context, opts ExtendLVOptions) error {
	lv, ok := f.LVs[opts.Name]
	if !ok {
		return ErrNotFound
	}
	size, err := strconv.ParseInt(opts.Size, 10, 64)
	if err != nil {
		return err
	}
	lv.Size = Int64String(size)
	f.LVs[opts.Name] = lv
	return nil
}

// IsLogicalVolumeCorrupted always returns false for the fake implementation.
func (f *Fake) IsLogicalVolumeCorrupted(ctx context.Context, vgName string, lvName string) (bool, error) {
	if f.Err != nil {
		return false, f.Err
	}
	// For fake implementation, we never simulate corruption by default
	return false, nil
}

// RemoveStaleDeviceMapperNodes is a no-op for the fake implementation.
func (f *Fake) RemoveStaleDeviceMapperNodes(ctx context.Context, vgName string) error {
	return f.Err
}
