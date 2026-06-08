// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package transform contains controller-runtime cache transform functions
// used by the manager to reduce memory usage for objects whose full
// payloads are not needed by reconcilers.
//
// Each transform must preserve the fields the informer machinery needs
// for cache bookkeeping (Name, Namespace, UID, ResourceVersion) and any
// fields that are written back to the apiserver via Patch/Update calls
// (notably Finalizers and DeletionTimestamp).
package transform

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TrimNode is a controller-runtime cache transform that strips Node
// objects down to only the fields the manager actually reads (the Ready
// condition). This keeps memory usage proportional to the number of
// nodes rather than the size of each Node object, which can be tens of
// KB once labels, annotations, addresses, images, and managed fields
// are included.
//
// The informer machinery requires Name, UID, and ResourceVersion to
// remain intact for cache bookkeeping.
func TrimNode(obj any) (any, error) {
	node, ok := obj.(*corev1.Node)
	if !ok {
		// Pass through tombstones and anything else unchanged.
		return obj, nil
	}
	var ready []corev1.NodeCondition
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			ready = []corev1.NodeCondition{c}
			break
		}
	}
	return &corev1.Node{
		TypeMeta: node.TypeMeta,
		ObjectMeta: metav1.ObjectMeta{
			Name:            node.Name,
			Namespace:       node.Namespace,
			UID:             node.UID,
			ResourceVersion: node.ResourceVersion,
		},
		Status: corev1.NodeStatus{
			Conditions: ready,
		},
	}, nil
}
