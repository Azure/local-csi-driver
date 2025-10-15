// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package gc

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

func TestPVFailoverReconciler(t *testing.T) {
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
	mockLVM := NewMockLVMVolumeManager()
	mockLVM.AddVolume("containerstorage#test-volume") // Volume exists on this node

	controller := &PVFailoverReconciler{
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
	if len(mockLVM.DeletedLVs) != 1 {
		t.Errorf("Expected 1 volume to be deleted, got %d", len(mockLVM.DeletedLVs))
	} else if mockLVM.DeletedLVs[0] != "containerstorage#test-volume" {
		t.Errorf("Expected volume 'containerstorage#test-volume' to be deleted, got '%s'", mockLVM.DeletedLVs[0])
	}
}
