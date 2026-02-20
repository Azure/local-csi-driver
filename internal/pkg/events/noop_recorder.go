// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package events

import (
	"k8s.io/apimachinery/pkg/runtime"
	kevents "k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"
)

var _ kevents.EventRecorder = &NoopRecorder{}

// NoopRecorder is a no-op implementation of kevents.EventRecorder.
type NoopRecorder struct{}

// Eventf logs an event for the given object.
func (n *NoopRecorder) Eventf(regarding runtime.Object, related runtime.Object, eventtype, reason, action, note string, args ...interface{}) {
}

// WithLogger returns the same NoopRecorder as it doesn't use logging.
func (n *NoopRecorder) WithLogger(logger klog.Logger) kevents.EventRecorderLogger {
	return n
}

// NewNoopRecorder creates a new NoopEventRecorder.
func NewNoopRecorder() kevents.EventRecorder {
	return &NoopRecorder{}
}
