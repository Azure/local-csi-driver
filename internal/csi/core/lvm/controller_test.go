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
