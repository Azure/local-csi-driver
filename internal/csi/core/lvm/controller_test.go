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
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"local-csi-driver/internal/csi/core/lvm"
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
				CapacityBytes: 1919999279104, // 1831054Mi
				VolumeContext: map[string]string{
					"localdisk.csi.acstor.io/capacity": "1919999279104",
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
	}
	for _, tt := range tests {
		var tt = tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := fake.NewClientBuilder().
				WithScheme(runtime.NewScheme()).
				Build()
			tp := telemetry.NewNoopTracerProvider()
			p := probe.NewFake([]string{"device1", "device2"}, nil)
			lvmMgr := lvmMgr.NewFake()

			l, err := lvm.New(c, "podname", "nodename", "default", p, lvmMgr, tp)
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
				p.EXPECT().ScanDevices(gomock.Any(), gomock.Any()).Return(nil, probe.ErrNoDevicesFound)
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
				p.EXPECT().ScanDevices(gomock.Any(), gomock.Any()).Return(nil, probe.ErrNoDevicesMatchingFilter)
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
