// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package gc

import (
	"context"
	"strings"

	lvmMgr "local-csi-driver/internal/pkg/lvm"
)

// MockLVMVolumeManager is a comprehensive mock implementation for testing
// both PV GC and LVM orphan cleanup controllers.
type MockLVMVolumeManager struct {
	*lvmMgr.Fake
	DeletedLVs []string
	VGToLVs    map[string][]string // Track which LVs belong to which VGs
	Volumes    map[string]bool     // Track which volumes exist (for PV GC tests)
}

// NewMockLVMVolumeManager creates a new mock LVM volume manager.
func NewMockLVMVolumeManager() *MockLVMVolumeManager {
	return &MockLVMVolumeManager{
		Fake:       lvmMgr.NewFake(),
		DeletedLVs: make([]string, 0),
		VGToLVs:    make(map[string][]string),
		Volumes:    make(map[string]bool),
	}
}

// CreateLogicalVolume wraps the fake implementation to track VG associations.
func (m *MockLVMVolumeManager) CreateLogicalVolume(ctx context.Context, opts lvmMgr.CreateLVOptions) (int64, error) {
	size, err := m.Fake.CreateLogicalVolume(ctx, opts)
	if err != nil {
		return 0, err
	}

	// Track the relationship between VG and LV
	vgName := opts.VGName
	if m.VGToLVs == nil {
		m.VGToLVs = make(map[string][]string)
	}
	m.VGToLVs[vgName] = append(m.VGToLVs[vgName], opts.Name)

	return size, nil
}

// ListLogicalVolumes with support for VG filtering.
func (m *MockLVMVolumeManager) ListLogicalVolumes(ctx context.Context, opts *lvmMgr.ListLVOptions) ([]lvmMgr.LogicalVolume, error) {
	// If there's a select option for VG filtering, handle it
	if opts != nil && opts.Select != "" {
		// Parse "vg_name=<name>" from select
		if strings.HasPrefix(opts.Select, "vg_name=") {
			vgName := strings.TrimPrefix(opts.Select, "vg_name=")

			// Get LVs for this VG
			lvNames, exists := m.VGToLVs[vgName]
			if !exists {
				return []lvmMgr.LogicalVolume{}, nil
			}

			var filteredLVs []lvmMgr.LogicalVolume
			for _, lvName := range lvNames {
				if lv, ok := m.LVs[lvName]; ok {
					filteredLVs = append(filteredLVs, lv)
				}
			}
			return filteredLVs, nil
		}
	}

	// Default behavior - return all LVs
	return m.Fake.ListLogicalVolumes(ctx, opts)
}

// DeleteVolume implements the LVMVolumeManager interface.
func (m *MockLVMVolumeManager) DeleteVolume(ctx context.Context, volumeID string) error {
	print(volumeID)
	// Parse volume ID to get LV name
	_, lvName, err := parseVolumeID(volumeID)
	if err != nil {
		return err
	}

	// Track deletion for both interfaces
	m.DeletedLVs = append(m.DeletedLVs, volumeID)
	delete(m.Volumes, volumeID)

	opts := lvmMgr.RemoveLVOptions{
		Name: lvName,
	}
	return m.RemoveLogicalVolume(ctx, opts)
}

// GetVolumeName implements the LVMVolumeManager interface.
func (m *MockLVMVolumeManager) GetVolumeName(volumeID string) (string, error) {
	_, lvName, err := parseVolumeID(volumeID)
	return lvName, err
}

// GetNodeDevicePath implements the LVMVolumeManager interface.
func (m *MockLVMVolumeManager) GetNodeDevicePath(volumeID string) (string, error) {
	vgName, lvName, err := parseVolumeID(volumeID)
	if err != nil {
		return "", err
	}

	// For simple PV GC tests, return generic path if volume exists
	if m.Volumes[volumeID] {
		return "/dev/containerstorage/test-volume", nil
	}

	// For more detailed tests, construct the actual device path
	return "/dev/" + vgName + "/" + lvName, nil
}

// UnmountVolume implements the LVMVolumeManager interface.
func (m *MockLVMVolumeManager) UnmountVolume(ctx context.Context, devicePath string) error {
	// Mock implementation - always succeeds
	return nil
}

// ListVolumeGroups implements the LVMVolumeManager interface.
func (m *MockLVMVolumeManager) ListVolumeGroups(ctx context.Context, opts *lvmMgr.ListVGOptions) ([]lvmMgr.VolumeGroup, error) {
	// For testing, return VGs from our tracked VGToLVs
	var vgs []lvmMgr.VolumeGroup
	for vgName := range m.VGToLVs {
		vgs = append(vgs, lvmMgr.VolumeGroup{Name: vgName})
	}
	return vgs, nil
}

// AddVolume is a test helper to add volumes for PV GC controller tests.
func (m *MockLVMVolumeManager) AddVolume(volumeID string) {
	m.Volumes[volumeID] = true
}
