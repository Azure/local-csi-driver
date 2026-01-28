// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package events

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
)

// contextKey is a private type for context keys to prevent collisions.
type contextKey struct{}

// recorderContextKey is the key used to store the recorder in the context.
var recorderContextKey = contextKey{}

// IntoContext stores a BoundRecorder in the context.
func IntoContext(ctx context.Context, recorder ObjectRecorder) context.Context {
	return context.WithValue(ctx, recorderContextKey, recorder)
}

// FromContext extracts a BoundRecorder from the context
// If no recorder is found, it returns a NoopBoundRecorder.
func FromContext(ctx context.Context) ObjectRecorder {
	if recorder, ok := ctx.Value(recorderContextKey).(ObjectRecorder); ok {
		return recorder
	}
	return NewNoopObjectRecorder()
}

// WithObjectIntoContext creates a bound recorder and stores it in context.
func WithObjectIntoContext(ctx context.Context, base events.EventRecorder, obj runtime.Object) context.Context {
	return IntoContext(ctx, WithObject(base, obj))
}
