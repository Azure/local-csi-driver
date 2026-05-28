// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm_test

import (
	"context"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	lvmMgr "local-csi-driver/internal/pkg/lvm"
)

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
		expectErrCode  codes.Code
		expectErr      bool
		expectResp     bool
		expectCapacity int64
	}{
		{
			name: "missing volume id",
			req: &csi.NodeExpandVolumeRequest{
				CapacityRange: &csi.CapacityRange{RequiredBytes: expandedSize},
			},
			expectErr:     true,
			expectErrCode: codes.InvalidArgument,
		},
		{
			name: "invalid volume id format",
			req: &csi.NodeExpandVolumeRequest{
				VolumeId:      "not-a-valid-id",
				CapacityRange: &csi.CapacityRange{RequiredBytes: expandedSize},
			},
			expectErr:     true,
			expectErrCode: codes.InvalidArgument,
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
			expectErr:     true,
			expectErrCode: codes.InvalidArgument,
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
			expectErr:     true,
			expectErrCode: codes.NotFound,
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
			expectErr:     true,
			expectErrCode: codes.Internal,
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
			expectErr:     true,
			expectErrCode: codes.OutOfRange,
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
					t.Fatalf("NodeExpandVolume() error = nil, want gRPC code %v", tt.expectErrCode)
				}
				if st, ok := status.FromError(err); !ok || st.Code() != tt.expectErrCode {
					t.Fatalf("NodeExpandVolume() error code = %v, want %v (err=%v)", st.Code(), tt.expectErrCode, err)
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
