// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package node

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	testingexec "k8s.io/utils/exec/testing"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"local-csi-driver/internal/csi/capability"
	"local-csi-driver/internal/csi/core"
	"local-csi-driver/internal/csi/core/lvm"
	"local-csi-driver/internal/csi/mounter"
	"local-csi-driver/internal/csi/testutil"
	"local-csi-driver/internal/pkg/convert"
	"local-csi-driver/internal/pkg/events"
	"local-csi-driver/internal/pkg/tracing"
)

const (
	testNodeName             = "node-name"
	driverName               = "testlocal.csi.azure.com"
	testInvalidId            = "invalidId"
	testVolumeID             = "vg#pv"
	recoveryOKVolumeID       = "vg#testrecoveryok"
	recoveryBrokenVolumeID   = "vg#testrecoverybroken"
	invalidStagingPath       = "invalidStagingPath"
	validStagingPath         = "/vg/pv"
	validTargetPath          = "validTargetPath"
	isMountPoint             = validTargetPath
	isNotMountPoint          = validTargetPath + "notmount"
	invalidTargetPath        = "invalidTargetPath"
	permissionError          = "permissionError"
	testTopologyKey          = "topology." + driverName + "/node"
	selectedNodeAnnotation   = "testlocal.csi.azure.com/selected-node"
	selectedInitialNodeParam = "testlocal.csi.azure.com/selected-initial-node"
)

func initTestNodeServer(_ *testing.T, ctrl *gomock.Controller) *Server {
	vc := core.NewFake()
	m := mounter.NewMockMounter(ctrl)
	r := events.NewNoopRecorder()
	tp := tracing.NewNoopTracerProvider()
	scheme := k8sruntime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	pvs := corev1.PersistentVolumeList{
		Items: []corev1.PersistentVolume{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pv-valid-0",
					Annotations: map[string]string{
						selectedNodeAnnotation: "test-node",
					},
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{},
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteOnce,
					},
					PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
					StorageClassName:              "standard",
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pv-valid-1",
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{},
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteOnce,
					},
					PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
					StorageClassName:              "standard",
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver:       "test-driver",
							VolumeHandle: "vg#pv-1",
							VolumeAttributes: map[string]string{
								selectedInitialNodeParam: "test-node",
							},
						},
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pv-wrong-node-0",
					Annotations: map[string]string{
						selectedNodeAnnotation: "test-wrong-node",
					},
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{},
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteOnce,
					},
					PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
					StorageClassName:              "standard",
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testvolumeidxxx",
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{},
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteOnce,
					},
					PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
					StorageClassName:              "standard",
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testrecoveryok",
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{},
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteOnce,
					},
					PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
					StorageClassName:              "standard",
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver:       driverName,
							VolumeHandle: recoveryOKVolumeID,
							VolumeAttributes: map[string]string{
								selectedInitialNodeParam:       "test-node",
								"local.csi.azure.com/capacity": "1Gi",
							},
						},
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testrecoverybroken",
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{},
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteOnce,
					},
					PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
					StorageClassName:              "standard",
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver:       driverName,
							VolumeHandle: recoveryOKVolumeID,
							VolumeAttributes: map[string]string{
								selectedInitialNodeParam: "test-node",
								// capacity not set.
							},
						},
					},
				},
			},
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithLists(&pvs).
		Build()

	caps := []*csi.NodeServiceCapability{
		capability.NewNodeServiceCapability(csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME),
	}

	return New(vc, testNodeName, selectedNodeAnnotation, selectedInitialNodeParam, driverName, caps, m, client, true, r, tp)
}

func TestMain(m *testing.M) {
	_ = m.Run()
}

func TestNodePublishVolume(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skipf("not supported on GOOS=%s", runtime.GOOS)
	}

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	volumeCap := csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER}
	stdVolCap := &csi.VolumeCapability_Mount{
		Mount: &csi.VolumeCapability_MountVolume{},
	}
	stdVolCapBlock := &csi.VolumeCapability_Block{
		Block: &csi.VolumeCapability_BlockVolume{},
	}

	tests := []struct {
		name              string
		req               *csi.NodePublishVolumeRequest
		resp              *csi.NodePublishVolumeResponse
		wantMountPointErr bool
		expectErr         error
		expectMount       func(mntDir string, m *mounter.MockMounter)
	}{
		{
			name: "Volume capability missing in request",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: validStagingPath,
				TargetPath:        isMountPoint,
			},
			resp:      nil,
			expectErr: status.Error(codes.InvalidArgument, "Volume capability missing in request"),
		},
		{
			name: "Volume ID missing in request",
			req: &csi.NodePublishVolumeRequest{
				VolumeCapability:  &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: stdVolCapBlock},
				StagingTargetPath: validStagingPath,
				TargetPath:        isMountPoint,
			},
			resp:      nil,
			expectErr: status.Error(codes.InvalidArgument, "Volume ID missing in request"),
		},
		{
			name: "Target path not provided",
			req: &csi.NodePublishVolumeRequest{
				VolumeCapability:  &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: stdVolCapBlock},
				VolumeId:          testVolumeID,
				StagingTargetPath: validStagingPath,
			},
			resp:      nil,
			expectErr: status.Error(codes.InvalidArgument, "Target path not provided"),
		},
		{
			name: "Target Path is a Mount Point",
			req: &csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{
					AccessMode: &volumeCap,
					AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
				},
				VolumeId:          testVolumeID,
				StagingTargetPath: validStagingPath,
				TargetPath:        isMountPoint,
				VolumeContext:     map[string]string{},
			},
			resp:      &csi.NodePublishVolumeResponse{},
			expectErr: nil,
			expectMount: func(mntDir string, m *mounter.MockMounter) {
				m.EXPECT().IsLikelyNotMountPoint(gomock.Eq(filepath.Join(mntDir, isMountPoint))).Return(false, nil).Times(1)
			},
		},
		{
			name: "Method IsLikelyNotMountPoint Error",
			req: &csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{
					AccessMode: &volumeCap,
					AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
				}, VolumeId: testVolumeID,
				StagingTargetPath: validStagingPath,
				TargetPath:        invalidTargetPath,
				VolumeContext:     map[string]string{},
			},
			wantMountPointErr: true,
			resp:              nil,
			expectErr:         status.Error(codes.Internal, fmt.Errorf("could not mount target: Method IsLikelyNotMountPoint Failed").Error()),
			expectMount: func(mntDir string, m *mounter.MockMounter) {
				m.EXPECT().IsLikelyNotMountPoint(gomock.Eq(filepath.Join(mntDir, invalidTargetPath))).Return(true, fmt.Errorf("Method IsLikelyNotMountPoint Failed")).Times(1)
			},
		},
		{
			name: "Failed Mount (Permission)",
			req: &csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{
					AccessMode: &volumeCap,
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{
							MountFlags: []string{permissionError},
						},
					},
				},
				VolumeId:          testVolumeID,
				StagingTargetPath: validStagingPath,
				TargetPath:        isNotMountPoint,
				VolumeContext:     map[string]string{},
			},
			resp:      nil,
			expectErr: status.Error(codes.PermissionDenied, fmt.Errorf("could not mount: %v", os.ErrPermission).Error()),
			expectMount: func(mntDir string, m *mounter.MockMounter) {
				mountOptions := []string{"bind"}
				m.EXPECT().IsLikelyNotMountPoint(gomock.Eq(filepath.Join(mntDir, isNotMountPoint))).Return(true, nil).Times(1)
				m.EXPECT().Mount(filepath.Join(mntDir, validStagingPath), filepath.Join(mntDir, isNotMountPoint), "", gomock.InAnyOrder(mountOptions)).Return(os.ErrPermission).Times(1)
			},
		},
		{
			name: "Failed Mount (Internal)",
			req: &csi.NodePublishVolumeRequest{
				VolumeCapability:  &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: stdVolCap},
				VolumeId:          testVolumeID,
				StagingTargetPath: validStagingPath,
				TargetPath:        isNotMountPoint,
				VolumeContext:     map[string]string{},
			},
			resp:      nil,
			expectErr: status.Error(codes.Internal, fmt.Errorf("could not mount: Failed Mount").Error()),
			expectMount: func(mntDir string, m *mounter.MockMounter) {
				mountOptions := []string{"bind"}
				m.EXPECT().IsLikelyNotMountPoint(gomock.Eq(filepath.Join(mntDir, isNotMountPoint))).Return(true, nil).Times(1)
				m.EXPECT().Mount(filepath.Join(mntDir, validStagingPath), filepath.Join(mntDir, isNotMountPoint), "", gomock.InAnyOrder(mountOptions)).Return(fmt.Errorf("Failed Mount")).Times(1)
			},
		},
		{
			name: "Success (volumeMode is Block)",
			req: &csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{
					AccessMode: &volumeCap,
					AccessType: stdVolCapBlock,
				},
				VolumeId:          testVolumeID,
				StagingTargetPath: validStagingPath,
				TargetPath:        isNotMountPoint,
				VolumeContext:     map[string]string{},
			},
			resp:      &csi.NodePublishVolumeResponse{},
			expectErr: nil,
			expectMount: func(mntDir string, m *mounter.MockMounter) {
				m.EXPECT().IsLikelyNotMountPoint(gomock.Eq(mntDir)).Return(true, nil).Times(1)
				m.EXPECT().Mount(filepath.Join(mntDir, validStagingPath), gomock.Eq(filepath.Join(mntDir, isNotMountPoint)), "", []string{"bind"}).Return(nil).Times(1)
				m.EXPECT().FileExists(gomock.Eq(filepath.Join(mntDir, isNotMountPoint))).Return(true, nil).Times(1)
			},
		},
		{
			name: "Success",
			req: &csi.NodePublishVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{
					AccessMode: &volumeCap,
					AccessType: stdVolCap,
				},
				VolumeId:          testVolumeID,
				StagingTargetPath: validStagingPath,
				TargetPath:        isNotMountPoint,
				VolumeContext:     map[string]string{},
			},
			resp:      &csi.NodePublishVolumeResponse{},
			expectErr: nil,
			expectMount: func(mntDir string, m *mounter.MockMounter) {
				m.EXPECT().IsLikelyNotMountPoint(gomock.Eq(filepath.Join(mntDir, isNotMountPoint))).Return(false, nil).Times(1)
			},
		},
	}
	for _, test := range tests {
		var tt = test
		t.Run(tt.name, func(t *testing.T) {
			mntDir, err := os.MkdirTemp(os.TempDir(), "mount")
			if err != nil {
				t.Fatalf("failed to create tmp dir: %v", err)
			}
			defer func() {
				if err := os.RemoveAll(mntDir); err != nil {
					t.Errorf("failed to remove tmp dir: %v", err)
				}
			}()

			ns := initTestNodeServer(t, ctrl)
			ns.volume.(*core.Fake).BaseDir = mntDir
			if tt.expectMount != nil {
				tt.expectMount(mntDir, ns.mounter.(*mounter.MockMounter))
			}

			ns.volume.(*core.Fake).BaseDir = mntDir

			// Prefix staging and target path with tmp path for easy cleanup.
			req := tt.req
			if req.StagingTargetPath != "" {
				req.StagingTargetPath = filepath.Join(mntDir, req.StagingTargetPath)
			}
			if req.TargetPath != "" {
				req.TargetPath = filepath.Join(mntDir, req.TargetPath)
			}

			resp, err := ns.NodePublishVolume(context.Background(), req)
			if status.Code(err) != status.Code(tt.expectErr) ||
				(err != nil && err.Error() != tt.expectErr.Error()) {
				t.Errorf("NodePublishVolume() error = %v, expected = %v", err, tt.expectErr)
				return
			}
			if !reflect.DeepEqual(resp, tt.resp) {
				t.Errorf("NodePublishVolume() resp:\n %+v\n expected:\n %+v", resp, tt.resp)
				return
			}
		})
	}
}

func TestNodeUnpublishVolume(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tests := []struct {
		name          string
		req           *csi.NodeUnpublishVolumeRequest
		resp          *csi.NodeUnpublishVolumeResponse
		expectErr     error
		expectUnmount func(mntDir string, a *mounter.MockMounter)
	}{
		{
			name: "Volume ID missing in request",
			req: &csi.NodeUnpublishVolumeRequest{
				TargetPath: isMountPoint,
			},
			resp:      nil,
			expectErr: status.Error(codes.InvalidArgument, "Volume ID missing in request"),
		},
		{
			name: "Target path not provided",
			req: &csi.NodeUnpublishVolumeRequest{
				VolumeId: testVolumeID,
			},
			resp:      nil,
			expectErr: status.Error(codes.InvalidArgument, "Target path not provided"),
		},
		{
			name: "OS RemoveAll Error (target path not provided)",
			req: &csi.NodeUnpublishVolumeRequest{
				VolumeId:   testVolumeID,
				TargetPath: "",
			},
			resp:      nil,
			expectErr: status.Error(codes.InvalidArgument, fmt.Errorf("Target path not provided").Error()),
		},
		{
			name: "Success",
			req: &csi.NodeUnpublishVolumeRequest{
				VolumeId:   testVolumeID,
				TargetPath: isMountPoint,
			},
			resp:      &csi.NodeUnpublishVolumeResponse{},
			expectErr: nil,
			expectUnmount: func(mntDir string, m *mounter.MockMounter) {
				m.EXPECT().CleanupMountPoint(gomock.Eq(filepath.Join(mntDir, isMountPoint))).Return(nil).Times(1)
			},
		},
	}
	for _, test := range tests {
		var tt = test
		t.Run(tt.name, func(t *testing.T) {
			mntDir, err := os.MkdirTemp(os.TempDir(), "mount")
			if err != nil {
				t.Fatalf("failed to create tmp dir: %v", err)
			}
			defer func() {
				if err := os.RemoveAll(mntDir); err != nil {
					t.Errorf("failed to remove tmp dir: %v", err)
				}
			}()

			ns := initTestNodeServer(t, ctrl)
			if tt.expectUnmount != nil {
				tt.expectUnmount(mntDir, ns.mounter.(*mounter.MockMounter))
			}

			// Prefix target path with tmp path for easy cleanup.
			req := tt.req
			if req.TargetPath != "" {
				req.TargetPath = filepath.Join(mntDir, req.TargetPath)
			}

			resp, err := ns.NodeUnpublishVolume(context.Background(), test.req)
			if status.Code(err) != status.Code(tt.expectErr) {
				t.Errorf("NodeUnpublishVolume() error = %v, expected = %v", err, tt.expectErr)
				return
			}
			if !reflect.DeepEqual(resp, tt.resp) {
				t.Errorf("NodeUnpublishVolume() resp:\n %+v\n expected:\n %+v", resp, tt.resp)
				return
			}
		})
	}
}

func TestNodeUnstageVolume(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tests := []struct {
		name          string
		req           *csi.NodeUnstageVolumeRequest
		resp          *csi.NodeUnstageVolumeResponse
		expectErr     error
		expectUnmount func(mntDir string, m *mounter.MockMounter)
	}{
		{
			name: "Success",
			req: &csi.NodeUnstageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: validStagingPath,
			},
			resp:      &csi.NodeUnstageVolumeResponse{},
			expectErr: nil,
			expectUnmount: func(mntDir string, m *mounter.MockMounter) {
				// Unmount moved to delete - no mounter calls expected here.
			},
		},
		{
			name: "Volume ID missing in request",
			req: &csi.NodeUnstageVolumeRequest{
				StagingTargetPath: validStagingPath,
			},
			resp:      nil,
			expectErr: status.Error(codes.InvalidArgument, "Volume ID missing in request"),
		},
		{
			name: "Staging Target Path not provided",
			req: &csi.NodeUnstageVolumeRequest{
				VolumeId: testVolumeID,
			},
			resp:      nil,
			expectErr: status.Error(codes.InvalidArgument, fmt.Errorf("Staging target path not provided").Error()),
		},
	}
	for _, test := range tests {
		var tt = test
		t.Run(tt.name, func(t *testing.T) {
			mntDir, err := os.MkdirTemp(os.TempDir(), "mount")
			if err != nil {
				t.Fatalf("failed to create tmp dir: %v", err)
			}

			defer func() {
				if err := os.RemoveAll(mntDir); err != nil {
					t.Errorf("failed to remove tmp dir: %v", err)
				}
			}()

			ns := initTestNodeServer(t, ctrl)
			if tt.expectUnmount != nil {
				tt.expectUnmount(mntDir, ns.mounter.(*mounter.MockMounter))
			}

			// Prefix staging and target path with tmp path for easy cleanup.
			req := tt.req
			if req.StagingTargetPath != "" {
				req.StagingTargetPath = filepath.Join(mntDir, req.StagingTargetPath)
			}

			resp, err := ns.NodeUnstageVolume(context.Background(), req)
			if status.Code(err) != status.Code(tt.expectErr) ||
				(err != nil && err.Error() != tt.expectErr.Error()) {
				t.Errorf("NodeUnstageVolume() error = %v, expected = %v", err, tt.expectErr)
				return
			}
			if !reflect.DeepEqual(resp, tt.resp) {
				t.Errorf("NodeUnstageVolume() resp:\n %+v\n expected:\n %+v", resp, tt.resp)
				return
			}
		})
	}
}

func TestNodeStageVolume(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	stdVolCap := &csi.VolumeCapability_Mount{
		Mount: &csi.VolumeCapability_MountVolume{
			FsType: "ext4",
		},
	}

	stdVolCapBlock := &csi.VolumeCapability_Block{
		Block: &csi.VolumeCapability_BlockVolume{},
	}

	volumeCap := csi.VolumeCapability_AccessMode{Mode: 2}

	tests := []struct {
		name        string
		req         *csi.NodeStageVolumeRequest
		resp        *csi.NodeStageVolumeResponse
		expectErr   error
		expectMount func(mntDir string, m *mounter.MockMounter)
	}{
		{
			name: "Success",
			req: &csi.NodeStageVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{
					AccessType: stdVolCap,
					AccessMode: &volumeCap,
				},
				VolumeId:          testVolumeID,
				StagingTargetPath: validStagingPath,
				VolumeContext:     map[string]string{},
			},
			resp:      &csi.NodeStageVolumeResponse{},
			expectErr: nil,
			expectMount: func(mntDir string, m *mounter.MockMounter) {
				m.EXPECT().IsLikelyNotMountPoint(gomock.Eq(filepath.Join(mntDir, validStagingPath))).Return(true, nil).Times(1)
				m.EXPECT().FormatAndMountSensitiveWithFormatOptions(validStagingPath, filepath.Join(mntDir, validStagingPath), "ext4", gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(1)
			},
		},
		{
			name: "Success (volumeMode is Block)",
			req: &csi.NodeStageVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{
					AccessType: stdVolCapBlock,
					AccessMode: &volumeCap,
				},
				VolumeId:          testVolumeID,
				StagingTargetPath: validStagingPath,
				VolumeContext:     map[string]string{},
			},
			resp:      &csi.NodeStageVolumeResponse{},
			expectErr: nil,
		},
		{
			name: "VolumeId is missing in request",
			req: &csi.NodeStageVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{
					AccessType: stdVolCap,
					AccessMode: &volumeCap,
				},
				StagingTargetPath: validStagingPath,
				VolumeContext:     map[string]string{},
			},
			resp:      nil,
			expectErr: status.Error(codes.InvalidArgument, "Volume ID missing in request"),
		},
		{
			name: "VolumeCapability is missing in request",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: validStagingPath,
				VolumeContext:     map[string]string{},
			},
			resp:      nil,
			expectErr: status.Error(codes.InvalidArgument, "Volume capability missing in request"),
		},
		{
			name: "Success already mount point",
			req: &csi.NodeStageVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{
					AccessType: stdVolCap,
					AccessMode: &volumeCap,
				},
				VolumeId:          testVolumeID,
				StagingTargetPath: validStagingPath,
				VolumeContext:     map[string]string{},
			},
			resp:      &csi.NodeStageVolumeResponse{},
			expectErr: nil,
			expectMount: func(mntDir string, m *mounter.MockMounter) {
				m.EXPECT().IsLikelyNotMountPoint(gomock.Eq(filepath.Join(mntDir, validStagingPath))).Return(false, nil).Times(1)
			},
		},
		{
			name: "Ensure mount error",
			req: &csi.NodeStageVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{
					AccessType: stdVolCap,
					AccessMode: &volumeCap,
				},
				VolumeId:          testVolumeID,
				StagingTargetPath: validStagingPath,
				VolumeContext:     map[string]string{},
			},
			resp:      nil,
			expectErr: status.Error(codes.Internal, fmt.Errorf("ensure mount error").Error()),
			expectMount: func(mntDir string, m *mounter.MockMounter) {
				err := fmt.Errorf("ensure mount error")
				m.EXPECT().IsLikelyNotMountPoint(gomock.Eq(filepath.Join(mntDir, validStagingPath))).Return(false, err).Times(1)
			},
		},
		{
			name: "Mounter error permission denied",
			req: &csi.NodeStageVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{
					AccessType: stdVolCap,
					AccessMode: &volumeCap,
				},
				VolumeId:          testVolumeID,
				StagingTargetPath: validStagingPath,
				VolumeContext:     map[string]string{},
			},
			resp:      nil,
			expectErr: status.Error(codes.PermissionDenied, os.ErrPermission.Error()),
			expectMount: func(mntDir string, m *mounter.MockMounter) {
				m.EXPECT().IsLikelyNotMountPoint(gomock.Eq(filepath.Join(mntDir, validStagingPath))).Return(true, nil).Times(1)
				m.EXPECT().FormatAndMountSensitiveWithFormatOptions(validStagingPath, filepath.Join(mntDir, validStagingPath), "ext4", gomock.Any(), gomock.Any(), gomock.Any()).Return(os.ErrPermission).Times(1)
			},
		},
		{
			name: "Mounter invalid argument",
			req: &csi.NodeStageVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{
					AccessType: stdVolCap,
					AccessMode: &volumeCap,
				},
				VolumeId:          testVolumeID,
				StagingTargetPath: validStagingPath,
				VolumeContext:     map[string]string{},
			},
			resp:      nil,
			expectErr: status.Error(codes.InvalidArgument, os.ErrInvalid.Error()),
			expectMount: func(mntDir string, m *mounter.MockMounter) {
				m.EXPECT().IsLikelyNotMountPoint(gomock.Eq(filepath.Join(mntDir, validStagingPath))).Return(true, nil).Times(1)
				m.EXPECT().FormatAndMountSensitiveWithFormatOptions(validStagingPath, filepath.Join(mntDir, validStagingPath), "ext4", gomock.Any(), gomock.Any(), gomock.Any()).Return(os.ErrInvalid).Times(1)
			},
		},
		{
			name: "PV Recovery Success",
			req: &csi.NodeStageVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{
					AccessType: stdVolCap,
					AccessMode: &volumeCap,
				},
				VolumeId:          recoveryOKVolumeID,
				StagingTargetPath: validStagingPath,
				VolumeContext:     map[string]string{},
			},
			resp:      &csi.NodeStageVolumeResponse{},
			expectErr: nil,
			expectMount: func(mntDir string, m *mounter.MockMounter) {
				m.EXPECT().IsLikelyNotMountPoint(gomock.Eq(filepath.Join(mntDir, validStagingPath))).Return(true, nil).Times(1)
				m.EXPECT().FormatAndMountSensitiveWithFormatOptions("/vg/testrecoveryok", filepath.Join(mntDir, validStagingPath), "ext4", gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(1)
			},
		},
		{
			name: "PV Recovery no volume context set",
			req: &csi.NodeStageVolumeRequest{
				VolumeCapability: &csi.VolumeCapability{
					AccessType: stdVolCap,
					AccessMode: &volumeCap,
				},
				VolumeId:          recoveryBrokenVolumeID,
				StagingTargetPath: validStagingPath,
				VolumeContext:     map[string]string{},
			},
			resp:      nil,
			expectErr: status.Error(codes.Internal, fmt.Errorf("volume request size is missing in pv attribute local.csi.azure.com/capacity - recovery impossible").Error()),
		},
	}
	for _, test := range tests {
		var tt = test
		t.Run(tt.name, func(t *testing.T) {
			mntDir, err := os.MkdirTemp(os.TempDir(), "mount")
			if err != nil {
				t.Fatalf("failed to create tmp dir: %v", err)
			}
			defer func() {
				if err := os.RemoveAll(mntDir); err != nil {
					t.Errorf("failed to remove tmp dir: %v", err)
				}
			}()

			ns := initTestNodeServer(t, ctrl)
			if tt.expectMount != nil {
				tt.expectMount(mntDir, ns.mounter.(*mounter.MockMounter))
			}

			// Prefix staging and target path with tmp path for easy cleanup.
			req := tt.req
			if req.StagingTargetPath != "" {
				req.StagingTargetPath = filepath.Join(mntDir, req.StagingTargetPath)
			}

			resp, err := ns.NodeStageVolume(context.Background(), req)
			if status.Code(err) != status.Code(tt.expectErr) ||
				(err != nil && err.Error() != tt.expectErr.Error()) {
				t.Errorf("NodeStageVolume() error = %v, expected = %v", err, tt.expectErr)
				return
			}
			if !reflect.DeepEqual(resp, tt.resp) {
				t.Errorf("NodeStageVolume() resp:\n %+v\n expected:\n %+v", resp, tt.resp)
				return
			}
		})
	}
}

func TestNodeGetInfo(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	ns := initTestNodeServer(t, ctrl)
	tests := []struct {
		name      string
		zone      string
		req       *csi.NodeGetInfoRequest
		resp      *csi.NodeGetInfoResponse
		expectErr error
	}{
		{
			name: "Success",
			req:  &csi.NodeGetInfoRequest{},
			resp: &csi.NodeGetInfoResponse{
				NodeId:             testNodeName,
				AccessibleTopology: &csi.Topology{Segments: map[string]string{testTopologyKey: testNodeName}},
			},
			expectErr: nil,
		},
	}
	for _, test := range tests {
		var tt = test
		t.Run(tt.name, func(t *testing.T) {
			resp, err := ns.NodeGetInfo(context.Background(), tt.req)
			if status.Code(err) != status.Code(tt.expectErr) {
				t.Errorf("NodeGetInfo() error = %v, expected = %v", err, tt.expectErr)
				return
			}
			if !reflect.DeepEqual(resp, tt.resp) {
				t.Errorf("NodeGetInfo() resp:\n %+v\n expected:\n %+v", resp, tt.resp)
				return
			}
		})
	}
}

func TestNodeGetCapabilities(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	ns := initTestNodeServer(t, ctrl)

	tests := []struct {
		name      string
		req       *csi.NodeGetCapabilitiesRequest
		resp      *csi.NodeGetCapabilitiesResponse
		expectErr error
	}{
		{
			name: "Success",
			req:  &csi.NodeGetCapabilitiesRequest{},
			resp: &csi.NodeGetCapabilitiesResponse{
				Capabilities: []*csi.NodeServiceCapability{
					{
						Type: &csi.NodeServiceCapability_Rpc{
							Rpc: &csi.NodeServiceCapability_RPC{
								Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
							},
						},
					},
				},
			},
			expectErr: nil,
		},
	}
	for _, test := range tests {
		var tt = test
		t.Run(tt.name, func(t *testing.T) {
			resp, err := ns.NodeGetCapabilities(context.Background(), tt.req)
			if status.Code(err) != status.Code(tt.expectErr) {
				t.Errorf("NodeGetCapabilities() error = %v, expected = %v", err, tt.expectErr)
				return
			}
			if !reflect.DeepEqual(resp, tt.resp) {
				t.Errorf("NodeGetCapabilities() resp:\n %+v\n expected:\n %+v", resp, tt.resp)
				return
			}
		})
	}
}

func TestNodeExpandVolume(t *testing.T) {
	t.Skip("Skipping test for NodeExpandVolume") // TODO: re-enable this test
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skipf("not supported on GOOS=%s", runtime.GOOS)
	}
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	ns := initTestNodeServer(t, ctrl)

	stdCapacityRange := &csi.CapacityRange{
		RequiredBytes: convert.GiBToBytes(4),
		LimitBytes:    convert.GiBToBytes(8),
	}

	blockVolumePath := "/tmp/block-volume-path"
	targetTest, err := testutil.GetWorkDirPath("target_test")
	if err != nil {
		t.Fatalf("failed to get target test path: %v\n", err)
		os.Exit(1)
	}

	if err := os.RemoveAll(targetTest); err != nil {
		t.Fatalf("failed to remove target test path: %v\n", err)
	}
	if err := os.RemoveAll(blockVolumePath); err != nil {
		t.Fatalf("failed to remove block volume path: %v\n", err)
	}
	_ = testutil.MakeDir(targetTest)
	_ = testutil.MakeDir(blockVolumePath)

	notFoundErr := status.Error(codes.NotFound, "exit status 1")
	getSizeErr := status.Errorf(codes.Internal, "Failed to get volume size")
	resizeTooSmallErr := status.Errorf(codes.Internal, "Volume size is less than requested size")
	notFoundErrAction := func() ([]byte, []byte, error) {
		return []byte{}, []byte{}, notFoundErr
	}

	tests := []struct {
		name              string
		req               *csi.NodeExpandVolumeRequest
		resp              *csi.NodeExpandVolumeResponse
		wantMountPointErr bool
		expectErr         error
		outputScripts     []testingexec.FakeAction
	}{
		{
			name:      "Volume ID missing in request",
			req:       &csi.NodeExpandVolumeRequest{},
			expectErr: status.Error(codes.InvalidArgument, "Volume ID missing in request"),
		},
		{
			name: "Path not found",
			req: &csi.NodeExpandVolumeRequest{
				CapacityRange:     stdCapacityRange,
				VolumePath:        "./test",
				VolumeId:          testVolumeID,
				StagingTargetPath: "test",
			},
			expectErr: status.Error(codes.NotFound, "Invalid path"),
		},
		{
			name: "Volume path not provided",
			req: &csi.NodeExpandVolumeRequest{
				CapacityRange:     stdCapacityRange,
				StagingTargetPath: "test",
				VolumeId:          "test",
			},
			expectErr: status.Error(codes.InvalidArgument, "Volume path must be provided"),
		},
		{
			name: "Expand failure",
			req: &csi.NodeExpandVolumeRequest{
				CapacityRange:     stdCapacityRange,
				VolumePath:        targetTest,
				VolumeId:          testVolumeID,
				StagingTargetPath: "test",
			},
			expectErr:     getSizeErr,
			outputScripts: []testingexec.FakeAction{notFoundErrAction},
		},
		{
			name: "Resize too small failure",
			req: &csi.NodeExpandVolumeRequest{
				CapacityRange:     stdCapacityRange,
				VolumePath:        targetTest,
				VolumeId:          testVolumeID,
				StagingTargetPath: "test",
			},
			expectErr: resizeTooSmallErr,
		},
		{
			name: "Successfully expanded",
			req: &csi.NodeExpandVolumeRequest{
				CapacityRange:     stdCapacityRange,
				VolumePath:        targetTest,
				VolumeId:          testVolumeID,
				StagingTargetPath: "test",
			},
			resp: &csi.NodeExpandVolumeResponse{
				CapacityBytes: stdCapacityRange.RequiredBytes,
			},
		},
		{
			name: "Block volume expansion",
			req: &csi.NodeExpandVolumeRequest{
				CapacityRange:     stdCapacityRange,
				VolumePath:        blockVolumePath,
				VolumeId:          testVolumeID,
				StagingTargetPath: "test",
			},
			resp: &csi.NodeExpandVolumeResponse{},
		},
		{
			name: "Block volume expansion volume capability ignore path",
			req: &csi.NodeExpandVolumeRequest{
				CapacityRange:     stdCapacityRange,
				VolumePath:        targetTest,
				VolumeId:          testVolumeID,
				StagingTargetPath: "test",
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Block{
						Block: &csi.VolumeCapability_BlockVolume{},
					},
				},
			},
			resp: &csi.NodeExpandVolumeResponse{},
		},
	}
	for _, test := range tests {
		var tt = test
		t.Run(tt.name, func(t *testing.T) {
			mntDir, err := os.MkdirTemp(os.TempDir(), "mount")
			if err != nil {
				t.Fatalf("failed to create tmp dir: %v", err)
			}
			defer func() {
				if err := os.RemoveAll(mntDir); err != nil {
					t.Errorf("failed to remove tmp dir: %v", err)
				}
			}()

			// Prefix staging and target path with tmp path for easy cleanup.
			req := tt.req
			if req.StagingTargetPath != "" {
				req.StagingTargetPath = filepath.Join(mntDir, req.StagingTargetPath)
			}
			resp, err := ns.NodeExpandVolume(context.Background(), req)

			if status.Code(err) != status.Code(tt.expectErr) ||
				(err != nil && err.Error() != tt.expectErr.Error()) {
				t.Errorf("NodeExpandVolume() error = %v, expected = %v", err, tt.expectErr)
				return
			}
			if !reflect.DeepEqual(resp, tt.resp) {
				t.Errorf("NodeExpandVolume() resp:\n %+v\n expected:\n %+v", resp, tt.resp)
				return
			}
		})
	}

	if err := os.RemoveAll(targetTest); err != nil {
		t.Fatalf("failed to remove target test path: %v\n", err)
	}
	if err := os.RemoveAll(blockVolumePath); err != nil {
		t.Fatalf("failed to remove block volume path: %v\n", err)
	}
}

func TestCheckMountError(t *testing.T) {
	tests := []struct {
		desc         string
		err          error
		expectedCode codes.Code
	}{
		{
			desc:         "Permission denied error",
			err:          errors.New("permission denied"),
			expectedCode: codes.PermissionDenied,
		},
		{
			desc:         "Invalid argument error",
			err:          errors.New("invalid argument"),
			expectedCode: codes.InvalidArgument,
		},
		{
			desc:         "Other error",
			err:          errors.New("some other error"),
			expectedCode: codes.Internal,
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			err := CheckMountError(test.err)
			st, ok := status.FromError(err)
			if !ok {
				t.Errorf("CheckMountError did not return a gRPC status error")
				return
			}
			if st.Code() != test.expectedCode {
				t.Errorf("CheckMountError returned incorrect gRPC status code, got: %v, want: %v", st.Code(), test.expectedCode)
			}
			if st.Message() != test.err.Error() {
				t.Errorf("CheckMountError returned incorrect error message, got: %v, want: %v", st.Message(), test.err.Error())
			}
		})
	}
}

func TestEnsureTargetFile(t *testing.T) {
	ctrl := gomock.NewController(t)
	testTarget, err := testutil.GetWorkDirPath("test")
	if err != nil {
		t.Errorf("Failed to get work dir path: %v", err)
		os.Exit(1)
	}
	filePath, err := testutil.GetWorkDirPath("test/invalidDir")
	if err != nil {
		t.Errorf("Failed to get work dir path: %v", err)
		os.Exit(1)
	}

	tests := []struct {
		desc          string
		targetPath    string
		expectedErr   bool
		expectedCalls func(m *mounter.MockMounter)
	}{
		{
			desc:        "Valid target path",
			targetPath:  testTarget,
			expectedErr: false,
			expectedCalls: func(m *mounter.MockMounter) {
				m.EXPECT().FileExists(gomock.Eq(testTarget)).Return(true, nil).Times(1)
			},
		},
		{
			desc:        "Invalid target path",
			targetPath:  filePath,
			expectedErr: true,
			expectedCalls: func(m *mounter.MockMounter) {
				m.EXPECT().FileExists(gomock.Eq(filePath)).Return(false, fmt.Errorf("invalid")).Times(1)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			ns := initTestNodeServer(t, ctrl)

			if test.expectedCalls != nil {
				test.expectedCalls(ns.mounter.(*mounter.MockMounter))
			}

			err := ns.EnsureTargetFile(test.targetPath)
			if err != nil && !test.expectedErr {
				t.Errorf("EnsureTargetFile error = %v, but error was not expected", err)
				return
			}
		})
	}
	// testTarget will be removed since it is created by EnsureTargetFile on success
	_ = os.RemoveAll(testTarget)
}

func TestEnsureMount(t *testing.T) {
	notDirTarget := "notDirTarget.go"
	invalidTargetPath := "invalidTargetPath"
	alreadyExistTarget := "alreadyExistTarget"
	targetTest := "targetTest"
	newDir := "newDir"
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tests := []struct {
		desc        string
		target      string
		result      bool
		expectErr   error
		expectMount func(m *mounter.MockMounter)
	}{
		{
			desc:      "Method IsLikelyNotMountPoint Error",
			target:    invalidTargetPath,
			expectErr: errors.New("Method IsLikelyNotMountPoint Failed"),
			result:    false,
			expectMount: func(m *mounter.MockMounter) {
				m.EXPECT().IsLikelyNotMountPoint(gomock.Eq(invalidTargetPath)).Return(true, fmt.Errorf("Method IsLikelyNotMountPoint Failed")).Times(1)
			},
		},
		{
			desc:      "Not a directory",
			target:    notDirTarget,
			expectErr: errors.New("Method IsLikelyNotMountPoint Failed"),
			result:    false,
			expectMount: func(m *mounter.MockMounter) {
				m.EXPECT().IsLikelyNotMountPoint(gomock.Eq(notDirTarget)).Return(true, fs.PathError{}.Err).Times(1)
			},
		},
		{
			desc:      "Directory does not exist",
			target:    newDir,
			expectErr: nil,
			result:    false,
			expectMount: func(m *mounter.MockMounter) {
				m.EXPECT().IsLikelyNotMountPoint(gomock.Eq(newDir)).Return(false, os.ErrNotExist).Times(1)
				m.EXPECT().MakeDir(gomock.Eq(newDir)).Return(nil).Times(1)
			},
		},
		{
			desc:      "Success",
			target:    targetTest,
			expectErr: nil,
			result:    true,
			expectMount: func(m *mounter.MockMounter) {
				m.EXPECT().IsLikelyNotMountPoint(gomock.Eq(targetTest)).Return(false, nil).Times(1)
			},
		},
		{
			desc:      "Already existing mount",
			target:    alreadyExistTarget,
			expectErr: nil,
			result:    true,
			expectMount: func(m *mounter.MockMounter) {
				m.EXPECT().IsLikelyNotMountPoint(gomock.Eq(alreadyExistTarget)).Return(false, nil).Times(1)
			},
		},
	}

	for _, test := range tests {
		var tt = test
		t.Run(tt.desc, func(t *testing.T) {

			ns := initTestNodeServer(t, ctrl)
			if tt.expectMount != nil {
				tt.expectMount(ns.mounter.(*mounter.MockMounter))
			}

			ok, err := ns.EnsureMount(tt.target)
			if err != nil && err.Error() != tt.expectErr.Error() {
				t.Errorf("EnsureMount error = %v, expected = %v", err, tt.expectErr)
				return
			}
			if ok != tt.result {
				t.Errorf("EnsureMount result:\n %+v\n expected:\n %+v", ok, tt.result)
				return
			}
		})
	}

	// newDir will be created since mock is set to return os.ErrNotExist
	_ = os.RemoveAll(newDir)
}

func TestNodeGetVolumeStats(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tests := []struct {
		name         string
		req          *csi.NodeGetVolumeStatsRequest
		volumePath   string
		volumeExists bool
		expectErr    error
		expectResp   *csi.NodeGetVolumeStatsResponse
		expectMount  func(m *mounter.MockMounter)
	}{
		{
			name: "Volume ID missing in request",
			req: &csi.NodeGetVolumeStatsRequest{
				VolumePath: "/mnt/test",
			},
			volumePath:   "/mnt/test",
			volumeExists: true,
			expectErr:    status.Error(codes.InvalidArgument, "NodeGetVolumeStats volume ID was empty"),
			expectResp:   nil,
		},
		{
			name: "Volume path missing in request",
			req: &csi.NodeGetVolumeStatsRequest{
				VolumeId: "testvolumeid",
			},
			volumePath:   "",
			volumeExists: true,
			expectErr:    status.Error(codes.InvalidArgument, "NodeGetVolumeStats volume path was empty"),
			expectResp:   nil,
		},
		{
			name: "Volume path does not exist",
			req: &csi.NodeGetVolumeStatsRequest{
				VolumeId:   "testvolumeid",
				VolumePath: "/mnt/test",
			},
			volumePath:   "/mnt/test",
			volumeExists: false,
			expectErr:    status.Error(codes.Internal, "failed to stat volume path /mnt/test: error"),
			expectResp:   nil,
			expectMount: func(m *mounter.MockMounter) {
				m.EXPECT().PathExists(gomock.Eq("/mnt/test")).Return(false, status.Error(codes.Internal, "error")).Times(1)
			},
		},
		{
			name: "Failed to stat volume path",
			req: &csi.NodeGetVolumeStatsRequest{
				VolumeId:   "testvolumeid",
				VolumePath: "/mnt/test",
			},
			volumePath:   "/mnt/test",
			volumeExists: true,
			expectErr:    status.Error(codes.Internal, "failed to stat volume path /mnt/test: stat error"),
			expectResp:   nil,
			expectMount: func(m *mounter.MockMounter) {
				m.EXPECT().PathExists(gomock.Eq("/mnt/test")).Return(true, fmt.Errorf("stat error")).Times(1)
			},
		},
		{
			name: "Failed to determine whether volume path is block device",
			req: &csi.NodeGetVolumeStatsRequest{
				VolumeId:   "testvolumeid",
				VolumePath: "/mnt/test",
			},
			volumePath:   "/mnt/test",
			volumeExists: true,
			expectErr:    status.Error(codes.Internal, "failed to determine whether /mnt/test is block device: error"),
			expectResp:   nil,
			expectMount: func(m *mounter.MockMounter) {
				m.EXPECT().PathExists(gomock.Eq("/mnt/test")).Return(true, nil).Times(1)
				m.EXPECT().PathIsDevice(gomock.Eq("/mnt/test")).Return(false, fmt.Errorf("error")).Times(1)
			},
		},
		{
			name: "Failed to get block capacity on path",
			req: &csi.NodeGetVolumeStatsRequest{
				VolumeId:   "testvolumeid",
				VolumePath: "/mnt/test",
			},
			volumePath:   "/mnt/test",
			volumeExists: true,
			expectErr:    status.Error(codes.Internal, "failed to get block capacity on path /mnt/test: error"),
			expectResp:   nil,
			expectMount: func(m *mounter.MockMounter) {
				m.EXPECT().PathExists(gomock.Eq("/mnt/test")).Return(true, nil).Times(1)
				m.EXPECT().PathIsDevice(gomock.Eq("/mnt/test")).Return(true, nil).Times(1)
				m.EXPECT().GetBlockSizeBytes(gomock.Eq("/mnt/test")).Return(int64(0), fmt.Errorf("failed to get block capacity on path /mnt/test: error")).Times(1)
			},
		},
	}

	for _, test := range tests {
		var tt = test
		t.Run(tt.name, func(t *testing.T) {
			mntDir, err := os.MkdirTemp(os.TempDir(), "mount")
			if err != nil {
				t.Fatalf("failed to create tmp dir: %v", err)
			}
			defer func() {
				if err := os.RemoveAll(mntDir); err != nil {
					t.Errorf("failed to remove tmp dir: %v", err)
				}
			}()

			ns := initTestNodeServer(t, ctrl)
			if tt.expectMount != nil {
				tt.expectMount(ns.mounter.(*mounter.MockMounter))
			}

			resp, err := ns.NodeGetVolumeStats(context.Background(), tt.req)
			if status.Code(err) != status.Code(tt.expectErr) {
				t.Errorf("NodeGetVolumeStats() error = %v, expected = %v", err, tt.expectErr)
				return
			}
			if !reflect.DeepEqual(resp, tt.expectResp) {
				t.Errorf("NodeGetVolumeStats() resp:\n %+v\n expected:\n %+v", resp, tt.expectResp)
				return
			}
		})
	}
}

func Test_getCapacityAndLimit(t *testing.T) {
	tests := []struct {
		name         string
		attrs        map[string]string
		wantCapacity int64
		wantLimit    int64
		wantErr      bool
	}{
		{
			name:         "valid capacity and limit",
			attrs:        map[string]string{lvm.CapacityParam: "10Gi", lvm.LimitParam: "20Gi"},
			wantCapacity: 10 * 1024 * 1024 * 1024,
			wantLimit:    20 * 1024 * 1024 * 1024,
			wantErr:      false,
		},
		{
			name:         "valid capacity and limit using bytes",
			attrs:        map[string]string{lvm.CapacityParam: "10737418240", lvm.LimitParam: "21474836480"},
			wantCapacity: 10 * 1024 * 1024 * 1024,
			wantLimit:    20 * 1024 * 1024 * 1024,
			wantErr:      false,
		},
		{
			name:         "valid capacity, unset limit",
			attrs:        map[string]string{lvm.CapacityParam: "5Gi"},
			wantCapacity: 5 * 1024 * 1024 * 1024,
			wantLimit:    0,
			wantErr:      false,
		},
		{
			name:         "valid capacity, empty limit",
			attrs:        map[string]string{lvm.CapacityParam: "5Gi", lvm.LimitParam: ""},
			wantCapacity: 5 * 1024 * 1024 * 1024,
			wantLimit:    0,
			wantErr:      false,
		},
		{
			name:         "empty capacity, valid limit",
			attrs:        map[string]string{lvm.CapacityParam: "", lvm.LimitParam: "1Ti"},
			wantCapacity: 0,
			wantLimit:    1 * 1024 * 1024 * 1024 * 1024,
			wantErr:      false,
		},
		{
			name:         "invalid capacity",
			attrs:        map[string]string{lvm.CapacityParam: "foo", lvm.LimitParam: "1Gi"},
			wantCapacity: 0,
			wantLimit:    0,
			wantErr:      true,
		},
		{
			name:         "invalid limit",
			attrs:        map[string]string{lvm.CapacityParam: "1Gi", lvm.LimitParam: "bar"},
			wantCapacity: 0,
			wantLimit:    0,
			wantErr:      true,
		},
		{
			name:         "both empty",
			attrs:        map[string]string{lvm.CapacityParam: "", lvm.LimitParam: ""},
			wantCapacity: 0,
			wantLimit:    0,
			wantErr:      false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCapacityBytes, gotLimitBytes, err := getCapacityAndLimit(tt.attrs)
			if (err != nil) != tt.wantErr {
				t.Errorf("getCapacityAndLimit() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotCapacityBytes != tt.wantCapacity {
				t.Errorf("getCapacityAndLimit() gotCapacityBytes = %v, want %v", gotCapacityBytes, tt.wantCapacity)
			}
			if gotLimitBytes != tt.wantLimit {
				t.Errorf("getCapacityAndLimit() gotLimitBytes = %v, want %v", gotLimitBytes, tt.wantLimit)
			}
		})
	}
}
