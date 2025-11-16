package pvcleanup

// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"local-csi-driver/internal/csi/core/lvm"
)

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

func TestHasFinalizer(t *testing.T) {
	tests := []struct {
		name      string
		pv        *corev1.PersistentVolume
		finalizer string
		expected  bool
	}{
		{
			name: "PV with PV protection finalizer",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-pv-1",
					Finalizers: []string{PVProtectionFinalizer},
				},
			},
			finalizer: PVProtectionFinalizer,
			expected:  true,
		},
		{
			name: "PV with external provisioner finalizer",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-pv-2",
					Finalizers: []string{ExternalProvisionerFinalizer},
				},
			},
			finalizer: ExternalProvisionerFinalizer,
			expected:  true,
		},
		{
			name: "PV with both finalizers",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-3",
					Finalizers: []string{
						PVProtectionFinalizer,
						ExternalProvisionerFinalizer,
					},
				},
			},
			finalizer: PVProtectionFinalizer,
			expected:  true,
		},
		{
			name: "PV without finalizer",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-pv-4",
					Finalizers: []string{},
				},
			},
			finalizer: PVProtectionFinalizer,
			expected:  false,
		},
		{
			name: "PV with different finalizer",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-pv-5",
					Finalizers: []string{"some-other-finalizer"},
				},
			},
			finalizer: PVProtectionFinalizer,
			expected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hasFinalizer(tt.pv, tt.finalizer)
			if result != tt.expected {
				t.Errorf("hasFinalizer() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestRemoveFinalizer(t *testing.T) {
	tests := []struct {
		name       string
		finalizers []string
		finalizer  string
		expected   []string
	}{
		{
			name:       "Remove single finalizer",
			finalizers: []string{PVProtectionFinalizer},
			finalizer:  PVProtectionFinalizer,
			expected:   []string{},
		},
		{
			name: "Remove one of multiple finalizers",
			finalizers: []string{
				PVProtectionFinalizer,
				ExternalProvisionerFinalizer,
			},
			finalizer: PVProtectionFinalizer,
			expected:  []string{ExternalProvisionerFinalizer},
		},
		{
			name:       "Remove non-existent finalizer",
			finalizers: []string{ExternalProvisionerFinalizer},
			finalizer:  PVProtectionFinalizer,
			expected:   []string{ExternalProvisionerFinalizer},
		},
		{
			name:       "Remove from empty list",
			finalizers: []string{},
			finalizer:  PVProtectionFinalizer,
			expected:   []string{},
		},
		{
			name: "Remove from list with multiple same finalizers",
			finalizers: []string{
				PVProtectionFinalizer,
				PVProtectionFinalizer,
				ExternalProvisionerFinalizer,
			},
			finalizer: PVProtectionFinalizer,
			expected:  []string{ExternalProvisionerFinalizer},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := removeFinalizer(tt.finalizers, tt.finalizer)
			if len(result) != len(tt.expected) {
				t.Errorf("removeFinalizer() returned %d finalizers, expected %d", len(result), len(tt.expected))
				return
			}
			for i, finalizer := range result {
				if finalizer != tt.expected[i] {
					t.Errorf("removeFinalizer() finalizer[%d] = %s, expected %s", i, finalizer, tt.expected[i])
				}
			}
		})
	}
}
