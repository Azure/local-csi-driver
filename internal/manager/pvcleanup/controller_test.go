package pvcleanup

// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"local-csi-driver/internal/csi/core/lvm"
)

// TestRegressionNodeReadErrorPreservesPV reconciles a released PV while a fake
// client returns a temporary Node lookup error, not NotFound.
// Bug: cleanup treats the failed read as a missing node and deletes the PV or
// removes its finalizers. Expected: return the read error and leave the PV alone.
func TestRegressionNodeReadErrorPreservesPV(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	for _, deleting := range []bool{false, true} {
		name := "before deletion"
		if deleting {
			name = "during finalizer removal"
		}
		t.Run(name, func(t *testing.T) {
			pv := corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "node-read-error",
					Finalizers: []string{PVProtectionFinalizer, ExternalProvisionerFinalizer},
				},
				Spec: corev1.PersistentVolumeSpec{
					PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver:       lvm.DriverName,
							VolumeHandle: "containerstorage#node-read-error",
						},
					},
					NodeAffinity: &corev1.VolumeNodeAffinity{
						Required: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{{
								MatchExpressions: []corev1.NodeSelectorRequirement{{
									Key:      lvm.TopologyKey,
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{"unknown-node"},
								}},
							}},
						},
					},
				},
				Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased},
			}
			if deleting {
				now := metav1.Now()
				pv.DeletionTimestamp = &now
			}
			readErr := errors.New("temporary node read failure")
			deleted, patched := false, false
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&pv).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if _, ok := obj.(*corev1.Node); ok {
							return readErr
						}
						return c.Get(ctx, key, obj, opts...)
					},
					Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
						deleted = true
						return nil
					},
					Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
						patched = true
						return nil
					},
				}).Build()
			reconciler := &PVCleanupReconciler{
				Client:   kubeClient,
				Recorder: kevents.NewFakeRecorder(10),
			}
			_, err := reconciler.Reconcile(t.Context(), ctrl.Request{
				NamespacedName: client.ObjectKey{Name: pv.Name},
			})
			if !errors.Is(err, readErr) {
				t.Errorf("Reconcile() error = %v, want node lookup error %v", err, readErr)
			}
			if deleted {
				t.Error("Reconcile() deleted the PV without establishing that its node is unavailable")
			}
			if patched {
				t.Error("Reconcile() patched PV finalizers without establishing that its node is unavailable")
			}
		})
	}
}

// Unit tests for helper functions

func TestExtractHostnamesFromPV(t *testing.T) {
	tests := []struct {
		name     string
		pv       *corev1.PersistentVolume
		expected []string
	}{
		{
			name: "PV with single hostname topology",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-1",
				},
				Spec: corev1.PersistentVolumeSpec{
					NodeAffinity: &corev1.VolumeNodeAffinity{
						Required: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{
											Key:      lvm.TopologyKey,
											Operator: corev1.NodeSelectorOpIn,
											Values:   []string{"node-1"},
										},
									},
								},
							},
						},
					},
				},
			},
			expected: []string{"node-1"},
		},
		{
			name: "PV with multiple hostname topology",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-2",
				},
				Spec: corev1.PersistentVolumeSpec{
					NodeAffinity: &corev1.VolumeNodeAffinity{
						Required: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{
											Key:      lvm.TopologyKey,
											Operator: corev1.NodeSelectorOpIn,
											Values:   []string{"node-1", "node-2"},
										},
									},
								},
							},
						},
					},
				},
			},
			expected: []string{"node-1", "node-2"},
		},
		{
			name: "PV with no node affinity",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-3",
				},
				Spec: corev1.PersistentVolumeSpec{},
			},
			expected: nil,
		},
		{
			name: "PV with wrong topology key",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-4",
				},
				Spec: corev1.PersistentVolumeSpec{
					NodeAffinity: &corev1.VolumeNodeAffinity{
						Required: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{
											Key:      "kubernetes.io/hostname",
											Operator: corev1.NodeSelectorOpIn,
											Values:   []string{"node-1"},
										},
									},
								},
							},
						},
					},
				},
			},
			expected: nil,
		},
		{
			name: "PV with wrong operator",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-5",
				},
				Spec: corev1.PersistentVolumeSpec{
					NodeAffinity: &corev1.VolumeNodeAffinity{
						Required: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{
											Key:      lvm.TopologyKey,
											Operator: corev1.NodeSelectorOpNotIn,
											Values:   []string{"node-1"},
										},
									},
								},
							},
						},
					},
				},
			},
			expected: nil,
		},
		{
			name: "PV with multiple terms, only one with correct topology",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-6",
				},
				Spec: corev1.PersistentVolumeSpec{
					NodeAffinity: &corev1.VolumeNodeAffinity{
						Required: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{
											Key:      "kubernetes.io/os",
											Operator: corev1.NodeSelectorOpIn,
											Values:   []string{"linux"},
										},
										{
											Key:      lvm.TopologyKey,
											Operator: corev1.NodeSelectorOpIn,
											Values:   []string{"node-3"},
										},
									},
								},
							},
						},
					},
				},
			},
			expected: []string{"node-3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractHostnamesFromPV(tt.pv)
			if len(result) != len(tt.expected) {
				t.Errorf("extractHostnamesFromPV() got %d hostnames, expected %d", len(result), len(tt.expected))
				return
			}
			for i, hostname := range result {
				if hostname != tt.expected[i] {
					t.Errorf("extractHostnamesFromPV() hostname[%d] = %s, expected %s", i, hostname, tt.expected[i])
				}
			}
		})
	}
}

func TestIsNodeReady(t *testing.T) {
	tests := []struct {
		name     string
		node     *corev1.Node
		expected bool
	}{
		{
			name: "Ready node",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-1",
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{
							Type:   corev1.NodeReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "Not ready node",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-2",
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{
							Type:   corev1.NodeReady,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "Node with unknown condition",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-3",
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{
							Type:   corev1.NodeReady,
							Status: corev1.ConditionUnknown,
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "Node without ready condition",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-4",
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{},
				},
			},
			expected: false,
		},
		{
			name: "Node with multiple conditions",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node-5",
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{
							Type:   corev1.NodeMemoryPressure,
							Status: corev1.ConditionFalse,
						},
						{
							Type:   corev1.NodeDiskPressure,
							Status: corev1.ConditionFalse,
						},
						{
							Type:   corev1.NodeReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isNodeReady(tt.node)
			if result != tt.expected {
				t.Errorf("isNodeReady() = %v, expected %v", result, tt.expected)
			}
		})
	}
}
