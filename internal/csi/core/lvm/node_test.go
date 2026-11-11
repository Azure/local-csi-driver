// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"go.uber.org/mock/gomock"

	"local-csi-driver/internal/csi/core"
	lvmMgr "local-csi-driver/internal/pkg/lvm"
)

// TestRegressionExpandFilesystemRetry requests 64MiB -> 128MiB. The LV grows,
// but filesystem growth fails; an identical retry now finds a 128MiB LV.
// Bug: the retry returns success without retrying the unfinished filesystem growth.
// Expected: finish growing the filesystem before reporting success.
func TestRegressionExpandFilesystemRetry(t *testing.T) {
	t.Parallel()

	volumeManager, _, manager, err := initTestLVM(gomock.NewController(t))
	if err != nil {
		t.Fatal(err)
	}
	resizeErr := errors.New("filesystem growth failed after LV growth")
	initialRead := manager.EXPECT().GetLogicalVolume(gomock.Any(), "containerstorage", "filesystem-retry").
		Return(&lvmMgr.LogicalVolume{Name: "filesystem-retry", Size: 64 << 20}, nil)
	failedExpansion := manager.EXPECT().ExtendLogicalVolume(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, opts lvmMgr.ExtendLVOptions) error {
			if !opts.ResizeFS {
				t.Error("initial expansion must request filesystem growth")
			}
			return resizeErr
		}).After(initialRead)
	manager.EXPECT().GetLogicalVolume(gomock.Any(), "containerstorage", "filesystem-retry").
		Return(&lvmMgr.LogicalVolume{Name: "filesystem-retry", Size: 128 << 20}, nil).
		AnyTimes().After(failedExpansion)
	failedRetry := manager.EXPECT().
		ResizeLogicalVolumeFilesystem(gomock.Any(), "containerstorage/filesystem-retry", "/staging/filesystem-retry").
		Return(resizeErr).After(failedExpansion)
	manager.EXPECT().
		ResizeLogicalVolumeFilesystem(gomock.Any(), "containerstorage/filesystem-retry", "/staging/filesystem-retry").
		Return(nil).After(failedRetry)
	request := &csi.NodeExpandVolumeRequest{
		VolumeId:      "containerstorage#filesystem-retry",
		VolumePath:    "/staging/filesystem-retry",
		CapacityRange: &csi.CapacityRange{RequiredBytes: 128 << 20},
		VolumeCapability: &csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Mount{
				Mount: &csi.VolumeCapability_MountVolume{FsType: "ext4"},
			},
			AccessMode: &csi.VolumeCapability_AccessMode{
				Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
			},
		},
	}
	if _, err := volumeManager.NodeExpandVolume(t.Context(), request); !errors.Is(err, resizeErr) {
		t.Fatalf("first expansion error = %v, want filesystem resize failure %v", err, resizeErr)
	}
	if _, err := volumeManager.NodeExpandVolume(t.Context(), request); !errors.Is(err, resizeErr) {
		t.Fatalf("retry error = %v, want filesystem resize failure %v", err, resizeErr)
	}
	if _, err := volumeManager.NodeExpandVolume(t.Context(), request); err != nil {
		t.Fatalf("retry with a healthy filesystem resizer failed: %v", err)
	}
}

// TestNodeExpandVolume covers the size-comparison branches of
// NodeExpandVolume. It exists primarily to catch the regression where the
// `requested > current` case was unreachable (`>=` vs `==` typo) and
// ExtendLogicalVolume was never invoked.
func TestNodeExpandVolume(t *testing.T) {
	t.Parallel()

	const (
		testVG        = "containerstorage"
		testLV        = "pv-test"
		testVolumeID  = testVG + "#" + testLV
		currentSize   = int64(20 * 1024 * 1024 * 1024)   // 20 GiB
		expandedSize  = int64(1700 * 1024 * 1024 * 1024) // 1700 GiB
		smallerSize   = int64(10 * 1024 * 1024 * 1024)   // 10 GiB
		expandLVName  = testVG + "/" + testLV
		expandLVSizeS = "1700Gi"
	)

	tests := []struct {
		name           string
		req            *csi.NodeExpandVolumeRequest
		expectLvm      func(*lvmMgr.MockManager)
		expectErrIs    error
		expectErr      bool
		expectResp     bool
		expectCapacity int64
	}{
		{
			name: "missing volume id",
			req: &csi.NodeExpandVolumeRequest{
				CapacityRange: &csi.CapacityRange{RequiredBytes: expandedSize},
			},
			expectErr:   true,
			expectErrIs: core.ErrInvalidArgument,
		},
		{
			name: "invalid volume id format",
			req: &csi.NodeExpandVolumeRequest{
				VolumeId:      "not-a-valid-id",
				CapacityRange: &csi.CapacityRange{RequiredBytes: expandedSize},
			},
			expectErr:   true,
			expectErrIs: core.ErrInvalidArgument,
		},
		{
			name: "missing capacity range",
			req: &csi.NodeExpandVolumeRequest{
				VolumeId: testVolumeID,
			},
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetLogicalVolume(gomock.Any(), testVG, testLV).
					Return(&lvmMgr.LogicalVolume{Name: testLV, Size: lvmMgr.Int64String(currentSize)}, nil).
					Times(1)
			},
			expectErr:   true,
			expectErrIs: core.ErrOutOfRange,
		},
		{
			name: "volume not found",
			req: &csi.NodeExpandVolumeRequest{
				VolumeId:      testVolumeID,
				CapacityRange: &csi.CapacityRange{RequiredBytes: expandedSize},
			},
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetLogicalVolume(gomock.Any(), testVG, testLV).
					Return(nil, lvmMgr.ErrNotFound).
					Times(1)
			},
			expectErr:   true,
			expectErrIs: core.ErrVolumeNotFound,
		},
		{
			name: "requested less than current returns OK with actual size",
			req: &csi.NodeExpandVolumeRequest{
				VolumeId:      testVolumeID,
				CapacityRange: &csi.CapacityRange{RequiredBytes: smallerSize},
			},
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetLogicalVolume(gomock.Any(), testVG, testLV).
					Return(&lvmMgr.LogicalVolume{Name: testLV, Size: lvmMgr.Int64String(currentSize)}, nil).
					Times(1)
				// ExtendLogicalVolume must NOT be called.
			},
			expectResp:     true,
			expectCapacity: currentSize,
		},
		{
			name: "requested equals current is idempotent no-op",
			req: &csi.NodeExpandVolumeRequest{
				VolumeId:      testVolumeID,
				CapacityRange: &csi.CapacityRange{RequiredBytes: currentSize},
			},
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetLogicalVolume(gomock.Any(), testVG, testLV).
					Return(&lvmMgr.LogicalVolume{Name: testLV, Size: lvmMgr.Int64String(currentSize)}, nil).
					Times(1)
				// ExtendLogicalVolume must NOT be called.
			},
			expectResp:     true,
			expectCapacity: currentSize,
		},
		{
			name: "smaller mount request still completes filesystem growth without shrinking LV",
			req: &csi.NodeExpandVolumeRequest{
				VolumeId:      testVolumeID,
				VolumePath:    "/staging/pv-test",
				CapacityRange: &csi.CapacityRange{RequiredBytes: smallerSize},
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
				},
			},
			expectLvm: func(m *lvmMgr.MockManager) {
				m.EXPECT().GetLogicalVolume(gomock.Any(), testVG, testLV).
					Return(&lvmMgr.LogicalVolume{Name: testLV, Size: lvmMgr.Int64String(currentSize)}, nil)
				m.EXPECT().ResizeLogicalVolumeFilesystem(gomock.Any(), expandLVName, "/staging/pv-test").Return(nil)
			},
			expectResp:     true,
			expectCapacity: currentSize,
		},
		{
			name: "requested greater than current invokes ExtendLogicalVolume exactly once",
			req: &csi.NodeExpandVolumeRequest{
				VolumeId:      testVolumeID,
				CapacityRange: &csi.CapacityRange{RequiredBytes: expandedSize},
			},
			expectLvm: func(m *lvmMgr.MockManager) {
				first := m.EXPECT().GetLogicalVolume(gomock.Any(), testVG, testLV).
					Return(&lvmMgr.LogicalVolume{Name: testLV, Size: lvmMgr.Int64String(currentSize)}, nil).
					Times(1)
				second := m.EXPECT().
					ExtendLogicalVolume(gomock.Any(), gomock.AssignableToTypeOf(lvmMgr.ExtendLVOptions{})).
					DoAndReturn(func(_ context.Context, opts lvmMgr.ExtendLVOptions) error {
						if opts.Name != expandLVName {
							t.Errorf("ExtendLogicalVolume Name = %q, want %q", opts.Name, expandLVName)
						}
						if opts.Size != expandLVSizeS {
							t.Errorf("ExtendLogicalVolume Size = %q, want %q", opts.Size, expandLVSizeS)
						}
						return nil
					}).
					Times(1).
					After(first)
				m.EXPECT().GetLogicalVolume(gomock.Any(), testVG, testLV).
					Return(&lvmMgr.LogicalVolume{Name: testLV, Size: lvmMgr.Int64String(expandedSize)}, nil).
					Times(1).
					After(second)
			},
			expectResp:     true,
			expectCapacity: expandedSize,
		},
		{
			name: "re-query failure after extend returns error",
			req: &csi.NodeExpandVolumeRequest{
				VolumeId:      testVolumeID,
				CapacityRange: &csi.CapacityRange{RequiredBytes: expandedSize},
			},
			expectLvm: func(m *lvmMgr.MockManager) {
				first := m.EXPECT().GetLogicalVolume(gomock.Any(), testVG, testLV).
					Return(&lvmMgr.LogicalVolume{Name: testLV, Size: lvmMgr.Int64String(currentSize)}, nil).
					Times(1)
				second := m.EXPECT().
					ExtendLogicalVolume(gomock.Any(), gomock.AssignableToTypeOf(lvmMgr.ExtendLVOptions{})).
					Return(nil).
					Times(1).
					After(first)
				m.EXPECT().GetLogicalVolume(gomock.Any(), testVG, testLV).
					Return(nil, lvmMgr.ErrNotFound).
					Times(1).
					After(second)
			},
			expectErr:   true,
			expectErrIs: lvmMgr.ErrNotFound,
		},
		{
			name: "requested greater with mount capability sets ResizeFS",
			req: &csi.NodeExpandVolumeRequest{
				VolumeId:      testVolumeID,
				CapacityRange: &csi.CapacityRange{RequiredBytes: expandedSize},
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
			expectLvm: func(m *lvmMgr.MockManager) {
				first := m.EXPECT().GetLogicalVolume(gomock.Any(), testVG, testLV).
					Return(&lvmMgr.LogicalVolume{Name: testLV, Size: lvmMgr.Int64String(currentSize)}, nil).
					Times(1)
				second := m.EXPECT().
					ExtendLogicalVolume(gomock.Any(), gomock.AssignableToTypeOf(lvmMgr.ExtendLVOptions{})).
					DoAndReturn(func(_ context.Context, opts lvmMgr.ExtendLVOptions) error {
						if !opts.ResizeFS {
							t.Errorf("ExtendLogicalVolume ResizeFS = false, want true for mount access type")
						}
						return nil
					}).
					Times(1).
					After(first)
				m.EXPECT().GetLogicalVolume(gomock.Any(), testVG, testLV).
					Return(&lvmMgr.LogicalVolume{Name: testLV, Size: lvmMgr.Int64String(expandedSize)}, nil).
					Times(1).
					After(second)
			},
			expectResp:     true,
			expectCapacity: expandedSize,
		},
		{
			name: "extend returns ErrResourceExhausted maps to OutOfRange",
			req: &csi.NodeExpandVolumeRequest{
				VolumeId:      testVolumeID,
				CapacityRange: &csi.CapacityRange{RequiredBytes: expandedSize},
			},
			expectLvm: func(m *lvmMgr.MockManager) {
				first := m.EXPECT().GetLogicalVolume(gomock.Any(), testVG, testLV).
					Return(&lvmMgr.LogicalVolume{Name: testLV, Size: lvmMgr.Int64String(currentSize)}, nil).
					Times(1)
				m.EXPECT().
					ExtendLogicalVolume(gomock.Any(), gomock.AssignableToTypeOf(lvmMgr.ExtendLVOptions{})).
					Return(lvmMgr.ErrResourceExhausted).
					Times(1).
					After(first)
			},
			expectErr:   true,
			expectErrIs: core.ErrOutOfRange,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			l, _, m, err := initTestLVM(ctrl)
			if err != nil {
				t.Fatalf("failed to initialize LVM: %v", err)
			}
			if tt.expectLvm != nil {
				tt.expectLvm(m)
			}

			resp, err := l.NodeExpandVolume(context.Background(), tt.req)
			if tt.expectErr {
				if err == nil {
					t.Fatalf("NodeExpandVolume() error = nil, want error wrapping %v", tt.expectErrIs)
				}
				if tt.expectErrIs != nil && !errors.Is(err, tt.expectErrIs) {
					t.Fatalf("NodeExpandVolume() error = %v, want it to wrap %v", err, tt.expectErrIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("NodeExpandVolume() unexpected error: %v", err)
			}
			if !tt.expectResp {
				return
			}
			if resp == nil {
				t.Fatalf("NodeExpandVolume() resp = nil, want non-nil")
			}
			if resp.GetCapacityBytes() != tt.expectCapacity {
				t.Errorf("NodeExpandVolume() CapacityBytes = %d, want %d", resp.GetCapacityBytes(), tt.expectCapacity)
			}
		})
	}
}
