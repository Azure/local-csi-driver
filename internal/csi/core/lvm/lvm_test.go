// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gotidy/ptr"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/mock/gomock"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"local-csi-driver/internal/csi/core"
	"local-csi-driver/internal/csi/core/lvm"
	"local-csi-driver/internal/pkg/block"
	"local-csi-driver/internal/pkg/convert"
	lvmMgr "local-csi-driver/internal/pkg/lvm"
	"local-csi-driver/internal/pkg/probe"
	"local-csi-driver/internal/pkg/telemetry"
)

const (
	testPodName      = "test-pod"
	testPodNamespace = "test-namespace"
	testNodeName     = "test-node"
)

var (
	errTestInternal = errors.New("internal test error")
)

func initTestLVM(ctrl *gomock.Controller) (*lvm.LVM, *probe.Mock, *lvmMgr.MockManager, error) {
	c := fake.NewClientBuilder().
		WithScheme(runtime.NewScheme()).
		Build()
	t := telemetry.NewNoopTracerProvider()
	p := probe.NewMock(ctrl)
	lvmMgr := lvmMgr.NewMockManager(ctrl)
	l, err := lvm.New(c, testPodName, testNodeName, testPodNamespace, p, lvmMgr, t)
	if err != nil {
		return nil, nil, nil, err
	}
	return l, p, lvmMgr, nil
}

func TestNewLVM(t *testing.T) {
	t.Parallel()
	// setup mocks
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	type args struct {
		client    client.Client
		podName   string
		nodeName  string
		namespace string
		probe     probe.Interface
		manager   lvmMgr.Manager
		tracer    trace.TracerProvider
	}

	valid := args{
		client:    fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build(),
		podName:   testPodName,
		nodeName:  testNodeName,
		namespace: testPodNamespace,
		probe:     probe.NewMock(ctrl),
		manager:   lvmMgr.NewMockManager(ctrl),
		tracer:    telemetry.NewNoopTracerProvider(),
	}

	testCases := []struct {
		name      string
		mutate    func(tc *args)
		expectErr bool
	}{
		{
			name:      "empty podName",
			mutate:    func(tc *args) { tc.podName = "" },
			expectErr: true,
		},
		{
			name:      "empty nodeName",
			mutate:    func(tc *args) { tc.nodeName = "" },
			expectErr: true,
		},
		{
			name:      "empty namespace",
			mutate:    func(tc *args) { tc.namespace = "" },
			expectErr: true,
		},
		{
			name:      "valid args",
			expectErr: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			test := valid
			if tc.mutate != nil {
				tc.mutate(&test)
			}
			got, err := lvm.New(test.client, test.podName, test.nodeName, test.namespace, test.probe, test.manager, test.tracer)
			if (err != nil) != tc.expectErr {
				t.Errorf("New(%q) error = %v, expectErr %v", tc.name, err, tc.expectErr)
			}
			if err == nil && got == nil {
				t.Errorf("New(%q) returned nil LVM, want non-nil", tc.name)
			}
		})
	}
}

func TestGetVolumeName(t *testing.T) {
	t.Parallel()
	type testCase struct {
		input       string
		want        string
		expectError bool
	}

	tests := []testCase{
		{
			input:       "vg#lv",
			want:        "lv",
			expectError: false,
		},
		{
			input:       "vg#",
			want:        "",
			expectError: true,
		},
	}

	for _, tc := range tests {
		// capture range variable
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			lvm, _, _, err := initTestLVM(gomock.NewController(t))
			if err != nil {
				t.Fatalf("failed to initialize LVM: %v", err)
			}
			got, err := lvm.GetVolumeName(tc.input)
			if (err != nil) != tc.expectError {
				t.Errorf("GetVolumeName(%q) error = %v, expectError %v", tc.input, err, tc.expectError)
			}
			if got != tc.want {
				t.Errorf("GetVolumeName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestGetNodeDevice(t *testing.T) {
	t.Parallel()
	type testCase struct {
		input       string
		want        string
		expectError bool
	}

	tests := []testCase{
		{
			input:       "vg#lv",
			want:        "/dev/vg/lv",
			expectError: false,
		},
		{
			input:       "vg#",
			want:        "",
			expectError: true,
		},
	}

	for _, tc := range tests {
		// capture range variable
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			lvm, _, _, err := initTestLVM(gomock.NewController(t))
			if err != nil {
				t.Fatalf("failed to initialize LVM: %v", err)
			}
			got, err := lvm.GetNodeDevicePath(tc.input)
			if (err != nil) != tc.expectError {
				t.Errorf("GetNodeDevicePath(%q) error = %v, expectError %v", tc.input, err, tc.expectError)
			}
			if got != tc.want {
				t.Errorf("GetNodeDevicePath(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestEnsurePhysicalVolumes(t *testing.T) {
	t.Parallel()
	devices := &block.DeviceList{Devices: []block.Device{{Path: "/dev/pv1"}, {Path: "/dev/pv2"}}}
	tests := []struct {
		name          string
		expectProbe   func(*probe.Mock)
		expectLvm     func(*lvmMgr.MockManager)
		expectedPaths []string
		expectedErr   error
	}{
		{
			name: "no physical volumes",
			expectProbe: func(p *probe.Mock) {
				p.EXPECT().ScanAvailableDevices(gomock.Any(), gomock.Any()).Return(nil, probe.ErrNoDevicesFound)
			},
			expectedPaths: nil,
			expectedErr:   core.ErrResourceExhausted,
		},
		{
			name: "no matching physical volumes",
			expectProbe: func(p *probe.Mock) {
				p.EXPECT().ScanAvailableDevices(gomock.Any(), gomock.Any()).Return(nil, probe.ErrNoDevicesFound)
			},
			expectedPaths: nil,
			expectedErr:   core.ErrResourceExhausted,
		},
		{
			name: "other error from probe",
			expectProbe: func(p *probe.Mock) {
				p.EXPECT().ScanAvailableDevices(gomock.Any(), gomock.Any()).Return(nil, errTestInternal)
			},
			expectedPaths: nil,
			expectedErr:   errTestInternal,
		},
		{
			name: "list physical volumes error",
			expectProbe: func(p *probe.Mock) {
				p.EXPECT().ScanAvailableDevices(gomock.Any(), gomock.Any()).Return(devices, nil)
			},
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().ListPhysicalVolumes(gomock.Any(), gomock.Any()).Return(nil, errTestInternal)
			},
			expectedPaths: nil,
			expectedErr:   errTestInternal,
		},
		{
			name: "create physical volumes error",
			expectProbe: func(p *probe.Mock) {
				p.EXPECT().ScanAvailableDevices(gomock.Any(), gomock.Any()).Return(devices, nil)
			},
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().ListPhysicalVolumes(gomock.Any(), gomock.Any()).Return(nil, nil)
				m.EXPECT().CreatePhysicalVolume(gomock.Any(), gomock.Any()).Return(errTestInternal)
			},
			expectedPaths: nil,
			expectedErr:   errTestInternal,
		},
		{
			name: "create physical volumes error in use",
			expectProbe: func(p *probe.Mock) {
				p.EXPECT().ScanAvailableDevices(gomock.Any(), gomock.Any()).Return(devices, nil)
			},
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().ListPhysicalVolumes(gomock.Any(), gomock.Any()).Return(nil, nil)
				m.EXPECT().CreatePhysicalVolume(gomock.Any(), gomock.Any()).Return(lvmMgr.ErrInUse).Times(2)
			},
			expectedPaths: nil,
			expectedErr:   core.ErrResourceExhausted,
		},
		{
			name: "create physical volumes partial error in use",
			expectProbe: func(p *probe.Mock) {
				p.EXPECT().ScanAvailableDevices(gomock.Any(), gomock.Any()).Return(devices, nil)
			},
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().ListPhysicalVolumes(gomock.Any(), gomock.Any()).Return(nil, nil)
				m.EXPECT().CreatePhysicalVolume(gomock.Any(), lvmMgr.CreatePVOptions{Name: "/dev/pv1"}).Return(lvmMgr.ErrInUse)
				m.EXPECT().CreatePhysicalVolume(gomock.Any(), lvmMgr.CreatePVOptions{Name: "/dev/pv2"}).Return(nil)
			},
			expectedPaths: []string{"/dev/pv2"},
			expectedErr:   nil,
		},
		{
			name: "create physical volumes partial internal error",
			expectProbe: func(p *probe.Mock) {
				p.EXPECT().ScanAvailableDevices(gomock.Any(), gomock.Any()).Return(devices, nil)
			},
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().ListPhysicalVolumes(gomock.Any(), gomock.Any()).Return(nil, nil)
				m.EXPECT().CreatePhysicalVolume(gomock.Any(), lvmMgr.CreatePVOptions{Name: "/dev/pv1"}).Return(errTestInternal)
			},
			expectedPaths: nil,
			expectedErr:   errTestInternal,
		},
		{
			name: "skip existing physical volumes",
			expectProbe: func(p *probe.Mock) {
				p.EXPECT().ScanAvailableDevices(gomock.Any(), gomock.Any()).Return(devices, nil)
			},
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().ListPhysicalVolumes(gomock.Any(), gomock.Any()).Return(createPvs("/dev/pv1"), nil)
				m.EXPECT().CreatePhysicalVolume(gomock.Any(), gomock.Any()).Return(nil).Times(1)
			},
			expectedPaths: []string{"/dev/pv1", "/dev/pv2"},
			expectedErr:   nil,
		},
		{
			name: "normal success case",
			expectProbe: func(p *probe.Mock) {
				p.EXPECT().ScanAvailableDevices(gomock.Any(), gomock.Any()).Return(devices, nil)
			},
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().ListPhysicalVolumes(gomock.Any(), gomock.Any()).Return(nil, nil)
				m.EXPECT().CreatePhysicalVolume(gomock.Any(), gomock.Any()).Return(nil).Times(2)
			},
			expectedPaths: []string{"/dev/pv1", "/dev/pv2"},
			expectedErr:   nil,
		},
	}

	for _, tc := range tests {

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, p, m, err := initTestLVM(gomock.NewController(t))
			if err != nil {
				t.Fatalf("failed to initialize LVM: %v", err)
			}
			if tc.expectProbe != nil {
				tc.expectProbe(p)
			}
			if tc.expectLvm != nil {
				tc.expectLvm(m)
			}
			paths, err := l.EnsurePhysicalVolumes(context.Background())
			if !errors.Is(err, tc.expectedErr) {
				t.Errorf("EnsurePhysicalVolumes() error = %v, expectErr %v", err, tc.expectedErr)
			}
			if len(paths) != len(tc.expectedPaths) {
				t.Errorf("EnsurePhysicalVolumes() = %v, want %v", paths, tc.expectedPaths)
			}
			for i, path := range paths {
				if len(tc.expectedPaths) <= i {
					t.Errorf("EnsurePhysicalVolumes() returned more paths than expected: got %v, want %v", paths, tc.expectedPaths)
					continue
				} else if path != tc.expectedPaths[i] {
					t.Errorf("EnsurePhysicalVolumes() = %v, want %v", path, tc.expectedPaths[i])
				}
			}
		})
	}
}

func TestEnsureVolumeGroup(t *testing.T) {
	t.Parallel()
	testVg := &lvmMgr.VolumeGroup{
		Name: "vg",
	}
	tests := []struct {
		name        string
		vgName      string
		devices     []string
		expectLvm   func(*lvmMgr.MockManager)
		expectedErr error
		expectedVG  *lvmMgr.VolumeGroup
	}{
		{
			name:        "empty devices",
			vgName:      "vg",
			devices:     nil,
			expectLvm:   nil,
			expectedErr: core.ErrResourceExhausted,
			expectedVG:  nil,
		},
		{
			name:        "empty vg name",
			vgName:      "",
			devices:     []string{"/dev/pv1", "/dev/pv2"},
			expectLvm:   nil,
			expectedErr: core.ErrInvalidArgument,
			expectedVG:  nil,
		},
		{
			name:    "get vg error",
			vgName:  "vg",
			devices: []string{"/dev/pv1", "/dev/pv2"},
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetVolumeGroup(gomock.Any(), gomock.Any()).Return(nil, errTestInternal)
			},
			expectedErr: errTestInternal,
			expectedVG:  nil,
		},
		{
			name:    "vg already exists",
			vgName:  "vg",
			devices: []string{"/dev/pv1", "/dev/pv2"},
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetVolumeGroup(gomock.Any(), gomock.Any()).Return(testVg, nil)
			},
			expectedErr: nil,
			expectedVG:  testVg,
		},
		{
			name:    "create vg error",
			vgName:  "vg",
			devices: []string{"/dev/pv1", "/dev/pv2"},
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetVolumeGroup(gomock.Any(), gomock.Any()).Return(nil, nil)
				m.EXPECT().CreateVolumeGroup(gomock.Any(), gomock.Any()).Return(errTestInternal)
			},
			expectedErr: errTestInternal,
			expectedVG:  nil,
		},
		{
			name:    "create vg concurrent error, already exists",
			vgName:  "vg",
			devices: []string{"/dev/pv1", "/dev/pv2"},
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetVolumeGroup(gomock.Any(), gomock.Any()).Return(nil, nil)
				m.EXPECT().CreateVolumeGroup(gomock.Any(), gomock.Any()).Return(lvmMgr.ErrAlreadyExists)
				m.EXPECT().GetVolumeGroup(gomock.Any(), gomock.Any()).Return(testVg, nil)
			},
			expectedErr: nil,
			expectedVG:  testVg,
		},
		{
			name:    "create vg success, get fails",
			vgName:  "vg",
			devices: []string{"/dev/pv1", "/dev/pv2"},
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetVolumeGroup(gomock.Any(), gomock.Any()).Return(nil, nil)
				m.EXPECT().CreateVolumeGroup(gomock.Any(), gomock.Any()).Return(nil)
				m.EXPECT().GetVolumeGroup(gomock.Any(), gomock.Any()).Return(nil, errTestInternal)
			},
			expectedErr: errTestInternal,
			expectedVG:  nil,
		},
		{
			name:    "create vg success normal",
			vgName:  "vg",
			devices: []string{"/dev/pv1", "/dev/pv2"},
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetVolumeGroup(gomock.Any(), gomock.Any()).Return(nil, nil)
				m.EXPECT().CreateVolumeGroup(gomock.Any(), gomock.Any()).Return(nil)
				m.EXPECT().GetVolumeGroup(gomock.Any(), gomock.Any()).Return(testVg, nil)
			},
			expectedErr: nil,
			expectedVG:  testVg,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			l, _, m, err := initTestLVM(gomock.NewController(t))
			if err != nil {
				t.Fatalf("failed to initialize LVM: %v", err)
			}
			if tt.expectLvm != nil {
				tt.expectLvm(m)
			}
			vg, err := l.EnsureVolumeGroup(context.Background(), tt.vgName, tt.devices)
			if !errors.Is(err, tt.expectedErr) {
				t.Errorf("EnsureVolumeGroup() error = %v, expectErr %v", err, tt.expectedErr)
			}
			if vg != tt.expectedVG {
				t.Errorf("EnsureVolumeGroup() = %v, want %v", vg, tt.expectedVG)
			}
		})
	}
}

func TestEnsureVolume(t *testing.T) {
	t.Parallel()
	testVg := &lvmMgr.VolumeGroup{Name: "vg"}
	// Use bytes for size: 1024MiB == 1073741824 bytes
	testLv1GiB := &lvmMgr.LogicalVolume{Name: "lv", Size: lvmMgr.Int64String(convert.MiBToBytes(1024))}
	testLv2GiB := &lvmMgr.LogicalVolume{Name: "lv", Size: lvmMgr.Int64String(convert.MiBToBytes(2048))}
	devices := &block.DeviceList{Devices: []block.Device{{Path: "/dev/pv1"}, {Path: "/dev/pv2"}}}

	tests := []struct {
		name        string
		volumeId    string
		request     int64
		limit       int64
		expectLvm   func(*lvmMgr.MockManager)
		expectProbe func(*probe.Mock)
		expectedErr error
	}{
		{
			name:        "empty vg name",
			volumeId:    "lv",
			request:     convert.MiBToBytes(1024),
			expectLvm:   nil,
			expectedErr: core.ErrInvalidArgument,
		},
		{
			name:        "zero request size",
			volumeId:    "vg#lv",
			request:     0,
			expectLvm:   nil,
			expectedErr: core.ErrInvalidArgument,
		},
		{
			name:        "invalid request size",
			volumeId:    "vg#lv",
			request:     -1,
			expectLvm:   nil,
			expectedErr: core.ErrInvalidArgument,
		},
		{
			name:        "invalid limit size",
			volumeId:    "vg#lv",
			request:     convert.MiBToBytes(1024),
			limit:       -1,
			expectLvm:   nil,
			expectedErr: core.ErrInvalidArgument,
		},
		{
			name:        "limit less than request size",
			volumeId:    "vg#lv",
			request:     convert.MiBToBytes(2048),
			limit:       convert.MiBToBytes(1024),
			expectLvm:   nil,
			expectedErr: core.ErrInvalidArgument,
		},
		{
			name:     "get lv error",
			volumeId: "vg#lv",
			request:  convert.MiBToBytes(1024),
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetLogicalVolume(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, errTestInternal)
			},
			expectedErr: errTestInternal,
		},
		{
			name:     "lv already exists",
			volumeId: "vg#lv",
			request:  convert.MiBToBytes(1024),
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetLogicalVolume(gomock.Any(), gomock.Any(), gomock.Any()).Return(testLv1GiB, nil)
			},
			expectedErr: nil,
		},
		{
			name:     "create lv got larger volume",
			volumeId: "vg#lv",
			request:  convert.MiBToBytes(2024),
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetLogicalVolume(gomock.Any(), gomock.Any(), gomock.Any()).Return(testLv2GiB, nil)
			},
			expectedErr: nil,
		},
		{
			name:     "create lv got smaller volume",
			volumeId: "vg#lv",
			request:  convert.MiBToBytes(2024),
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetLogicalVolume(gomock.Any(), gomock.Any(), gomock.Any()).Return(testLv1GiB, nil)
			},
			expectedErr: core.ErrVolumeSizeMismatch,
		},
		{
			name:     "vg already exists, create lv",
			volumeId: "vg#lv",
			request:  convert.MiBToBytes(1024),
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetLogicalVolume(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil)
				m.EXPECT().GetVolumeGroup(gomock.Any(), gomock.Any()).Return(testVg, nil)
				m.EXPECT().CreateLogicalVolume(gomock.Any(), gomock.Any()).Return(convert.MiBToBytes(1024), nil)
			},
			expectedErr: nil,
		},
		{
			name:     "vg already exists, create lv striped",
			volumeId: "vg#lv",
			request:  convert.MiBToBytes(1024),
			expectLvm: func(m *lvmMgr.MockManager) {
				stripedVg := &lvmMgr.VolumeGroup{
					Name:    "vg",
					PVCount: 4,
				}
				createArgs := lvmMgr.CreateLVOptions{
					Name:    "lv",
					VGName:  "vg",
					Size:    "1073741824B",
					Type:    "raid0",
					Stripes: ptr.Of(4),
				}
				m.EXPECT().GetLogicalVolume(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil)
				m.EXPECT().GetVolumeGroup(gomock.Any(), gomock.Any()).Return(stripedVg, nil)
				m.EXPECT().CreateLogicalVolume(gomock.Any(), createArgs).Return(convert.MiBToBytes(1024), nil)
			},
			expectedErr: nil,
		},
		{
			name:     "get vg error",
			volumeId: "vg#lv",
			request:  convert.MiBToBytes(1024),
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetLogicalVolume(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil)
				m.EXPECT().GetVolumeGroup(gomock.Any(), gomock.Any()).Return(nil, errTestInternal)
			},
			expectedErr: errTestInternal,
		},
		{
			name:     "ensure physical volumes error",
			volumeId: "vg#lv",
			request:  convert.MiBToBytes(1024),
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetLogicalVolume(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil)
				m.EXPECT().GetVolumeGroup(gomock.Any(), gomock.Any()).Return(nil, lvmMgr.ErrNotFound)
			},
			expectProbe: func(p *probe.Mock) {
				p.EXPECT().ScanAvailableDevices(gomock.Any(), gomock.Any()).Return(nil, errTestInternal)
			},
			expectedErr: errTestInternal,
		},
		{
			name:     "ensure vg error",
			volumeId: "vg#lv",
			request:  convert.MiBToBytes(1024),
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetLogicalVolume(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil)
				m.EXPECT().GetVolumeGroup(gomock.Any(), gomock.Any()).Return(nil, lvmMgr.ErrNotFound)
				m.EXPECT().ListPhysicalVolumes(gomock.Any(), gomock.Any()).Return(createPvs("/dev/pv1", "/dev/pv2"), nil)
				m.EXPECT().GetVolumeGroup(gomock.Any(), gomock.Any()).Return(nil, lvmMgr.ErrNotFound)
				m.EXPECT().CreateVolumeGroup(gomock.Any(), gomock.Any()).Return(errTestInternal)

			},
			expectProbe: func(p *probe.Mock) {
				p.EXPECT().ScanAvailableDevices(gomock.Any(), gomock.Any()).Return(devices, nil)
			},
			expectedErr: errTestInternal,
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
			err = l.EnsureVolume(context.Background(), tt.volumeId, ptr.Of(tt.request), tt.limit, true)
			if !errors.Is(err, tt.expectedErr) {
				t.Errorf("EnsureVolume() error = %v, expectErr %v", err, tt.expectedErr)
			}
		})
	}

}

func createPvs(paths ...string) []lvmMgr.PhysicalVolume {
	pvs := make([]lvmMgr.PhysicalVolume, len(paths))
	for i, path := range paths {
		pvs[i] = lvmMgr.PhysicalVolume{Name: path}
	}
	return pvs
}
