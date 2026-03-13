// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"local-csi-driver/internal/pkg/block"
	"local-csi-driver/internal/pkg/probe"
)

const (
	// Startup diagnostic event reasons.
	noDiskAvailable       = "NoDiskAvailable"
	diskDiscoveryComplete = "DiskDiscoveryComplete"
)

// StartupDiagnostic runs a one-time disk availability check when the pod starts
// and emits a Kubernetes event on the pod with the results. It emits a Warning
// event if no disks are available, or a Normal event listing available and
// in-use disks.
type StartupDiagnostic struct {
	probe     probe.Interface
	block     block.Interface
	filter    *probe.Filter
	recorder  record.EventRecorder
	k8sClient client.Client
	podName   string
	namespace string
}

// NewStartupDiagnostic creates a new StartupDiagnostic instance.
func NewStartupDiagnostic(
	p probe.Interface,
	b block.Interface,
	filter *probe.Filter,
	recorder record.EventRecorder,
	k8sClient client.Client,
	podName string,
	namespace string,
) *StartupDiagnostic {
	return &StartupDiagnostic{
		probe:     p,
		block:     b,
		filter:    filter,
		recorder:  recorder,
		k8sClient: k8sClient,
		podName:   podName,
		namespace: namespace,
	}
}

// Start implements manager.Runnable. It performs the disk availability check
// once at startup and emits a Kubernetes event on the pod with the results.
func (s *StartupDiagnostic) Start(ctx context.Context) error {
	log := log.FromContext(ctx).WithName("startup-diagnostic")

	// Fetch the pod for event emission.
	var pod corev1.Pod
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: s.podName}, &pod); err != nil {
		log.Error(err, "failed to get pod for event emission", "pod", s.podName, "namespace", s.namespace)
		return nil
	}

	// Check if there are any available disks.
	devices, err := s.probe.ScanAvailableDevices(ctx)
	if err != nil && !errors.Is(err, probe.ErrNoDevicesFound) {
		log.Error(err, "failed to scan for available devices during startup diagnostic")
		return nil
	}

	// Scan all NVMe devices on the node to build a full picture.
	summary := s.scanNVMeDevices(ctx)

	if devices == nil || len(devices.Devices) == 0 {
		log.Info("no available disks found", "totalNVMeDisks", summary.total, "nonLVM2FormattedDisks", summary.nonLVM2Formatted)
		msg := buildNoDiskMessage(summary)
		s.recorder.Event(&pod, corev1.EventTypeWarning, noDiskAvailable, msg)
		return nil
	}

	log.Info("startup disk discovery complete", "availableDisks", len(devices.Devices))
	msg := buildDiskDiscoveryMessage(devices.Devices, summary)
	s.recorder.Event(&pod, corev1.EventTypeNormal, diskDiscoveryComplete, msg)
	return nil
}

// NeedLeaderElection returns false since the diagnostic should run on every node.
func (s *StartupDiagnostic) NeedLeaderElection() bool {
	return false
}

// deviceSummary holds the results of scanning all NVMe devices on the node.
type deviceSummary struct {
	total            int
	nonLVM2Formatted int
	available        []block.Device
	inUse            []block.Device
}

// scanNVMeDevices scans all block devices and categorizes NVMe devices
// matching the filter. Returns a summary with device lists.
func (s *StartupDiagnostic) scanNVMeDevices(ctx context.Context) deviceSummary {
	log := log.FromContext(ctx).WithName("startup-diagnostic")

	allDevices, err := s.block.GetDevices(ctx)
	if err != nil {
		log.Error(err, "failed to list all block devices for diagnostic context")
		return deviceSummary{}
	}

	var summary deviceSummary
	for _, d := range allDevices.Devices {
		if !s.filter.Match(d) {
			continue
		}
		summary.total++

		isFormatted, err := s.block.IsFormatted(d.Path)
		if err != nil {
			log.Error(err, "failed to check device format status", "device", d.Path)
			continue
		}
		if !isFormatted {
			log.V(1).Info("NVMe device found (unformatted)", "path", d.Path, "model", d.Model, "size", d.Size)
			summary.available = append(summary.available, d)
			continue
		}

		isLVM2, err := s.block.IsLVM2(d.Path)
		if err != nil {
			log.Error(err, "failed to check device LVM2 status", "device", d.Path)
			continue
		}
		if isLVM2 {
			log.V(1).Info("NVMe device found (LVM2, part of a volume group)", "path", d.Path, "model", d.Model, "size", d.Size)
			summary.available = append(summary.available, d)
			continue
		}

		summary.nonLVM2Formatted++
		log.V(1).Info("NVMe device found (formatted, non-LVM2)", "path", d.Path, "model", d.Model, "size", d.Size)
		summary.inUse = append(summary.inUse, d)
	}

	return summary
}

// formatDeviceList formats a list of devices into a human-readable string.
func formatDeviceList(devices []block.Device) string {
	names := make([]string, len(devices))
	for i, d := range devices {
		names[i] = fmt.Sprintf("%s (%s)", d.Path, formatBytes(d.Size))
	}
	return strings.Join(names, ", ")
}

// formatBytes formats bytes into a human-readable string.
func formatBytes(b int64) string {
	const gi = 1024 * 1024 * 1024
	if b >= gi {
		return fmt.Sprintf("%.1f GiB", float64(b)/float64(gi))
	}
	const mi = 1024 * 1024
	return fmt.Sprintf("%.1f MiB", float64(b)/float64(mi))
}

// buildDiskDiscoveryMessage constructs a Normal event message listing
// available and in-use devices.
func buildDiskDiscoveryMessage(available []block.Device, summary deviceSummary) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Disk discovery complete: found %d available disk(s) for volume group creation", len(available))

	sb.WriteString(". Available: ")
	sb.WriteString(formatDeviceList(available))

	if len(summary.inUse) > 0 {
		sb.WriteString(". In use (non-LVM formatted): ")
		sb.WriteString(formatDeviceList(summary.inUse))
	}

	return sb.String()
}

// buildNoDiskMessage constructs a Warning event message when no disks are
// available, with diagnostic context and remediation advice.
func buildNoDiskMessage(summary deviceSummary) string {
	if summary.total == 0 {
		return "No NVMe disks matching the expected model (Microsoft NVMe Direct Disk) " +
			"were found on this node. This can happen when the node pool uses a VM SKU " +
			"with ephemeral OS disk enabled, which consumes the NVMe disk for the OS. " +
			"Consider using a VM SKU with additional NVMe disks, or disable " +
			"ephemeral OS disk on the node pool."
	}

	if summary.nonLVM2Formatted == summary.total {
		return fmt.Sprintf(
			"No available disks for volume group creation. Found %d NVMe disk(s) "+
				"on this node, but all are already formatted with a non-LVM filesystem: %s. "+
				"Consider using a VM SKU with additional NVMe disks.",
			summary.total, formatDeviceList(summary.inUse),
		)
	}

	return fmt.Sprintf(
		"No available disks for volume group creation. Found %d NVMe disk(s) "+
			"on this node (%d formatted with a non-LVM filesystem, %d unformatted or already "+
			"in a volume group), but none are newly available for volume group creation.",
		summary.total, summary.nonLVM2Formatted, summary.total-summary.nonLVM2Formatted,
	)
}
