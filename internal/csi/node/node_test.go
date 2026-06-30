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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"local-csi-driver/internal/csi/capability"
	"local-csi-driver/internal/csi/core"
	"local-csi-driver/internal/csi/core/lvm"
	"local-csi-driver/internal/csi/mounter"
	"local-csi-driver/internal/csi/testutil"
	"local-csi-driver/internal/pkg/events"
	"local-csi-driver/internal/pkg/telemetry"
)

const (
	testNodeName             = "node-name"
	driverName               = "testlocaldisk.csi.acstor.io"
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
	testTopologyKey          = corev1.LabelHostname
	selectedNodeAnnotation   = "testlocaldisk.csi.acstor.io/selected-node"
	selectedInitialNodeParam = "testlocaldisk.csi.acstor.io/selected-initial-node"
)

func initTestNodeServer(_ *testing.T, ctrl *gomock.Controller) *Server {
	vc := core.NewFake()
	m := mounter.NewMockMounter(ctrl)
	r := events.NewNoopRecorder()
	tp := telemetry.NewNoopTracerProvider()
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
								selectedInitialNodeParam:           "test-node",
								"localdisk.csi.acstor.io/capacity": "1Gi",
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
			expectErr: status.Error(codes.Internal, fmt.Errorf("volume request size is missing in pv attribute localdisk.csi.acstor.io/capacity - recovery impossible").Error()),
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
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skipf("not supported on GOOS=%s", runtime.GOOS)
	}

	expandCapacity := int64(8 * 1024 * 1024 * 1024) // 8 GiB

	// Create a real directory for the volume path.
	targetTest, err := testutil.GetWorkDirPath("expand_target_test")
	if err != nil {
		t.Fatalf("failed to get target test path: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(targetTest) })
	_ = testutil.MakeDir(targetTest)

	tests := []struct {
		name            string
		req             *csi.NodeExpandVolumeRequest
		fakeCapacity    int64
		fakeErr         error
		expectErrCode   codes.Code
		expectErr       bool
		expectResp      bool
		expectCapacity  int64
		checkAnnotation bool
	}{
		{
			name:          "Volume ID missing",
			req:           &csi.NodeExpandVolumeRequest{},
			expectErr:     true,
			expectErrCode: codes.InvalidArgument,
		},
		{
			name: "Volume path not provided",
			req: &csi.NodeExpandVolumeRequest{
				VolumeId: testVolumeID,
			},
			expectErr:     true,
			expectErrCode: codes.InvalidArgument,
		},
		{
			name: "Volume path does not exist",
			req: &csi.NodeExpandVolumeRequest{
				VolumeId:   testVolumeID,
				VolumePath: "/nonexistent/path",
			},
			expectErr:     true,
			expectErrCode: codes.NotFound,
		},
		{
			name: "Invalid volume ID format",
			req: &csi.NodeExpandVolumeRequest{
				VolumeId:   "invalidId",
				VolumePath: targetTest,
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: expandCapacity,
				},
			},
			expectErr:     true,
			expectErrCode: codes.InvalidArgument,
		},
		{
			name: "Volume expand fails",
			req: &csi.NodeExpandVolumeRequest{
				VolumeId:   testVolumeID,
				VolumePath: targetTest,
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: expandCapacity,
				},
			},
			fakeErr:       fmt.Errorf("extend failed"),
			expectErr:     true,
			expectErrCode: codes.Internal,
		},
		{
			name: "Successfully expanded with annotation patched",
			req: &csi.NodeExpandVolumeRequest{
				VolumeId:   testVolumeID,
				VolumePath: targetTest,
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: expandCapacity,
				},
			},
			fakeCapacity:    expandCapacity,
			expectResp:      true,
			expectCapacity:  expandCapacity,
			checkAnnotation: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			vc := core.NewFake()
			vc.DiskPoolCapacity = tt.fakeCapacity
			vc.Err = tt.fakeErr

			m := mounter.NewMockMounter(ctrl)
			r := events.NewNoopRecorder()
			tp := telemetry.NewNoopTracerProvider()
			scheme := k8sruntime.NewScheme()
			_ = corev1.AddToScheme(scheme)

			// PV named "pv" matches GetVolumeName("vg#pv") → "pv"
			pv := corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pv",
				},
				Spec: corev1.PersistentVolumeSpec{
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver:       driverName,
							VolumeHandle: testVolumeID,
							VolumeAttributes: map[string]string{
								lvm.CapacityParam: "4Gi",
							},
						},
					},
				},
			}
			k8sClient := fake.NewClientBuilder().WithScheme(scheme).
				WithObjects(&pv).
				Build()

			caps := []*csi.NodeServiceCapability{
				capability.NewNodeServiceCapability(csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME),
			}
			ns := New(vc, testNodeName, selectedNodeAnnotation, selectedInitialNodeParam, driverName, caps, m, k8sClient, true, r, tp)

			resp, err := ns.NodeExpandVolume(context.Background(), tt.req)
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

			// Verify expanded-capacity annotation was written to the PV.
			if tt.checkAnnotation {
				var updatedPV corev1.PersistentVolume
				if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "pv"}, &updatedPV); err != nil {
					t.Fatalf("failed to get PV after expand: %v", err)
				}
				got := updatedPV.Annotations[lvm.ExpandedCapacityParam]
				want := fmt.Sprint(tt.expectCapacity)
				if got != want {
					t.Errorf("PV annotation %s = %q, want %q", lvm.ExpandedCapacityParam, got, want)
				}
			}
		})
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
		annotations  map[string]string
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
		{
			name:         "expanded capacity annotation overrides attrs",
			attrs:        map[string]string{lvm.CapacityParam: "10737418240"},
			annotations:  map[string]string{lvm.ExpandedCapacityParam: "11811160064"},
			wantCapacity: 11811160064,
			wantLimit:    0,
			wantErr:      false,
		},
		{
			name:         "expanded capacity annotation smaller than attrs is ignored",
			attrs:        map[string]string{lvm.CapacityParam: "10737418240"},
			annotations:  map[string]string{lvm.ExpandedCapacityParam: "5368709120"},
			wantCapacity: 10737418240,
			wantLimit:    0,
			wantErr:      false,
		},
		{
			name:         "invalid expanded capacity annotation falls back to attrs",
			attrs:        map[string]string{lvm.CapacityParam: "10Gi"},
			annotations:  map[string]string{lvm.ExpandedCapacityParam: "invalid"},
			wantCapacity: 10 * 1024 * 1024 * 1024,
			wantLimit:    0,
			wantErr:      false,
		},
		{
			name:         "nil annotations uses attrs capacity",
			attrs:        map[string]string{lvm.CapacityParam: "10Gi"},
			annotations:  nil,
			wantCapacity: 10 * 1024 * 1024 * 1024,
			wantLimit:    0,
			wantErr:      false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCapacityBytes, gotLimitBytes, err := getCapacityAndLimit(tt.attrs, tt.annotations)
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
