// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package events

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"
)

var _ events.EventRecorder = &NoopRecorder{}

// NoopRecorder is a no-op implementation of events.EventRecorder.
type NoopRecorder struct{}

// Eventf logs an event for the given object.
func (n *NoopRecorder) Eventf(regarding runtime.Object, related runtime.Object, eventtype, reason, action, note string, args ...interface{}) {
}

// WithLogger returns the same NoopRecorder as it doesn't use logging.
func (n *NoopRecorder) WithLogger(logger klog.Logger) events.EventRecorderLogger {
	return n
}

// NewNoopRecorder creates a new NoopEventRecorder.
func NewNoopRecorder() events.EventRecorder {
	return &NoopRecorder{}
}
