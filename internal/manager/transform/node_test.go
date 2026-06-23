// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package transform

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

var (
	node = &corev1.Node{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Node",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test-node",
			UID:             "12345678-1234-1234-1234-123456789012",
			ResourceVersion: "1",
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{
					Type:   corev1.NodeReady,
					Status: corev1.ConditionTrue,
				},
				{
					Type:   corev1.NodeMemoryPressure,
					Status: corev1.ConditionFalse,
				},
			},
		},
	}
)

func TestTrimNode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   any
		want    any
		wantErr bool
	}{
		{
			name:  "trim node",
			input: node,
			want: &corev1.Node{
				TypeMeta: node.TypeMeta,
				ObjectMeta: metav1.ObjectMeta{
					Name:            node.Name,
					UID:             node.UID,
					ResourceVersion: node.ResourceVersion,
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
			wantErr: false,
		},
		{
			name:    "trim non-node object",
			input:   "not a node",
			want:    "not a node",
			wantErr: false,
		},
		{
			name:    "trim nil object",
			input:   nil,
			want:    nil,
			wantErr: false,
		},
		{
			name:    "trim node with no ready condition",
			input:   &corev1.Node{},
			want:    &corev1.Node{},
			wantErr: false,
		},
		{
			name:    "trim node with no conditions",
			input:   &corev1.Node{Status: corev1.NodeStatus{}},
			want:    &corev1.Node{Status: corev1.NodeStatus{}},
			wantErr: false,
		},
		{
			name:    "pass through delete tombstone",
			input:   &cache.DeletedFinalStateUnknown{},
			want:    &cache.DeletedFinalStateUnknown{},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := TrimNode(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("TrimNode() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("TrimNode() = %v, want %v", got, tt.want)
			}
		})
	}
}
