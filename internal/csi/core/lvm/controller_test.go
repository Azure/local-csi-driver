// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"go.uber.org/mock/gomock"

	"local-csi-driver/internal/csi/core/lvm"
	"local-csi-driver/internal/pkg/block"
	lvmMgr "local-csi-driver/internal/pkg/lvm"
	"local-csi-driver/internal/pkg/probe"
	"local-csi-driver/internal/pkg/telemetry"
)

const (
	testVolumeGroup = "test-vg"
)

func TestLVM_Create(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		req     *csi.CreateVolumeRequest
		want    *csi.Volume
		wantErr bool
	}{
		{
			name: "empty params",
			req: &csi.CreateVolumeRequest{
				Name: "test-volume",
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 1024 * 1024 * 1024, // 1 GiB
				},
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Block{
							Block: &csi.VolumeCapability_BlockVolume{},
						},
					},
				},
				Parameters: map[string]string{},
			},
			want: &csi.Volume{
				VolumeId:      "containerstorage#test-volume",
				CapacityBytes: 1024 * 1024 * 1024, // 1 GiB
				VolumeContext: map[string]string{
					"localdisk.csi.acstor.io/capacity": "1073741824",
					"localdisk.csi.acstor.io/limit":    "0",
				},
				AccessibleTopology: []*csi.Topology{
					{
						Segments: map[string]string{
							"topology.localdisk.csi.acstor.io/node": "nodename",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "custom volume group",
			req: &csi.CreateVolumeRequest{
				Name: "test-volume",
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 1024 * 1024 * 1024, // 1 GiB
				},
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Block{
							Block: &csi.VolumeCapability_BlockVolume{},
						},
					},
				},
				Parameters: map[string]string{
					"volumeGroup": "custom-vg",
				},
			},
			want: &csi.Volume{
				VolumeId:      "custom-vg#test-volume",
				CapacityBytes: 1024 * 1024 * 1024, // 1 GiB
				VolumeContext: map[string]string{
					"localdisk.csi.acstor.io/capacity": "1073741824",
					"localdisk.csi.acstor.io/limit":    "0",
					"volumeGroup":                      "custom-vg",
				},
				AccessibleTopology: []*csi.Topology{
					{
						Segments: map[string]string{
							"topology.localdisk.csi.acstor.io/node": "nodename",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "empty params",
			req: &csi.CreateVolumeRequest{
				Name: "test-volume",
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 1024 * 1024 * 1024, // 1 GiB
				},
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Block{
							Block: &csi.VolumeCapability_BlockVolume{},
						},
					},
				},
				Parameters: map[string]string{},
			},
			want: &csi.Volume{
				VolumeId:      "containerstorage#test-volume",
				CapacityBytes: 1024 * 1024 * 1024, // 1 GiB
				VolumeContext: map[string]string{
					"localdisk.csi.acstor.io/capacity": "1073741824",
					"localdisk.csi.acstor.io/limit":    "0",
				},
				AccessibleTopology: []*csi.Topology{
					{
						Segments: map[string]string{
							"topology.localdisk.csi.acstor.io/node": "nodename",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "non-aligned capacity",
			req: &csi.CreateVolumeRequest{
				Name: "test-volume",
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 1919999279104, // 1831054Mi
				},
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Block{
							Block: &csi.VolumeCapability_BlockVolume{},
						},
					},
				},
				Parameters: map[string]string{},
			},
			want: &csi.Volume{
				VolumeId:      "containerstorage#test-volume",
				CapacityBytes: 1920001376256, // Rounded up to 4MiB boundary
				VolumeContext: map[string]string{
					"localdisk.csi.acstor.io/capacity": "1920001376256",
					"localdisk.csi.acstor.io/limit":    "0",
				},
				AccessibleTopology: []*csi.Topology{
					{
						Segments: map[string]string{
							"topology.localdisk.csi.acstor.io/node": "nodename",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "with limit",
			req: &csi.CreateVolumeRequest{
				Name: "test-volume",
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 1024 * 1024 * 1024,     // 1 GiB
					LimitBytes:    2 * 1024 * 1024 * 1024, // 2 GiB
				},
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Block{
							Block: &csi.VolumeCapability_BlockVolume{},
						},
					},
				},
				Parameters: map[string]string{},
			},
			want: &csi.Volume{
				VolumeId:      "containerstorage#test-volume",
				CapacityBytes: 1073741824, // 1 GiB
				VolumeContext: map[string]string{
					"localdisk.csi.acstor.io/capacity": "1073741824",
					"localdisk.csi.acstor.io/limit":    "2147483648",
				},
				AccessibleTopology: []*csi.Topology{
					{
						Segments: map[string]string{
							"topology.localdisk.csi.acstor.io/node": "nodename",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "with limit lower than request",
			req: &csi.CreateVolumeRequest{
				Name: "test-volume",
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 2 * 1024 * 1024 * 1024, // 2 GiB
					LimitBytes:    1024 * 1024 * 1024,     // 1 GiB
				},
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Block{
							Block: &csi.VolumeCapability_BlockVolume{},
						},
					},
				},
				Parameters: map[string]string{},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "lvm extent boundary allocation",
			req: &csi.CreateVolumeRequest{
				Name: "test-volume",
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 1073741824 + 2097152, // 1 GiB + 2 MiB (not aligned to 4MiB boundary)
				},
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Block{
							Block: &csi.VolumeCapability_BlockVolume{},
						},
					},
				},
				Parameters: map[string]string{},
			},
			want: &csi.Volume{
				VolumeId:      "containerstorage#test-volume",
				CapacityBytes: 1077936128, // 1 GiB + 4 MiB (rounded up to next 4MiB boundary)
				VolumeContext: map[string]string{
					"localdisk.csi.acstor.io/capacity": "1077936128", // actual allocated size
					"localdisk.csi.acstor.io/limit":    "0",
				},
				AccessibleTopology: []*csi.Topology{
					{
						Segments: map[string]string{
							"topology.localdisk.csi.acstor.io/node": "nodename",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "lvm extent boundary allocation with limit",
			req: &csi.CreateVolumeRequest{
				Name: "test-volume",
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 1073741824 + 2097152, // 1 GiB + 2 MiB (not aligned to 4MiB boundary)
					LimitBytes:    2147483648 + 3145728, // 2 GiB + 3 MiB (not aligned to 4MiB boundary)
				},
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Block{
							Block: &csi.VolumeCapability_BlockVolume{},
						},
					},
				},
				Parameters: map[string]string{},
			},
			want: &csi.Volume{
				VolumeId:      "containerstorage#test-volume",
				CapacityBytes: 1077936128, // 1 GiB + 4 MiB (rounded up to next 4MiB boundary)
				VolumeContext: map[string]string{
					"localdisk.csi.acstor.io/capacity": "1077936128", // actual allocated size
					// We won't be rounding up the limit. Its a validation and not allocation.
					"localdisk.csi.acstor.io/limit": "2150629376", // Limit won't be rounded up
				},
				AccessibleTopology: []*csi.Topology{
					{
						Segments: map[string]string{
							"topology.localdisk.csi.acstor.io/node": "nodename",
						},
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		var tt = tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tp := telemetry.NewNoopTracerProvider()
			p := probe.NewFake([]string{"device1", "device2"}, nil)
			lvmMgr := lvmMgr.NewFake()

			l, err := lvm.New("podname", "nodename", "default", true, p, lvmMgr, tp)
			if err != nil {
				t.Fatalf("failed to create LVM instance: %v", err)
			}
			got, err := l.Create(context.Background(), tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("LVM.Create() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("LVM.Create()\ngot:\n%v\nwant:\n%v", got, tt.want)
			}
		})
	}
}

func TestAvailableCapacity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		vgName      string
		expectLvm   func(*lvmMgr.MockManager)
		expectProbe func(*probe.Mock)
		expectedCap int64
		expectedErr error
	}{
		{
			name:   "no devices found",
			vgName: testVolumeGroup,
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetVolumeGroup(gomock.Any(), testVolumeGroup).Return(nil, lvmMgr.ErrNotFound)
			},
			expectProbe: func(p *probe.Mock) {
				p.EXPECT().ScanAvailableDevices(gomock.Any()).Return(nil, probe.ErrNoDevicesFound)
			},
			expectedCap: 0,
		},
		{
			name:   "no devices matching filter",
			vgName: testVolumeGroup,
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetVolumeGroup(gomock.Any(), testVolumeGroup).Return(nil, lvmMgr.ErrNotFound)
			},
			expectProbe: func(p *probe.Mock) {
				p.EXPECT().ScanAvailableDevices(gomock.Any()).Return(nil, probe.ErrNoDevicesFound)
			},
			expectedCap: 0,
		},
		{
			name:   "existing volume group",
			vgName: testVolumeGroup,
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetVolumeGroup(gomock.Any(), testVolumeGroup).Return(&lvmMgr.VolumeGroup{
					Name: testVolumeGroup,
					Free: 1024 * 1024 * 1024, // 1 GiB
				}, nil)
			},
			expectedCap: 1024 * 1024 * 1024,
		},
		{
			name:   "single device with enough capacity",
			vgName: testVolumeGroup,
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetVolumeGroup(gomock.Any(), testVolumeGroup).Return(nil, lvmMgr.ErrNotFound)
				m.EXPECT().ListPhysicalVolumes(gomock.Any(), gomock.Any()).Return([]lvmMgr.PhysicalVolume{}, nil)
			},
			expectProbe: func(p *probe.Mock) {
				devices := &block.DeviceList{
					Devices: []block.Device{
						{
							Path: "/dev/sdb",
							Size: 10 * 1024 * 1024, // 10 MiB - will become 9 MiB, rounded down to 8 MiB
						},
					},
				}
				p.EXPECT().ScanAvailableDevices(gomock.Any()).Return(devices, nil)
			},
			expectedCap: 8 * 1024 * 1024,
		},
		{
			name:   "two device with enough capacity",
			vgName: testVolumeGroup,
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetVolumeGroup(gomock.Any(), testVolumeGroup).Return(nil, lvmMgr.ErrNotFound)
				m.EXPECT().ListPhysicalVolumes(gomock.Any(), gomock.Any()).Return([]lvmMgr.PhysicalVolume{}, nil)
			},
			expectProbe: func(p *probe.Mock) {
				devices := &block.DeviceList{
					Devices: []block.Device{
						{
							Path: "/dev/sdb",
							Size: 10 * 1024 * 1024, // 10 MiB - will become 9 MiB, rounded down to 8 MiB
						},
						{
							Path: "/dev/sda",
							Size: 10 * 1024 * 1024, // 10 MiB - will become 9 MiB, rounded down to 8 MiB
						},
					},
				}
				p.EXPECT().ScanAvailableDevices(gomock.Any()).Return(devices, nil)
			},
			expectedCap: 16 * 1024 * 1024,
		},
		{
			name:   "two device with enough capacity with one allocated to another VG",
			vgName: testVolumeGroup,
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetVolumeGroup(gomock.Any(), testVolumeGroup).Return(nil, lvmMgr.ErrNotFound)
				m.EXPECT().ListPhysicalVolumes(gomock.Any(), gomock.Any()).Return([]lvmMgr.PhysicalVolume{
					{
						Name:   "/dev/sda",
						VGName: "other-vg",
					},
					{
						Name:   "/dev/sdb",
						VGName: "", // unallocated
					},
				}, nil)
			},
			expectProbe: func(p *probe.Mock) {
				devices := &block.DeviceList{
					Devices: []block.Device{
						{
							Path: "/dev/sda",
							Size: 10 * 1024 * 1024, // 10 MiB - will become 9 MiB, rounded down to 8 MiB
						},
						{
							Path: "/dev/sdb",
							Size: 20 * 1024 * 1024, // 10 MiB - will become 9 MiB, rounded down to 8 MiB
						},
					},
				}
				p.EXPECT().ScanAvailableDevices(gomock.Any()).Return(devices, nil)
			},
			expectedCap: 16 * 1024 * 1024,
		},
		{
			name:   "two device with enough capacity with one allocated to the VG (could happen if VG created during request)",
			vgName: testVolumeGroup,
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetVolumeGroup(gomock.Any(), testVolumeGroup).Return(nil, lvmMgr.ErrNotFound)
				m.EXPECT().ListPhysicalVolumes(gomock.Any(), gomock.Any()).Return([]lvmMgr.PhysicalVolume{
					{
						Name:   "/dev/sda",
						VGName: testVolumeGroup,
					},
					{
						Name:   "/dev/sdb",
						VGName: testVolumeGroup,
					},
				}, nil)
			},
			expectProbe: func(p *probe.Mock) {
				devices := &block.DeviceList{
					Devices: []block.Device{
						{
							Path: "/dev/sdb",
							Size: 20 * 1024 * 1024, // 20 MiB - will become 19 MiB, rounded down to 16 MiB
						},
						{
							Path: "/dev/sda",
							Size: 10 * 1024 * 1024, // 10 MiB - will become 9 MiB, rounded down to 8 MiB
						},
					},
				}
				p.EXPECT().ScanAvailableDevices(gomock.Any()).Return(devices, nil)
			},
			expectedCap: 24 * 1024 * 1024,
		},
		{
			name:   "small device with enough not enough capacity for any PE",
			vgName: testVolumeGroup,
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetVolumeGroup(gomock.Any(), testVolumeGroup).Return(nil, lvmMgr.ErrNotFound)
				m.EXPECT().ListPhysicalVolumes(gomock.Any(), gomock.Any()).Return([]lvmMgr.PhysicalVolume{}, nil)
			},
			expectProbe: func(p *probe.Mock) {
				devices := &block.DeviceList{
					Devices: []block.Device{
						{
							Path: "/dev/sdb",
							Size: 5 * 1024, // 5 KiB - not enough for even one PE (4 MiB)
						},
					},
				}
				p.EXPECT().ScanAvailableDevices(gomock.Any()).Return(devices, nil)
			},
			expectedCap: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			l, p, m, err := initTestLVM(gomock.NewController(t))
			if err != nil {
				t.Fatalf("failed to initialize LVM: %v", err)
			}
			if tt.expectLvm != nil {
				tt.expectLvm(m)
			}
			if tt.expectProbe != nil {
				tt.expectProbe(p)
			}
			cap, err := l.AvailableCapacity(context.Background(), tt.vgName)
			if !errors.Is(err, tt.expectedErr) {
				t.Errorf("EnsureVolume() error = %v, expectErr %v", err, tt.expectedErr)
			}
			if cap != tt.expectedCap {
				t.Errorf("EnsureVolume() cap = %d, expectCap %d", cap, tt.expectedCap)
			}
		})
	}
}

func TestLVM_List(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		expectLVM       func(*lvmMgr.MockManager)
		expectedEntries int
		wantErr         bool
	}{
		{
			name: "no volume group exists",
			expectLVM: func(m *lvmMgr.MockManager) {
				m.EXPECT().ListVolumeGroups(gomock.Any(), &lvmMgr.ListVGOptions{
					Select: "vg_tags=" + lvm.DefaultVolumeGroupTag,
				}).Return([]lvmMgr.VolumeGroup{}, nil)
			},
			expectedEntries: 0,
			wantErr:         false,
		},
		{
			name: "volume group exists with logical volumes",
			expectLVM: func(m *lvmMgr.MockManager) {
				m.EXPECT().ListVolumeGroups(gomock.Any(), &lvmMgr.ListVGOptions{
					Select: "vg_tags=" + lvm.DefaultVolumeGroupTag,
				}).Return([]lvmMgr.VolumeGroup{
					{Name: lvm.DefaultVolumeGroup},
				}, nil)
				m.EXPECT().ListLogicalVolumes(gomock.Any(), &lvmMgr.ListLVOptions{
					Select: "vg_name=" + lvm.DefaultVolumeGroup,
				}).Return([]lvmMgr.LogicalVolume{
					{Name: "test-lv-1", Size: lvmMgr.Int64String(1024 * 1024 * 1024)}, // 1 GiB
					{Name: "test-lv-2", Size: lvmMgr.Int64String(2048 * 1024 * 1024)}, // 2 GiB
				}, nil)
			},
			expectedEntries: 2,
			wantErr:         false,
		},
		{
			name: "volume group exists with no logical volumes",
			expectLVM: func(m *lvmMgr.MockManager) {
				m.EXPECT().ListVolumeGroups(gomock.Any(), &lvmMgr.ListVGOptions{
					Select: "vg_tags=" + lvm.DefaultVolumeGroupTag,
				}).Return([]lvmMgr.VolumeGroup{
					{Name: lvm.DefaultVolumeGroup},
				}, nil)
				m.EXPECT().ListLogicalVolumes(gomock.Any(), &lvmMgr.ListLVOptions{
					Select: "vg_name=" + lvm.DefaultVolumeGroup,
				}).Return([]lvmMgr.LogicalVolume{}, nil)
			},
			expectedEntries: 0,
			wantErr:         false,
		},
		{
			name: "list volume groups error",
			expectLVM: func(m *lvmMgr.MockManager) {
				m.EXPECT().ListVolumeGroups(gomock.Any(), &lvmMgr.ListVGOptions{
					Select: "vg_tags=" + lvm.DefaultVolumeGroupTag,
				}).Return(nil, errors.New("list VG failed"))
			},
			expectedEntries: 0,
			wantErr:         true,
		},
		{
			name: "list logical volumes error",
			expectLVM: func(m *lvmMgr.MockManager) {
				m.EXPECT().ListVolumeGroups(gomock.Any(), &lvmMgr.ListVGOptions{
					Select: "vg_tags=" + lvm.DefaultVolumeGroupTag,
				}).Return([]lvmMgr.VolumeGroup{
					{Name: lvm.DefaultVolumeGroup},
				}, nil)
				m.EXPECT().ListLogicalVolumes(gomock.Any(), &lvmMgr.ListLVOptions{
					Select: "vg_name=" + lvm.DefaultVolumeGroup,
				}).Return(nil, errors.New("list LV failed"))
			},
			expectedEntries: 0,
			wantErr:         true,
		},
		{
			name: "multiple volume groups with same tag",
			expectLVM: func(m *lvmMgr.MockManager) {
				m.EXPECT().ListVolumeGroups(gomock.Any(), &lvmMgr.ListVGOptions{
					Select: "vg_tags=" + lvm.DefaultVolumeGroupTag,
				}).Return([]lvmMgr.VolumeGroup{
					{Name: lvm.DefaultVolumeGroup},
					{Name: "another-vg"},
				}, nil)
				m.EXPECT().ListLogicalVolumes(gomock.Any(), &lvmMgr.ListLVOptions{
					Select: "vg_name=" + lvm.DefaultVolumeGroup,
				}).Return([]lvmMgr.LogicalVolume{
					{Name: "test-lv-1", Size: lvmMgr.Int64String(1024 * 1024 * 1024)}, // 1 GiB
				}, nil)
				m.EXPECT().ListLogicalVolumes(gomock.Any(), &lvmMgr.ListLVOptions{
					Select: "vg_name=another-vg",
				}).Return([]lvmMgr.LogicalVolume{
					{Name: "test-lv-2", Size: lvmMgr.Int64String(2048 * 1024 * 1024)}, // 2 GiB
				}, nil)
			},
			expectedEntries: 2,
			wantErr:         false,
		},
	}

	for _, tt := range tests {
		var tt = tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockLVM := lvmMgr.NewMockManager(ctrl)
			tt.expectLVM(mockLVM)

			tp := telemetry.NewNoopTracerProvider()
			p := probe.NewFake([]string{"device1"}, nil)

			l, err := lvm.New("test-pod", "test-node", "test-namespace", false, p, mockLVM, tp)
			if err != nil {
				t.Fatal(err)
			}

			req := &csi.ListVolumesRequest{}
			resp, err := l.List(context.Background(), req)

			if tt.wantErr {
				if err == nil {
					t.Errorf("List() expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("List() unexpected error: %v", err)
				return
			}

			if len(resp.Entries) != tt.expectedEntries {
				t.Errorf("List() expected %d entries, got %d", tt.expectedEntries, len(resp.Entries))
			}

			// Verify volume ID format and topology for each entry
			for _, entry := range resp.Entries {
				if entry.Volume == nil {
					t.Errorf("List() entry has nil Volume")
					continue
				}

				// Verify volume ID format (should be <vg>#<lv>)
				if len(entry.Volume.VolumeId) == 0 || !contains(entry.Volume.VolumeId, "#") {
					t.Errorf("List() volume ID %q should contain separator '#'", entry.Volume.VolumeId)
				}

				// Verify topology contains the node name
				if len(entry.Volume.AccessibleTopology) == 0 {
					t.Errorf("List() volume %q has no accessible topology", entry.Volume.VolumeId)
				} else if entry.Volume.AccessibleTopology[0].Segments[lvm.TopologyKey] != "test-node" {
					t.Errorf("List() volume %q has wrong topology node, expected 'test-node', got %q",
						entry.Volume.VolumeId,
						entry.Volume.AccessibleTopology[0].Segments[lvm.TopologyKey])
				}
			}
		})
	}
}

// Helper function to check if a string contains a substring.
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
