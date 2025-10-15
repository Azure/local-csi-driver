// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package gc

import (
	"local-csi-driver/internal/csi/core/lvm"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestHasNodeAnnotationMismatch(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

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
								"localdisk.csi.acstor.io/selected-initial-node": "node3",
							},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "mismatch - matching initial node annotation only",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
					Annotations: map[string]string{
						"localdisk.csi.acstor.io/selected-node": "node3",
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
			expected: true,
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
			result := hasNodeAnnotationMismatch(tt.pv, "node1", "localdisk.csi.acstor.io/selected-node", "localdisk.csi.acstor.io/selected-initial-node")
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
