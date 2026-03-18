// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"local-csi-driver/internal/csi/core/lvm"
	"local-csi-driver/internal/pkg/block"
	"local-csi-driver/internal/pkg/probe"
)

const (
	expectedWarningPrefix = "Warning NoDiskAvailable"
	expectedNormalPrefix  = "Normal DiskDiscoveryComplete"
)

func newTestPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
	}
}

func newTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	return scheme
}

func TestStartupDiagnostic_DisksAvailable(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockProbe := probe.NewMock(ctrl)
	mockProbe.EXPECT().ScanAvailableDevices(gomock.Any()).Return(&block.DeviceList{
		Devices: []block.Device{
			{Path: "/dev/nvme1n1", Model: "Microsoft NVMe Direct Disk", Size: 500 * 1024 * 1024 * 1024, Type: "disk"},
		},
	}, nil)

	mockBlock := block.NewMock(ctrl)
	// scanNVMeDevices is always called to build the full picture.
	mockBlock.EXPECT().GetDevices(gomock.Any()).Return(&block.DeviceList{
		Devices: []block.Device{
			{Path: "/dev/nvme0n1", Model: "Microsoft NVMe Direct Disk", Size: 500 * 1024 * 1024 * 1024, Type: "disk"},
			{Path: "/dev/nvme1n1", Model: "Microsoft NVMe Direct Disk", Size: 500 * 1024 * 1024 * 1024, Type: "disk"},
			{Path: "/dev/sda", Model: "Some SCSI Disk", Size: 100 * 1024 * 1024 * 1024, Type: "disk"},
		},
	}, nil)
	mockBlock.EXPECT().IsFormatted("/dev/nvme0n1").Return(true, nil)
	mockBlock.EXPECT().IsLVM2("/dev/nvme0n1").Return(false, nil)
	mockBlock.EXPECT().IsFormatted("/dev/nvme1n1").Return(false, nil)

	recorder := kevents.NewFakeRecorder(10)
	k8sClient := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(newTestPod()).Build()

	diag := lvm.NewStartupDiagnostic(mockProbe, mockBlock, probe.EphemeralDiskFilter, recorder, k8sClient, "test-pod", "test-namespace")

	if err := diag.Start(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case event := <-recorder.Events:
		if !strings.HasPrefix(event, expectedNormalPrefix) {
			t.Fatalf("expected Normal DiskDiscoveryComplete event, got: %s", event)
		}
		if !strings.Contains(event, "/dev/nvme1n1") {
			t.Fatalf("expected event to list available disk /dev/nvme1n1, got: %s", event)
		}
		if !strings.Contains(event, "/dev/nvme0n1") {
			t.Fatalf("expected event to list in-use disk /dev/nvme0n1, got: %s", event)
		}
		if !strings.Contains(event, "In use") {
			t.Fatalf("expected event to mention 'In use', got: %s", event)
		}
	default:
		t.Fatal("expected a Normal event, got none")
	}
}

func TestStartupDiagnostic_DisksAvailable_NoInUse(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockProbe := probe.NewMock(ctrl)
	mockProbe.EXPECT().ScanAvailableDevices(gomock.Any()).Return(&block.DeviceList{
		Devices: []block.Device{
			{Path: "/dev/nvme0n1", Model: "Microsoft NVMe Direct Disk", Size: 500 * 1024 * 1024 * 1024, Type: "disk"},
		},
	}, nil)

	mockBlock := block.NewMock(ctrl)
	mockBlock.EXPECT().GetDevices(gomock.Any()).Return(&block.DeviceList{
		Devices: []block.Device{
			{Path: "/dev/nvme0n1", Model: "Microsoft NVMe Direct Disk", Size: 500 * 1024 * 1024 * 1024, Type: "disk"},
		},
	}, nil)
	mockBlock.EXPECT().IsFormatted("/dev/nvme0n1").Return(false, nil)

	recorder := kevents.NewFakeRecorder(10)
	k8sClient := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(newTestPod()).Build()

	diag := lvm.NewStartupDiagnostic(mockProbe, mockBlock, probe.EphemeralDiskFilter, recorder, k8sClient, "test-pod", "test-namespace")

	if err := diag.Start(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case event := <-recorder.Events:
		if !strings.HasPrefix(event, expectedNormalPrefix) {
			t.Fatalf("expected Normal event, got: %s", event)
		}
		if strings.Contains(event, "In use") {
			t.Fatalf("expected no 'In use' section when all disks are available, got: %s", event)
		}
	default:
		t.Fatal("expected a Normal event, got none")
	}
}

func TestStartupDiagnostic_NoDisks_AllFormattedNVMe(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockProbe := probe.NewMock(ctrl)
	mockProbe.EXPECT().ScanAvailableDevices(gomock.Any()).Return(nil, probe.ErrNoDevicesFound)

	mockBlock := block.NewMock(ctrl)
	mockBlock.EXPECT().GetDevices(gomock.Any()).Return(&block.DeviceList{
		Devices: []block.Device{
			{Path: "/dev/nvme0n1", Model: "Microsoft NVMe Direct Disk", Size: 500 * 1024 * 1024 * 1024, Type: "disk"},
			{Path: "/dev/sda", Model: "Some SCSI Disk", Size: 100 * 1024 * 1024 * 1024, Type: "disk"},
		},
	}, nil)
	mockBlock.EXPECT().IsFormatted("/dev/nvme0n1").Return(true, nil)
	mockBlock.EXPECT().IsLVM2("/dev/nvme0n1").Return(false, nil)

	recorder := kevents.NewFakeRecorder(10)
	k8sClient := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(newTestPod()).Build()

	diag := lvm.NewStartupDiagnostic(mockProbe, mockBlock, probe.EphemeralDiskFilter, recorder, k8sClient, "test-pod", "test-namespace")

	if err := diag.Start(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case event := <-recorder.Events:
		if !strings.HasPrefix(event, expectedWarningPrefix) {
			t.Fatalf("expected Warning event, got: %s", event)
		}
		if !strings.Contains(event, "1 NVMe disk(s)") {
			t.Fatalf("expected event to mention '1 NVMe disk(s)', got: %s", event)
		}
		if !strings.Contains(event, "non-LVM filesystem") {
			t.Fatalf("expected event to mention 'non-LVM filesystem', got: %s", event)
		}
		if !strings.Contains(event, "/dev/nvme0n1") {
			t.Fatalf("expected event to list the in-use disk, got: %s", event)
		}
	default:
		t.Fatal("expected a Warning event, got none")
	}
}

func TestStartupDiagnostic_NoDisks_NoNVMe(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockProbe := probe.NewMock(ctrl)
	mockProbe.EXPECT().ScanAvailableDevices(gomock.Any()).Return(nil, probe.ErrNoDevicesFound)

	mockBlock := block.NewMock(ctrl)
	mockBlock.EXPECT().GetDevices(gomock.Any()).Return(&block.DeviceList{
		Devices: []block.Device{
			{Path: "/dev/sda", Model: "Some SCSI Disk", Size: 100 * 1024 * 1024 * 1024, Type: "disk"},
		},
	}, nil)

	recorder := kevents.NewFakeRecorder(10)
	k8sClient := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(newTestPod()).Build()

	diag := lvm.NewStartupDiagnostic(mockProbe, mockBlock, probe.EphemeralDiskFilter, recorder, k8sClient, "test-pod", "test-namespace")

	if err := diag.Start(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case event := <-recorder.Events:
		if !strings.HasPrefix(event, expectedWarningPrefix) {
			t.Fatalf("expected Warning event, got: %s", event)
		}
		if !strings.Contains(event, "No NVMe disks matching the expected model") {
			t.Fatalf("expected ephemeral OS disk message, got: %s", event)
		}
		if !strings.Contains(event, "ephemeral OS disk") {
			t.Fatalf("expected event to mention 'ephemeral OS disk', got: %s", event)
		}
	default:
		t.Fatal("expected a Warning event, got none")
	}
}

func TestStartupDiagnostic_ScanError(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockProbe := probe.NewMock(ctrl)
	mockProbe.EXPECT().ScanAvailableDevices(gomock.Any()).Return(nil, fmt.Errorf("scan failed"))

	mockBlock := block.NewMock(ctrl)
	recorder := kevents.NewFakeRecorder(10)
	k8sClient := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(newTestPod()).Build()

	diag := lvm.NewStartupDiagnostic(mockProbe, mockBlock, probe.EphemeralDiskFilter, recorder, k8sClient, "test-pod", "test-namespace")

	if err := diag.Start(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// On scan error, the diagnostic should log and return without emitting events.
	select {
	case event := <-recorder.Events:
		t.Fatalf("expected no event on scan error, got: %s", event)
	default:
	}
}

func TestStartupDiagnostic_PodNotFound(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockProbe := probe.NewMock(ctrl)
	mockBlock := block.NewMock(ctrl)
	recorder := kevents.NewFakeRecorder(10)
	// No pod object — simulates pod not found.
	k8sClient := fake.NewClientBuilder().WithScheme(newTestScheme()).Build()

	diag := lvm.NewStartupDiagnostic(mockProbe, mockBlock, probe.EphemeralDiskFilter, recorder, k8sClient, "test-pod", "test-namespace")

	if err := diag.Start(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case event := <-recorder.Events:
		t.Fatalf("expected no event when pod not found, got: %s", event)
	default:
	}
}

func TestStartupDiagnostic_MultipleNVMe_SomeFormatted(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockProbe := probe.NewMock(ctrl)
	mockProbe.EXPECT().ScanAvailableDevices(gomock.Any()).Return(nil, probe.ErrNoDevicesFound)

	mockBlock := block.NewMock(ctrl)
	mockBlock.EXPECT().GetDevices(gomock.Any()).Return(&block.DeviceList{
		Devices: []block.Device{
			{Path: "/dev/nvme0n1", Model: "Microsoft NVMe Direct Disk", Size: 500 * 1024 * 1024 * 1024, Type: "disk"},
			{Path: "/dev/nvme1n1", Model: "Microsoft NVMe Direct Disk", Size: 500 * 1024 * 1024 * 1024, Type: "disk"},
		},
	}, nil)
	mockBlock.EXPECT().IsFormatted("/dev/nvme0n1").Return(true, nil)
	mockBlock.EXPECT().IsLVM2("/dev/nvme0n1").Return(false, nil)
	mockBlock.EXPECT().IsFormatted("/dev/nvme1n1").Return(false, nil)

	recorder := kevents.NewFakeRecorder(10)
	k8sClient := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(newTestPod()).Build()

	diag := lvm.NewStartupDiagnostic(mockProbe, mockBlock, probe.EphemeralDiskFilter, recorder, k8sClient, "test-pod", "test-namespace")

	if err := diag.Start(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case event := <-recorder.Events:
		if !strings.HasPrefix(event, expectedWarningPrefix) {
			t.Fatalf("expected Warning event, got: %s", event)
		}
		if !strings.Contains(event, "2 NVMe disk(s)") {
			t.Fatalf("expected event to mention '2 NVMe disk(s)', got: %s", event)
		}
		if !strings.Contains(event, "1 formatted with a non-LVM filesystem") {
			t.Fatalf("expected event to mention '1 formatted with a non-LVM filesystem', got: %s", event)
		}
	default:
		t.Fatal("expected a Warning event, got none")
	}
}

func TestStartupDiagnostic_NeedLeaderElection(t *testing.T) {
	t.Parallel()
	diag := lvm.NewStartupDiagnostic(nil, nil, nil, nil, nil, "", "")
	if diag.NeedLeaderElection() {
		t.Fatal("expected NeedLeaderElection to return false")
	}
}

func TestStartupDiagnostic_GetDevicesError(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockProbe := probe.NewMock(ctrl)
	mockProbe.EXPECT().ScanAvailableDevices(gomock.Any()).Return(nil, probe.ErrNoDevicesFound)

	mockBlock := block.NewMock(ctrl)
	mockBlock.EXPECT().GetDevices(gomock.Any()).Return(nil, fmt.Errorf("lsblk failed"))

	recorder := kevents.NewFakeRecorder(10)
	k8sClient := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(newTestPod()).Build()

	diag := lvm.NewStartupDiagnostic(mockProbe, mockBlock, probe.EphemeralDiskFilter, recorder, k8sClient, "test-pod", "test-namespace")

	if err := diag.Start(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should still emit a warning (with totalNVMe=0 fallback).
	select {
	case event := <-recorder.Events:
		if !strings.HasPrefix(event, expectedWarningPrefix) {
			t.Fatalf("expected Warning event, got: %s", event)
		}
	default:
		t.Fatal("expected a Warning event, got none")
	}
}

func TestStartupDiagnostic_IsFormattedError(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockProbe := probe.NewMock(ctrl)
	mockProbe.EXPECT().ScanAvailableDevices(gomock.Any()).Return(nil, probe.ErrNoDevicesFound)

	mockBlock := block.NewMock(ctrl)
	mockBlock.EXPECT().GetDevices(gomock.Any()).Return(&block.DeviceList{
		Devices: []block.Device{
			{Path: "/dev/nvme0n1", Model: "Microsoft NVMe Direct Disk", Size: 500 * 1024 * 1024 * 1024, Type: "disk"},
		},
	}, nil)
	mockBlock.EXPECT().IsFormatted("/dev/nvme0n1").Return(false, fmt.Errorf("blkid failed"))

	recorder := kevents.NewFakeRecorder(10)
	k8sClient := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(newTestPod()).Build()

	diag := lvm.NewStartupDiagnostic(mockProbe, mockBlock, probe.EphemeralDiskFilter, recorder, k8sClient, "test-pod", "test-namespace")

	if err := diag.Start(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case event := <-recorder.Events:
		if !strings.HasPrefix(event, expectedWarningPrefix) {
			t.Fatalf("expected Warning event, got: %s", event)
		}
	default:
		t.Fatal("expected a Warning event, got none")
	}
}

func TestStartupDiagnostic_IsLVM2Error(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockProbe := probe.NewMock(ctrl)
	mockProbe.EXPECT().ScanAvailableDevices(gomock.Any()).Return(nil, probe.ErrNoDevicesFound)

	mockBlock := block.NewMock(ctrl)
	mockBlock.EXPECT().GetDevices(gomock.Any()).Return(&block.DeviceList{
		Devices: []block.Device{
			{Path: "/dev/nvme0n1", Model: "Microsoft NVMe Direct Disk", Size: 500 * 1024 * 1024 * 1024, Type: "disk"},
		},
	}, nil)
	mockBlock.EXPECT().IsFormatted("/dev/nvme0n1").Return(true, nil)
	mockBlock.EXPECT().IsLVM2("/dev/nvme0n1").Return(false, fmt.Errorf("check failed"))

	recorder := kevents.NewFakeRecorder(10)
	k8sClient := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(newTestPod()).Build()

	diag := lvm.NewStartupDiagnostic(mockProbe, mockBlock, probe.EphemeralDiskFilter, recorder, k8sClient, "test-pod", "test-namespace")

	if err := diag.Start(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case event := <-recorder.Events:
		if !strings.HasPrefix(event, expectedWarningPrefix) {
			t.Fatalf("expected Warning event, got: %s", event)
		}
	default:
		t.Fatal("expected a Warning event, got none")
	}
}
