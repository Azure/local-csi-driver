// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package gc

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"local-csi-driver/internal/csi/core/lvm"
	lvmMgr "local-csi-driver/internal/pkg/lvm"
)

func TestLVMOrphanScanner_shouldCleanupVolume(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	tests := []struct {
		name                  string
		volumeID              string
		existingPVs           []*corev1.PersistentVolume
		nodeID                string
		expectedShouldCleanup bool
		expectedReason        string
	}{
		{
			name:     "orphaned volume - no corresponding PV",
			volumeID: "containerstorage#orphaned-volume",
			existingPVs: []*corev1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "other-pv"},
					Spec: corev1.PersistentVolumeSpec{
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							CSI: &corev1.CSIPersistentVolumeSource{
								Driver:       lvm.DriverName,
								VolumeHandle: "containerstorage#other-volume",
							},
						},
					},
				},
			},
			nodeID:                "node1",
			expectedShouldCleanup: true,
			expectedReason:        "no PV with matching volume handle found",
		},
		{
			name:     "volume with matching PV and correct node",
			volumeID: "containerstorage#correct-volume",
			existingPVs: []*corev1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "correct-pv",
						Annotations: map[string]string{
							"localdisk.csi.acstor.io/selected-node": "node1",
						},
					},
					Spec: corev1.PersistentVolumeSpec{
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							CSI: &corev1.CSIPersistentVolumeSource{
								Driver:       lvm.DriverName,
								VolumeHandle: "containerstorage#correct-volume",
								VolumeAttributes: map[string]string{
									"localdisk.csi.acstor.io/selected-initial-node": "node1",
								},
							},
						},
					},
				},
			},
			nodeID:                "node1",
			expectedShouldCleanup: false,
			expectedReason:        "",
		},
		{
			name:     "volume with PV but wrong selected node annotation",
			volumeID: "containerstorage#mismatched-volume",
			existingPVs: []*corev1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "mismatched-pv",
						Annotations: map[string]string{
							"localdisk.csi.acstor.io/selected-node": "node2", // Different node
						},
					},
					Spec: corev1.PersistentVolumeSpec{
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							CSI: &corev1.CSIPersistentVolumeSource{
								Driver:       lvm.DriverName,
								VolumeHandle: "containerstorage#mismatched-volume",
							},
						},
					},
				},
			},
			nodeID:                "node1",
			expectedShouldCleanup: true,
			expectedReason:        "node annotation mismatch",
		},
		{
			name:     "volume with PV but wrong initial node parameter",
			volumeID: "containerstorage#initial-mismatch-volume",
			existingPVs: []*corev1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "initial-mismatch-pv"},
					Spec: corev1.PersistentVolumeSpec{
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							CSI: &corev1.CSIPersistentVolumeSource{
								Driver:       lvm.DriverName,
								VolumeHandle: "containerstorage#initial-mismatch-volume",
								VolumeAttributes: map[string]string{
									"localdisk.csi.acstor.io/selected-initial-node": "node2", // Different node
								},
							},
						},
					},
				},
			},
			nodeID:                "node1",
			expectedShouldCleanup: true,
			expectedReason:        "node annotation mismatch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objects := make([]client.Object, len(tt.existingPVs))
			for i, pv := range tt.existingPVs {
				objects[i] = pv
			}

			client := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objects...).
				WithIndex(&corev1.PersistentVolume{}, CSIVolumeHandleIndex, func(obj client.Object) []string {
					pv := obj.(*corev1.PersistentVolume)
					if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == lvm.DriverName {
						return []string{pv.Spec.CSI.VolumeHandle}
					}
					return nil
				}).
				Build()
			recorder := record.NewFakeRecorder(10)

			cleanup := &LVMOrphanScanner{
				Client:                   client,
				scheme:                   scheme,
				recorder:                 recorder,
				nodeID:                   tt.nodeID,
				selectedNodeAnnotation:   "localdisk.csi.acstor.io/selected-node",
				selectedInitialNodeParam: "localdisk.csi.acstor.io/selected-initial-node",
			}

			// Now we can test the real shouldCleanupVolume method with field indexing
			shouldCleanup, reason, err := cleanup.shouldCleanupVolume(context.Background(), tt.volumeID)

			if err != nil {
				t.Errorf("shouldCleanupVolume() unexpected error: %v", err)
			}

			if shouldCleanup != tt.expectedShouldCleanup {
				t.Errorf("shouldCleanupVolume() shouldCleanup = %v, want %v", shouldCleanup, tt.expectedShouldCleanup)
			}

			if reason != tt.expectedReason {
				t.Errorf("shouldCleanupVolume() reason = %v, want %v", reason, tt.expectedReason)
			}
		})
	}
}

func TestLVMOrphanScanner_Reconcile(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	// Create test PV that exists but has node mismatch
	testPV := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-volume",
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
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(testPV).
		WithIndex(&corev1.PersistentVolume{}, CSIVolumeHandleIndex, func(obj client.Object) []string {
			pv := obj.(*corev1.PersistentVolume)
			if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == lvm.DriverName {
				return []string{pv.Spec.CSI.VolumeHandle}
			}
			return nil
		}).
		Build()
	recorder := record.NewFakeRecorder(10)

	// Set up the fake LVM manager with test data
	fakeLVM := lvmMgr.NewFake()

	// Create volume group
	err := fakeLVM.CreateVolumeGroup(context.Background(), lvmMgr.CreateVGOptions{Name: "containerstorage"})
	if err != nil {
		t.Fatalf("Failed to create VG: %v", err)
	}

	// Create and configure the mock LVM manager
	testLVM := NewMockLVMVolumeManager()
	testLVM.Fake = fakeLVM

	// Create logical volumes through the wrapper to track VG associations
	_, err = testLVM.CreateLogicalVolume(context.Background(), lvmMgr.CreateLVOptions{
		VGName: "containerstorage",
		Name:   "test-volume",
		Size:   "1073741824B", // 1GB in bytes
	})
	if err != nil {
		t.Fatalf("Failed to create LV: %v", err)
	}
	_, err = testLVM.CreateLogicalVolume(context.Background(), lvmMgr.CreateLVOptions{
		VGName: "containerstorage",
		Name:   "orphaned-volume",
		Size:   "1073741824B", // 1GB in bytes
	})
	if err != nil {
		t.Fatalf("Failed to create LV: %v", err)
	}

	cleanup := &LVMOrphanScanner{
		Client:                   client,
		scheme:                   scheme,
		recorder:                 recorder,
		nodeID:                   "node1",
		selectedNodeAnnotation:   "localdisk.csi.acstor.io/selected-node",
		selectedInitialNodeParam: "localdisk.csi.acstor.io/selected-initial-node",
		lvmManager:               testLVM,
		reconcileInterval:        time.Minute,
	}

	ctx := context.Background()
	result, err := cleanup.Reconcile(ctx, ctrl.Request{})

	if err != nil {
		t.Errorf("Reconcile() unexpected error: %v", err)
	}

	if result.RequeueAfter != time.Minute {
		t.Errorf("Reconcile() expected requeue after %v, got %v", time.Minute, result.RequeueAfter)
	}

	// Verify that both volumes were deleted
	expectedDeleted := []string{
		"containerstorage#test-volume",     // Node mismatch
		"containerstorage#orphaned-volume", // No corresponding PV
	}

	if len(testLVM.DeletedLVs) != 2 {
		t.Errorf("Expected 2 volumes to be deleted, got %v", testLVM.DeletedLVs)
	}

	for _, expected := range expectedDeleted {
		found := false
		for _, deleted := range testLVM.DeletedLVs {
			if deleted == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected volume '%s' to be deleted, but it wasn't", expected)
		}
	}
}
