// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package csi

import (
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"local-csi-driver/internal/csi/capability"
	"local-csi-driver/internal/csi/core"
	"local-csi-driver/internal/pkg/events"
	"local-csi-driver/internal/pkg/telemetry"
)

func TestNewCombinedDriver(t *testing.T) {
	fakeVolumeClient := core.NewFake()
	fakeClient := fake.NewClientBuilder().
		WithScheme(runtime.NewScheme()).
		Build()
	recorder := events.NewNoopRecorder()
	tracerProvider := telemetry.NewNoopTracerProvider()

	driver := NewCombined("node1", fakeVolumeClient, fakeClient, false, recorder, tracerProvider)

	if driver == nil {
		t.Fatalf("Expected driver to be non-nil")
	}
	if driver.is == nil {
		t.Error("Expected identity server to be non-nil")
	}
	if driver.cs == nil {
		t.Error("Expected controller server to be non-nil")
	}
	if driver.ns == nil {
		t.Error("Expected node server to be non-nil")
	}
	if driver.volume != fakeVolumeClient {
		t.Error("Expected volume client to be set correctly")
	}
	if driver.tp != tracerProvider {
		t.Error("Expected tracer provider to be set correctly")
	}
}

func TestNewControllerDriver(t *testing.T) {
	fakeVolumeClient := core.NewFake()
	fakeClient := fake.NewClientBuilder().
		WithScheme(runtime.NewScheme()).
		Build()
	recorder := events.NewNoopRecorder()
	tracerProvider := telemetry.NewNoopTracerProvider()

	driver := NewController(fakeVolumeClient, fakeClient, false, recorder, tracerProvider)

	if driver == nil {
		t.Fatalf("Expected driver to be non-nil")
	}
	if driver.is == nil {
		t.Error("Expected identity server to be non-nil")
	}
	if driver.cs == nil {
		t.Error("Expected controller server to be non-nil")
	}
	if driver.ns != nil {
		t.Error("Expected node server to be nil")
	}
	if driver.volume != fakeVolumeClient {
		t.Error("Expected volume client to be set correctly")
	}
	if driver.tp != tracerProvider {
		t.Error("Expected tracer provider to be set correctly")
	}
}

func TestNewNodeDriver(t *testing.T) {
	fakeVolumeClient := core.NewFake()
	fakeClient := fake.NewClientBuilder().
		WithScheme(runtime.NewScheme()).
		Build()
	recorder := events.NewNoopRecorder()
	tracerProvider := telemetry.NewNoopTracerProvider()

	driver := NewNode("node1", fakeVolumeClient, fakeClient, false, recorder, tracerProvider)

	if driver == nil {
		t.Fatalf("Expected driver to be non-nil")
	}
	if driver.is == nil {
		t.Error("Expected identity server to be non-nil")
	}
	if driver.cs != nil {
		t.Error("Expected controller server to be nil")
	}
	if driver.ns == nil {
		t.Error("Expected node server to be non-nil")
	}
	if driver.volume != fakeVolumeClient {
		t.Error("Expected volume client to be set correctly")
	}
	if driver.tp != tracerProvider {
		t.Error("Expected tracer provider to be set correctly")
	}
}

func TestDriver_GetIdentityServer(t *testing.T) {
	fakeVolumeClient := core.NewFake()
	fakeClient := fake.NewClientBuilder().
		WithScheme(runtime.NewScheme()).
		Build()
	recorder := events.NewNoopRecorder()
	tracerProvider := telemetry.NewNoopTracerProvider()

	driver := NewCombined("node1", fakeVolumeClient, fakeClient, false, recorder, tracerProvider)

	identityServer := driver.GetIdentityServer()
	if identityServer == nil {
		t.Error("Expected identity server to be non-nil")
	}
}

func TestDriver_GetControllerServer(t *testing.T) {
	fakeVolumeClient := core.NewFake()
	fakeClient := fake.NewClientBuilder().
		WithScheme(runtime.NewScheme()).
		Build()
	recorder := events.NewNoopRecorder()
	tracerProvider := telemetry.NewNoopTracerProvider()

	driver := NewCombined("node1", fakeVolumeClient, fakeClient, false, recorder, tracerProvider)

	controllerServer := driver.GetControllerServer()
	if controllerServer == nil {
		t.Error("Expected controller server to be non-nil")
	}
}

func TestDriver_GetNodeServer(t *testing.T) {
	fakeVolumeClient := core.NewFake()
	fakeClient := fake.NewClientBuilder().
		WithScheme(runtime.NewScheme()).
		Build()
	recorder := events.NewNoopRecorder()
	tracerProvider := telemetry.NewNoopTracerProvider()

	driver := NewCombined("node1", fakeVolumeClient, fakeClient, false, recorder, tracerProvider)

	nodeServer := driver.GetNodeServer()
	if nodeServer == nil {
		t.Error("Expected node server to be non-nil")
	}
}

func TestNewControllerServiceCapability(t *testing.T) {
	capType := csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME

	capability := capability.NewControllerServiceCapability(capType)

	if capability == nil {
		t.Error("Expected capability to be non-nil")
	}
	if capability.GetRpc().GetType() != capType {
		t.Errorf("Expected capability type to be %v, got %v", capType, capability.GetRpc().GetType())
	}
}
