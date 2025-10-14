// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"local-csi-driver/internal/csi/core/lvm"
)

// mockLVMVolumeManager is a mock implementation for testing
type mockLVMVolumeManager struct {
	volumes map[string]bool // tracks which volumes exist
	deleted []string        // tracks which volumes were deleted
}

func (m *mockLVMVolumeManager) DeleteVolume(ctx context.Context, volumeID string) error {
	m.deleted = append(m.deleted, volumeID)
	delete(m.volumes, volumeID)
	return nil
}

func (m *mockLVMVolumeManager) GetVolumeName(volumeID string) (string, error) {
	// Extract volume name from volume ID for testing
	_, lvName, err := parseVolumeID(volumeID)
	if err != nil {
		return "", err
	}
	return lvName, nil
}

func (m *mockLVMVolumeManager) GetNodeDevicePath(volumeID string) (string, error) {
	if m.volumes[volumeID] {
		return "/dev/containerstorage/test-volume", nil
	}
	return "", nil
}

func TestPVGarbageCollector_hasNodeAnnotationMismatch(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	recorder := record.NewFakeRecorder(10)
	mockLVM := &mockLVMVolumeManager{
		volumes: make(map[string]bool),
		deleted: make([]string, 0),
	}

	controller := &PVGarbageCollector{
		Client:                   fake.NewClientBuilder().WithScheme(scheme).Build(),
		scheme:                   scheme,
		recorder:                 recorder,
		nodeID:                   "node1",
		selectedNodeAnnotation:   "localdisk.csi.acstor.io/selected-node",
		selectedInitialNodeParam: "localdisk.csi.acstor.io/selected-initial-node",
		lvmManager:               mockLVM,
	}

	tests := []struct {
		name     string
		pv       *corev1.PersistentVolume
		expected bool
	}{
		{
			name: "no mismatch - matching selected node annotation",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
					Annotations: map[string]string{
						"localdisk.csi.acstor.io/selected-node": "node1",
					},
				},
				Spec: corev1.PersistentVolumeSpec{
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver: lvm.DriverName,
							VolumeAttributes: map[string]string{
								"localdisk.csi.acstor.io/selected-initial-node": "node1",
							},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "mismatch - different selected node annotation",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
					Annotations: map[string]string{
						"localdisk.csi.acstor.io/selected-node": "node2",
					},
				},
				Spec: corev1.PersistentVolumeSpec{
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver: lvm.DriverName,
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "mismatch - different initial node param",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
				},
				Spec: corev1.PersistentVolumeSpec{
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver: lvm.DriverName,
							VolumeAttributes: map[string]string{
								"localdisk.csi.acstor.io/selected-initial-node": "node2",
							},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "no annotations - no mismatch",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
				},
				Spec: corev1.PersistentVolumeSpec{
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver: lvm.DriverName,
						},
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := controller.hasNodeAnnotationMismatch(tt.pv)
			if result != tt.expected {
				t.Errorf("hasNodeAnnotationMismatch() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestParseVolumeID(t *testing.T) {
	tests := []struct {
		name      string
		volumeID  string
		expectVG  string
		expectLV  string
		expectErr bool
	}{
		{
			name:      "valid volume ID",
			volumeID:  "containerstorage#test-volume",
			expectVG:  "containerstorage",
			expectLV:  "test-volume",
			expectErr: false,
		},
		{
			name:      "invalid volume ID - no separator",
			volumeID:  "containerstorage-test-volume",
			expectErr: true,
		},
		{
			name:      "invalid volume ID - empty VG",
			volumeID:  "#test-volume",
			expectErr: true,
		},
		{
			name:      "invalid volume ID - empty LV",
			volumeID:  "containerstorage#",
			expectErr: true,
		},
		{
			name:      "invalid volume ID - too many segments",
			volumeID:  "containerstorage#test#volume",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vg, lv, err := parseVolumeID(tt.volumeID)
			if tt.expectErr {
				if err == nil {
					t.Errorf("parseVolumeID() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("parseVolumeID() unexpected error: %v", err)
				return
			}
			if vg != tt.expectVG {
				t.Errorf("parseVolumeID() VG = %v, want %v", vg, tt.expectVG)
			}
			if lv != tt.expectLV {
				t.Errorf("parseVolumeID() LV = %v, want %v", lv, tt.expectLV)
			}
		})
	}
}

func TestPVGarbageCollector_Reconcile(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	// Test PV with node annotation mismatch
	testPV := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pv",
			Annotations: map[string]string{
				"localdisk.csi.acstor.io/selected-node": "node2", // Different from controller's nodeID
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       lvm.DriverName,
					VolumeHandle: "containerstorage#test-volume",
				},
			},
		},
		Status: corev1.PersistentVolumeStatus{
			Phase: corev1.VolumeAvailable,
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(testPV).Build()
	recorder := record.NewFakeRecorder(10)
	mockLVM := &mockLVMVolumeManager{
		volumes: map[string]bool{
			"containerstorage#test-volume": true, // Volume exists on this node
		},
		deleted: make([]string, 0),
	}

	controller := &PVGarbageCollector{
		Client:                   client,
		scheme:                   scheme,
		recorder:                 recorder,
		nodeID:                   "node1",
		selectedNodeAnnotation:   "localdisk.csi.acstor.io/selected-node",
		selectedInitialNodeParam: "localdisk.csi.acstor.io/selected-initial-node",
		lvmManager:               mockLVM,
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name: testPV.Name,
		},
	}

	ctx := context.Background()
	result, err := controller.Reconcile(ctx, req)

	if err != nil {
		t.Errorf("Reconcile() unexpected error: %v", err)
	}

	if result.Requeue {
		t.Errorf("Reconcile() should not requeue on success")
	}

	// Verify the volume was deleted
	if len(mockLVM.deleted) != 1 {
		t.Errorf("Expected 1 volume to be deleted, got %d", len(mockLVM.deleted))
	} else if mockLVM.deleted[0] != "containerstorage#test-volume" {
		t.Errorf("Expected volume 'containerstorage#test-volume' to be deleted, got '%s'", mockLVM.deleted[0])
	}
}
