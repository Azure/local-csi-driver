// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"local-csi-driver/internal/csi/core"
	"local-csi-driver/internal/csi/mounter"
	"local-csi-driver/internal/pkg/events"
	"local-csi-driver/internal/pkg/telemetry"
)

var selectedNodeAnnotation = "testlocaldisk.csi.acstor.io/selected-node"
var selectedInitialNodeAnnotation = "testlocaldisk.csi.acstor.io/selected-initial-node"

func initTestControllerServer(ctrl *gomock.Controller) *Server {
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
								selectedInitialNodeAnnotation: "test-node",
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
					Name: "pv-wrong-node-1",
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
							VolumeHandle: "vg#pv-wrong-node-1",
							VolumeAttributes: map[string]string{
								selectedInitialNodeAnnotation: "test-wrong-node",
							},
						},
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pv-node-not-found-0",
					Annotations: map[string]string{
						selectedNodeAnnotation: "test-node-not-found",
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
					Name: "pv-node-not-found-1",
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
								selectedInitialNodeAnnotation: "test-node-not-found",
							},
						},
					},
				},
			},
		},
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "test-namespace",
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
		},
	}

	nodes := corev1.NodeList{
		Items: []corev1.Node{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-wrong-node",
				},
			},
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithLists(&pvs, &nodes).
		WithObjects(pvc).
		Build()

	caps := []*csi.ControllerServiceCapability{
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
				},
			},
		},
	}
	modes := []*csi.VolumeCapability_AccessMode{
		{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
	}

	return New(vc, caps, modes, m, client, "test-node", selectedNodeAnnotation, selectedInitialNodeAnnotation, true, r, tp)
}

func TestServer_CreateVolume(t *testing.T) {
	ctrl := gomock.NewController(t)
	server := initTestControllerServer(ctrl)
	tests := []struct {
		name    string
		pre     func()
		req     *csi.CreateVolumeRequest
		want    *csi.CreateVolumeResponse
		wantErr bool
		errCode codes.Code
	}{
		{
			name: "valid request",
			pre: func() {
				server.volume.(*core.Fake).Volume = &csi.Volume{
					VolumeId: "vg#test-volume",
				}
			},
			req: &csi.CreateVolumeRequest{
				Name: "test-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
				Parameters: map[string]string{
					core.PVCNameParam:      "test-pvc",
					core.PVCNamespaceParam: "test-namespace",
				},
			},
			want: &csi.CreateVolumeResponse{
				Volume: &csi.Volume{
					VolumeId: "vg#test-volume",
					VolumeContext: map[string]string{
						selectedInitialNodeAnnotation: "test-node",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid volume capability",
			req: &csi.CreateVolumeRequest{
				Name: "test-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY,
						},
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
			},
			wantErr: true,
			errCode: codes.InvalidArgument,
		},
		{
			name: "invalid volume capacity request",
			req: &csi.CreateVolumeRequest{
				Name: "test-volume",
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: -1, // Invalid capacity
				},
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
			},
			wantErr: true,
			errCode: codes.InvalidArgument,
		},
		{
			name: "invalid volume capacity limit",
			req: &csi.CreateVolumeRequest{
				Name: "test-volume",
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 1024 * 1024 * 1024, // Valid capacity
					LimitBytes:    -1,                 // Invalid limit
				},

				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
			},
			wantErr: true,
			errCode: codes.InvalidArgument,
		},
		{
			name: "invalid volume capacity limit lower than requested",
			req: &csi.CreateVolumeRequest{
				Name: "test-volume",
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 1024 * 1024 * 1024, // Valid capacity
					LimitBytes:    1024,               // Lower than requested
				},

				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
			},
			wantErr: true,
			errCode: codes.InvalidArgument,
		},
		{
			name: "volume create failed",
			pre: func() {
				server.volume.(*core.Fake).Err = errors.New("volume create failed")
			},
			req: &csi.CreateVolumeRequest{
				Name: "test-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
				Parameters: map[string]string{
					core.PVCNameParam:      "test-pvc",
					core.PVCNamespaceParam: "test-namespace",
				},
			},
			wantErr: true,
			errCode: codes.Internal,
		},
		{
			name: "server resource exhausted failed",
			pre: func() {
				server.volume.(*core.Fake).Err = core.ErrResourceExhausted
			},
			req: &csi.CreateVolumeRequest{
				Name: "test-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
				Parameters: map[string]string{
					core.PVCNameParam:      "test-pvc",
					core.PVCNamespaceParam: "test-namespace",
				},
			},
			wantErr: true,
			errCode: codes.ResourceExhausted,
		},
		{
			name: "server already exists failed",
			pre: func() {
				server.volume.(*core.Fake).Err = core.ErrVolumeSizeMismatch
			},
			req: &csi.CreateVolumeRequest{
				Name: "test-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
				Parameters: map[string]string{
					core.PVCNameParam:      "test-pvc",
					core.PVCNamespaceParam: "test-namespace",
				},
			},
			wantErr: true,
			errCode: codes.AlreadyExists,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.pre != nil {
				tt.pre()
			}

			resp, err := server.CreateVolume(context.Background(), tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateVolume() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				st, ok := status.FromError(err)
				if !ok {
					t.Errorf("CreateVolume() error = %v, expected gRPC status error", err)
					return
				}
				if st.Code() != tt.errCode {
					t.Errorf("CreateVolume() error code = %v, wantErrCode %v", st.Code(), tt.errCode)
				}
			}
			if !tt.wantErr && resp == nil {
				t.Errorf("CreateVolume() response = %v, expected non-nil response", resp)
			}
			if tt.want != nil {
				if !reflect.DeepEqual(resp, tt.want) {
					t.Errorf("CreateVolume() response = %v, want %v", resp, tt.want)
				}
			}
		})
	}
}

func TestServer_DeleteVolume(t *testing.T) {
	const baseDir = "/dev/test"

	ctrl := gomock.NewController(t)

	tests := []struct {
		name        string
		pre         func(cs *Server)
		req         *csi.DeleteVolumeRequest
		want        *csi.DeleteVolumeResponse
		wantErr     bool
		errCode     codes.Code
		expectMount func(m *mounter.MockMounter)
	}{
		{
			name: "valid request",
			pre: func(cs *Server) {
				cs.volume.(*core.Fake).Volume = &csi.Volume{
					VolumeId: "vg#pv-valid-0",
				}
				cs.volume.(*core.Fake).BaseDir = baseDir
			},
			req: &csi.DeleteVolumeRequest{
				VolumeId: "vg#pv-valid-0",
			},
			want:    &csi.DeleteVolumeResponse{},
			wantErr: false,
			expectMount: func(m *mounter.MockMounter) {
				m.EXPECT().CleanupStagingDir(gomock.Any(), "/dev/test/vg/pv-valid-0").Return(nil).Times(1)
			},
		},
		{
			name: "valid request on pv with initial node param",
			pre: func(cs *Server) {
				cs.volume.(*core.Fake).Volume = &csi.Volume{
					VolumeId: "vg#pv-valid-1",
				}
				cs.volume.(*core.Fake).BaseDir = baseDir
			},
			req: &csi.DeleteVolumeRequest{
				VolumeId: "vg#pv-valid-1",
			},
			want:    &csi.DeleteVolumeResponse{},
			wantErr: false,
			expectMount: func(m *mounter.MockMounter) {
				m.EXPECT().CleanupStagingDir(gomock.Any(), "/dev/test/vg/pv-valid-1").Return(nil).Times(1)
			},
		},
		{
			name: "volume not found",
			req: &csi.DeleteVolumeRequest{
				VolumeId: "vg#invalid-volume-id",
			},
			want:    &csi.DeleteVolumeResponse{},
			wantErr: false,
		},
		{
			name: "volume unmount failed",
			pre: func(cs *Server) {
				cs.volume.(*core.Fake).Volume = &csi.Volume{
					VolumeId: "vg#pv-valid-0",
				}
				cs.volume.(*core.Fake).BaseDir = baseDir
			},
			req: &csi.DeleteVolumeRequest{
				VolumeId: "vg#pv-valid-0",
			},
			wantErr: true,
			errCode: codes.Internal,
			expectMount: func(m *mounter.MockMounter) {
				m.EXPECT().CleanupStagingDir(gomock.Any(), "/dev/test/vg/pv-valid-0").Return(fmt.Errorf("unmount error")).Times(1)
			},
		},
		{
			name: "volume delete failed",
			pre: func(cs *Server) {
				cs.volume.(*core.Fake).Volume = &csi.Volume{
					VolumeId: "vg#pv-valid-0",
				}
				cs.volume.(*core.Fake).BaseDir = baseDir
				cs.volume.(*core.Fake).Err = errors.New("volume delete failed")
			},
			req: &csi.DeleteVolumeRequest{
				VolumeId: "vg#pv-valid-0",
			},
			wantErr: true,
			errCode: codes.Internal,
			expectMount: func(m *mounter.MockMounter) {
				m.EXPECT().CleanupStagingDir(gomock.Any(), "/dev/test/vg/pv-valid-0").Return(nil).Times(1)
			},
		},
		{
			name: "valid request on pv with annotation but wrong node",
			pre: func(cs *Server) {
				cs.volume.(*core.Fake).Volume = &csi.Volume{
					VolumeId: "vg#pv-wrong-node-0",
				}
				cs.volume.(*core.Fake).BaseDir = baseDir
			},
			req: &csi.DeleteVolumeRequest{
				VolumeId: "vg#pv-wrong-node-0",
			},
			errCode: codes.FailedPrecondition,
			wantErr: true,
		},
		{
			name: "valid request on pv with initial node param but wrong node",
			pre: func(cs *Server) {
				cs.volume.(*core.Fake).Volume = &csi.Volume{
					VolumeId: "vg#pv-wrong-node-1",
				}
				cs.volume.(*core.Fake).BaseDir = baseDir
			},
			req: &csi.DeleteVolumeRequest{
				VolumeId: "vg#pv-wrong-node-1",
			},
			errCode: codes.FailedPrecondition,
			wantErr: true,
		},
		{
			name: "valid request on pv with annotation but node not found",
			pre: func(cs *Server) {
				cs.volume.(*core.Fake).Volume = &csi.Volume{
					VolumeId: "vg#pv-node-not-found-0",
				}
				cs.volume.(*core.Fake).BaseDir = baseDir
			},
			req: &csi.DeleteVolumeRequest{
				VolumeId: "vg#pv-node-not-found-0",
			},
			want:    &csi.DeleteVolumeResponse{},
			wantErr: false,
		},
		{
			name: "valid request on pv with initial node param but node not found",
			pre: func(cs *Server) {
				cs.volume.(*core.Fake).Volume = &csi.Volume{
					VolumeId: "vg#pv-node-not-found-1",
				}
				cs.volume.(*core.Fake).BaseDir = baseDir
			},
			req: &csi.DeleteVolumeRequest{
				VolumeId: "vg#pv-node-not-found-1",
			},
			want:    &csi.DeleteVolumeResponse{},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		var tt = tt
		t.Run(tt.name, func(t *testing.T) {
			cs := initTestControllerServer(ctrl)

			if tt.pre != nil {
				tt.pre(cs)
			}

			if tt.expectMount != nil {
				tt.expectMount(cs.mounter.(*mounter.MockMounter))
			}

			resp, err := cs.DeleteVolume(context.Background(), tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("DeleteVolume() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				st, ok := status.FromError(err)
				if !ok {
					t.Errorf("DeleteVolume() error = %v, expected gRPC status error", err)
					return
				}
				if st.Code() != tt.errCode {
					t.Errorf("DeleteVolume() error code = %v, wantErrCode %v", st.Code(), tt.errCode)
				}
			}
			if !tt.wantErr && resp == nil {
				t.Errorf("DeleteVolume() response = %v, expected non-nil response", resp)
			}
			if tt.want != nil {
				if !reflect.DeepEqual(resp, tt.want) {
					t.Errorf("DeleteVolume() response = %v, want %v", resp, tt.want)
				}
			}
		})
	}
}

func TestServer_GetCapacity(t *testing.T) {
	ctrl := gomock.NewController(t)
	server := initTestControllerServer(ctrl)

	tests := []struct {
		name    string
		pre     func()
		req     *csi.GetCapacityRequest
		want    *csi.GetCapacityResponse
		wantErr bool
		errCode codes.Code
	}{
		{
			name: "valid request",
			pre: func() {
				server.volume.(*core.Fake).DiskPoolCapacity = 1000
			},
			req: &csi.GetCapacityRequest{},
			want: &csi.GetCapacityResponse{
				AvailableCapacity: 1000,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.pre != nil {
				tt.pre()
			}

			resp, err := server.GetCapacity(context.Background(), tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetCapacity() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				st, ok := status.FromError(err)
				if !ok {
					t.Errorf("GetCapacity() error = %v, expected gRPC status error", err)
					return
				}
				if st.Code() != tt.errCode {
					t.Errorf("GetCapacity() error code = %v, wantErrCode %v", st.Code(), tt.errCode)
				}
			}
			if !tt.wantErr && resp == nil {
				t.Errorf("GetCapacity() response = %v, expected non-nil response", resp)
			}
			if tt.want != nil {
				if !reflect.DeepEqual(resp, tt.want) {
					t.Errorf("GetCapacity() response = %v, want %v", resp, tt.want)
				}
			}
		})
	}
}
