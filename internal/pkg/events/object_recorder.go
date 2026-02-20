// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package events

import (
	"k8s.io/apimachinery/pkg/runtime"
	kevents "k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"
)

// ObjectRecorder is an interface for a recorder that is bound to an object.
type ObjectRecorder interface {
	// Event logs an event for the bound object.
	Event(eventtype, reason, message string)

	// Eventf logs an event with a formatted message for the bound object.
	Eventf(eventtype, reason, messageFmt string, args ...any)
}

// defaultObjectRecorder is the standard implementation that records events to the bound object.
type defaultObjectRecorder struct {
	base kevents.EventRecorder
	obj  runtime.Object
}

func (b *defaultObjectRecorder) Event(eventtype, reason, message string) {
	// The new events API uses Eventf with different parameters:
	// Eventf(regarding, related, eventtype, reason, action, note, args...)
	// We map: reason -> action, message -> note
	b.base.Eventf(b.obj, nil, eventtype, reason, reason, message)
}

func (b *defaultObjectRecorder) Eventf(eventtype, reason, messageFmt string, args ...any) {
	// The new events API uses Eventf with different parameters:
	// Eventf(regarding, related, eventtype, reason, action, note, args...)
	// We map: reason -> action, messageFmt -> note
	b.base.Eventf(b.obj, nil, eventtype, reason, reason, messageFmt, args...)
}

// noopObjectRecorder is an implementation that doesn't actually record events.
type noopObjectRecorder struct{}

func (n *noopObjectRecorder) Event(eventtype, reason, message string) {}

func (n *noopObjectRecorder) Eventf(eventtype, reason, messageFmt string, args ...any) {}

// WithObject creates a new BoundRecorder for the given object.
func WithObject(base kevents.EventRecorder, obj runtime.Object) ObjectRecorder {
	if base == nil {
		klog.Warning("base recorder is nil, using no-op recorder")
		return NewNoopObjectRecorder()
	}
	if obj == nil {
		klog.Warning("object is nil, using no-op recorder")
		return NewNoopObjectRecorder()
	}
	return &defaultObjectRecorder{
		base: base,
		obj:  obj,
	}
}

// NewNoopObjectRecorder creates a no-op implementation of BoundRecorder.
func NewNoopObjectRecorder() ObjectRecorder {
	return &noopObjectRecorder{}
}
