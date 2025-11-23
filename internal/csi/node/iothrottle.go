// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package node

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

// IOThrottleParams represents the IO throttling parameters
type IOThrottleParams struct {
	RBPS  *int64 // Read bytes per second
	WBPS  *int64 // Write bytes per second
	RIOPS *int64 // Read IOPS per second
	WIOPS *int64 // Write IOPS per second
}

// ThrottlingUpdater is an interface for updating running pod throttling settings
// This allows the controller to trigger updates to running pods
// ThrottlingUpdater interface for updating running pods' IO throttling
// The implementation uses provided parameters when available, nil signals to clear throttling
type ThrottlingUpdater interface {
	// UpdateRunningPodsThrottling updates running pods' throttling with provided params
	// If throttleParams is nil, it signals that throttling should be cleared
	UpdateRunningPodsThrottling(volumeID string, throttleParams *IOThrottleParams) error
}

// extractThrottlingParamsFromVolumeContext extracts throttling parameters from volume context
func extractThrottlingParamsFromVolumeContext(volumeContext map[string]string) *IOThrottleParams {
	if len(volumeContext) == 0 {
		return nil
	}

	// Debug logging to see all available keys in volume context
	ctx := context.Background()
	logger := log.FromContext(ctx).WithName("extractThrottlingParams")
	logger.V(4).Info("Extracting throttling parameters from volume context", "volumeContextKeys", getVolumeContextKeys(volumeContext))

	throttleParams := &IOThrottleParams{}
	hasParams := false

	// Extract rbps (read bytes per second)
	if rbpsStr, exists := volumeContext["csi.storage.k8s.io/throttle.rbps"]; exists {
		if rbps, err := strconv.ParseInt(rbpsStr, 10, 64); err == nil {
			throttleParams.RBPS = &rbps
			hasParams = true
		}
	}

	// Extract wbps (write bytes per second)
	if wbpsStr, exists := volumeContext["csi.storage.k8s.io/throttle.wbps"]; exists {
		if wbps, err := strconv.ParseInt(wbpsStr, 10, 64); err == nil {
			throttleParams.WBPS = &wbps
			hasParams = true
		}
	}

	// Extract riops (read IOPS per second)
	if riopsStr, exists := volumeContext["csi.storage.k8s.io/throttle.riops"]; exists {
		if riops, err := strconv.ParseInt(riopsStr, 10, 64); err == nil {
			throttleParams.RIOPS = &riops
			hasParams = true
		}
	}

	// Extract wiops (write IOPS per second)
	if wiopsStr, exists := volumeContext["csi.storage.k8s.io/throttle.wiops"]; exists {
		if wiops, err := strconv.ParseInt(wiopsStr, 10, 64); err == nil {
			throttleParams.WIOPS = &wiops
			hasParams = true
		}
	}

	if !hasParams {
		return nil
	}

	return throttleParams
}

// getVolumeContextKeys returns the keys from volume context for logging
func getVolumeContextKeys(volumeContext map[string]string) []string {
	keys := make([]string, 0, len(volumeContext))
	for key := range volumeContext {
		keys = append(keys, key)
	}
	return keys
}

// findDeviceForMountPath finds the underlying block device for a given mount path
func (ns *Server) findDeviceForMountPath(mountPath string) (string, error) {
	// Read /proc/mounts to find the device for the mount point
	content, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return "", fmt.Errorf("failed to read /proc/mounts: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == mountPath {
			// Found the mount point, return the device
			return fields[0], nil
		}
	}

	return "", fmt.Errorf("device not found for mount path %s", mountPath)
}

// findPodCgroupFromTargetPath finds the pod's cgroup path from the target mount path
func (ns *Server) findPodCgroupFromTargetPath(targetPath string) (string, error) {
	// The target path typically looks like:
	// /var/lib/kubelet/pods/{pod-uid}/volumes/kubernetes.io~csi/{volume-name}/mount
	// We need to extract the pod UID and construct the cgroup path

	// Extract pod UID from target path
	podUID, err := extractPodUIDFromPath(targetPath)
	if err != nil {
		return "", fmt.Errorf("failed to extract pod UID from path %s: %w", targetPath, err)
	}

	// Find the cgroup path for this pod
	cgroupPath, err := findCgroupPathForPodUID(podUID)
	if err != nil {
		return "", fmt.Errorf("failed to find cgroup path for pod UID %s: %w", podUID, err)
	}

	return cgroupPath, nil
}

// extractPodUIDFromPath extracts the pod UID from a Kubernetes volume target path
func extractPodUIDFromPath(targetPath string) (string, error) {
	// Target path format: /var/lib/kubelet/pods/{pod-uid}/volumes/...
	parts := strings.Split(targetPath, "/")

	for i, part := range parts {
		if part == "pods" && i+1 < len(parts) {
			podUID := parts[i+1]
			// Basic validation - pod UIDs are typically UUID format
			if len(podUID) > 0 && strings.Contains(podUID, "-") {
				return podUID, nil
			}
		}
	}

	return "", fmt.Errorf("pod UID not found in path %s", targetPath)
}

// findCgroupPathForPodUID finds the cgroup path for a given pod UID
func findCgroupPathForPodUID(podUID string) (string, error) {
	// Look for the pod's cgroup in the cgroup hierarchy
	// Common paths include:
	// /sys/fs/cgroup/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod{pod-uid}.slice
	// /sys/fs/cgroup/kubepods/burstable/pod{pod-uid}

	// Normalize pod UID (remove dashes for cgroup naming)
	normalizedUID := strings.ReplaceAll(podUID, "-", "_")

	// Common cgroup patterns to search
	patterns := []string{
		fmt.Sprintf("kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod%s.slice", normalizedUID),
		fmt.Sprintf("kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-pod%s.slice", normalizedUID),
		fmt.Sprintf("kubepods.slice/kubepods-guaranteed.slice/kubepods-guaranteed-pod%s.slice", normalizedUID),
		fmt.Sprintf("kubepods/burstable/pod%s", podUID),
		fmt.Sprintf("kubepods/besteffort/pod%s", podUID),
		fmt.Sprintf("kubepods/guaranteed/pod%s", podUID),
	}

	for _, pattern := range patterns {
		cgroupPath := filepath.Join("/sys/fs/cgroup", pattern)
		if _, err := os.Stat(cgroupPath); err == nil {
			// Return relative path from /sys/fs/cgroup
			return pattern, nil
		}
	}

	return "", fmt.Errorf("cgroup path not found for pod UID %s", podUID)
}

// configurePodCgroupIOMax configures cgroup v2 io.max for the pod with throttling parameters
func (ns *Server) configurePodCgroupIOMax(devicePath string, params *IOThrottleParams, cgroupPath string) error {
	ctx := context.Background()
	logger := log.FromContext(ctx).WithName("configurePodCgroupIOMax")

	if params == nil {
		return nil // No throttling to configure
	}

	// Get device major:minor numbers
	deviceNumbers, err := getDeviceMajorMinor(devicePath)
	if err != nil {
		return fmt.Errorf("failed to get device numbers: %w", err)
	}

	// Construct the full cgroup io.max path
	ioMaxPath := filepath.Join("/sys/fs/cgroup", cgroupPath, "io.max")

	logger.V(4).Info("Configuring cgroup io.max", "ioMaxPath", ioMaxPath, "deviceNumbers", deviceNumbers)

	// Build the io.max configuration string
	// Format: "major:minor rbps=value wbps=value riops=value wiops=value"
	var ioMaxConfig strings.Builder
	ioMaxConfig.WriteString(deviceNumbers)

	if params.RBPS != nil {
		ioMaxConfig.WriteString(fmt.Sprintf(" rbps=%d", *params.RBPS))
	}
	if params.WBPS != nil {
		ioMaxConfig.WriteString(fmt.Sprintf(" wbps=%d", *params.WBPS))
	}
	if params.RIOPS != nil {
		ioMaxConfig.WriteString(fmt.Sprintf(" riops=%d", *params.RIOPS))
	}
	if params.WIOPS != nil {
		ioMaxConfig.WriteString(fmt.Sprintf(" wiops=%d", *params.WIOPS))
	}

	// Write to io.max file
	configStr := ioMaxConfig.String()
	logger.V(4).Info("Writing io.max configuration", "ioMaxPath", ioMaxPath, "config", configStr)
	err = os.WriteFile(ioMaxPath, []byte(configStr), 0644)
	if err != nil {
		return fmt.Errorf("failed to write io.max config '%s' to %s: %w", configStr, ioMaxPath, err)
	}

	return nil
}

// getDeviceMajorMinor gets the major:minor device numbers for a device path
func getDeviceMajorMinor(devicePath string) (string, error) {
	ctx := context.Background()
	logger := log.FromContext(ctx).WithName("getDeviceMajorMinor")
	logger.V(4).Info("Getting device major:minor numbers", "devicePath", devicePath)

	// Resolve any symlinks to get the actual device
	realPath, err := filepath.EvalSymlinks(devicePath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve device path %s: %w", devicePath, err)
	}

	logger.V(4).Info("Resolved device path", "originalPath", devicePath, "realPath", realPath)

	// For device-mapper devices (like LVM), we need to get the major:minor directly from the device file
	if strings.Contains(realPath, "/dev/mapper/") || strings.Contains(realPath, "/dev/dm-") {
		return getDeviceMajorMinorFromStat(realPath)
	}

	// Extract device name from path for regular block devices
	deviceName := filepath.Base(realPath)
	logger.V(4).Info("Extracted device name", "deviceName", deviceName)

	// Get major:minor numbers from /proc/partitions
	return getDeviceNumbers(deviceName)
}

// getDeviceNumbers gets the major:minor numbers for a device from /proc/partitions
func getDeviceNumbers(deviceName string) (string, error) {
	// Read from /proc/partitions to get major:minor for the device
	procPath := "/proc/partitions"
	content, err := os.ReadFile(procPath)
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %w", procPath, err)
	}

	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[3] == deviceName {
			major := fields[0]
			minor := fields[1]
			return fmt.Sprintf("%s:%s", major, minor), nil
		}
	}

	return "", fmt.Errorf("device %s not found in /proc/partitions", deviceName)
}

// getDeviceMajorMinorFromStat gets the major:minor numbers for a device using stat
func getDeviceMajorMinorFromStat(devicePath string) (string, error) {
	ctx := context.Background()
	logger := log.FromContext(ctx).WithName("getDeviceMajorMinorFromStat")
	logger.V(3).Info("Getting device major:minor from stat", "devicePath", devicePath)

	// Get file info for the device
	fileInfo, err := os.Stat(devicePath)
	if err != nil {
		return "", fmt.Errorf("failed to stat device %s: %w", devicePath, err)
	}

	// Check if it's a device file
	if fileInfo.Mode()&os.ModeDevice == 0 {
		return "", fmt.Errorf("%s is not a device file", devicePath)
	}

	// Get the device major:minor from the file system
	// This requires using syscall to get the device numbers
	stat, ok := fileInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("failed to get syscall stat for device %s", devicePath)
	}

	// Extract major and minor numbers from the rdev field
	major := (stat.Rdev >> 8) & 0xff
	minor := stat.Rdev & 0xff

	majorMinor := fmt.Sprintf("%d:%d", major, minor)
	logger.V(3).Info("Device major:minor resolved", "devicePath", devicePath, "majorMinor", majorMinor)

	return majorMinor, nil
}

// UpdateRunningPodsThrottling updates the IO throttling parameters for all running pods using a specific volume
// When throttleParams is nil, it signals that throttling should be cleared (no fallback to VAC)
// When throttleParams is provided, it uses those parameters to avoid redundant VAC reads
func (ns *Server) UpdateRunningPodsThrottling(volumeID string, throttleParams *IOThrottleParams) error {
	ctx := context.Background()
	log := log.FromContext(ctx).WithName("UpdateRunningPodsThrottling")
	log.V(2).Info("UpdateRunningPodsThrottling called", "volumeID", volumeID)

	var currentThrottleParams *IOThrottleParams

	// If throttleParams is nil, treat it as a signal to clear throttling
	if throttleParams == nil {
		currentThrottleParams = nil
		log.V(2).Info("Clear throttling signal received (nil params)")
	} else {
		// Use provided parameters (from ControllerModifyVolume with validated VAC params)
		currentThrottleParams = throttleParams
		log.V(2).Info("Using provided throttling params", "throttleParams", currentThrottleParams)
	} // Find all active volume mounts for this volume ID
	activeMounts, err := ns.findActiveVolumeMounts(volumeID)
	if err != nil {
		return fmt.Errorf("failed to find active mounts for volume %s: %w", volumeID, err)
	}

	if len(activeMounts) == 0 {
		log.V(2).Info("No active mounts found", "volumeID", volumeID)
		return nil
	}

	log.V(2).Info("Found active mounts", "volumeID", volumeID, "mountCount", len(activeMounts))

	// Update or clear throttling for each active mount based on VAC parameters
	var updateErrors []error
	for _, mountInfo := range activeMounts {
		var err error
		if currentThrottleParams == nil {
			// Clear throttling when no throttling params found in VAC (including no-throttle VAC)
			log.V(3).Info("Clearing throttling for mount",
				"mountPath", mountInfo.MountPath,
				"devicePath", mountInfo.DevicePath,
				"cgroupPath", mountInfo.CgroupPath)
			err = ns.clearPodCgroupIOMax(mountInfo.DevicePath, mountInfo.CgroupPath)
		} else {
			// Set throttling parameters from VAC
			log.V(3).Info("Updating throttling for mount with VAC params",
				"mountPath", mountInfo.MountPath,
				"devicePath", mountInfo.DevicePath,
				"cgroupPath", mountInfo.CgroupPath)
			err = ns.configurePodCgroupIOMax(mountInfo.DevicePath, currentThrottleParams, mountInfo.CgroupPath)
		}

		if err != nil {
			operation := "update"
			if currentThrottleParams == nil {
				operation = "clear"
			}
			updateErrors = append(updateErrors, fmt.Errorf("failed to %s throttling for mount %s: %w", operation, mountInfo.MountPath, err))
		} else {
			operation := "updated"
			if currentThrottleParams == nil {
				operation = "cleared"
			}
			log.V(3).Info("Successfully processed throttling for mount",
				"operation", operation,
				"mountPath", mountInfo.MountPath)
		}
	}

	if len(updateErrors) > 0 {
		return fmt.Errorf("failed to update some mounts: %v", updateErrors)
	}

	return nil
}

// clearPodCgroupIOMax clears all IO throttling limits for the pod's cgroup
func (ns *Server) clearPodCgroupIOMax(devicePath string, cgroupPath string) error {
	ctx := context.Background()
	logger := log.FromContext(ctx).WithName("clearPodCgroupIOMax")

	// Get device major:minor numbers
	deviceNumbers, err := getDeviceMajorMinor(devicePath)
	if err != nil {
		return fmt.Errorf("failed to get device numbers: %w", err)
	}

	// Construct the full cgroup io.max path
	ioMaxPath := filepath.Join("/sys/fs/cgroup", cgroupPath, "io.max")

	// Log the clearing operation
	logger.V(3).Info("Clearing throttling for device",
		"deviceNumbers", deviceNumbers,
		"ioMaxPath", ioMaxPath)

	// Clear throttling by writing device with "max" values (no limits)
	// Format: "major:minor rbps=max wbps=max riops=max wiops=max"
	clearConfig := fmt.Sprintf("%s rbps=max wbps=max riops=max wiops=max", deviceNumbers)

	err = os.WriteFile(ioMaxPath, []byte(clearConfig), 0644)
	if err != nil {
		return fmt.Errorf("failed to clear io.max throttling for device %s at %s: %w", deviceNumbers, ioMaxPath, err)
	}

	logger.V(3).Info("Successfully cleared throttling for device", "deviceNumbers", deviceNumbers)
	return nil
}

// VolumeMount represents an active volume mount
type VolumeMount struct {
	MountPath  string
	DevicePath string
	CgroupPath string
}

// findActiveVolumeMounts finds all active mounts for a given volume ID
func (ns *Server) findActiveVolumeMounts(volumeID string) ([]VolumeMount, error) {
	var mounts []VolumeMount

	// The volume ID is used in the mount path, so we can search /proc/mounts
	// and /var/lib/kubelet/pods for matching paths

	// Read /proc/mounts to find all CSI volume mounts
	content, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil, fmt.Errorf("failed to read /proc/mounts: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			mountPath := fields[1]
			devicePath := fields[0]

			// Check if this mount path corresponds to our volume
			if ns.isMountForVolume(mountPath, volumeID) {
				// Extract pod cgroup path from mount path
				cgroupPath, err := ns.findPodCgroupFromTargetPath(mountPath)
				if err != nil {
					ctx := context.Background()
					logger := log.FromContext(ctx).WithName("findActiveVolumeMounts")
					logger.V(4).Info("Could not find cgroup for mount", "mountPath", mountPath, "error", err)
					continue
				}

				// Resolve device path to actual device
				realDevicePath, err := ns.findDeviceForMountPath(mountPath)
				if err != nil {
					ctx := context.Background()
					logger := log.FromContext(ctx).WithName("findActiveVolumeMounts")
					logger.V(4).Info("Could not find device for mount, using fallback", "mountPath", mountPath, "error", err)
					// Fall back to the device path from /proc/mounts
					realDevicePath = devicePath
				}

				mounts = append(mounts, VolumeMount{
					MountPath:  mountPath,
					DevicePath: realDevicePath,
					CgroupPath: cgroupPath,
				})

				ctx := context.Background()
				logger := log.FromContext(ctx).WithName("findActiveVolumeMounts")
				logger.V(4).Info("Found active mount",
					"mountPath", mountPath,
					"devicePath", realDevicePath,
					"cgroupPath", cgroupPath)
			}
		}
	}

	return mounts, nil
}

// isMountForVolume checks if a mount path corresponds to our volume ID
func (ns *Server) isMountForVolume(mountPath, volumeID string) bool {
	// CSI volume mount paths typically look like:
	// /var/lib/kubelet/pods/{pod-uid}/volumes/kubernetes.io~csi/{volume-name}/mount

	// The volume name should contain our volume ID (with some transformation)
	// For our driver, volume IDs are like "containerstorage#pvc-{uuid}"

	if !strings.Contains(mountPath, "/volumes/kubernetes.io~csi/") {
		return false
	}

	// Extract the volume name from the path
	parts := strings.Split(mountPath, "/")
	for i, part := range parts {
		if part == "kubernetes.io~csi" && i+1 < len(parts) {
			volumeName := parts[i+1]
			// Check if this volume name corresponds to our volume ID
			// The volume name in the path is typically the PV name, which can be derived from volume ID
			if ns.volumeNameMatchesID(volumeName, volumeID) {
				return true
			}
		}
	}

	return false
}

// volumeNameMatchesID checks if a volume name from mount path matches our volume ID
func (ns *Server) volumeNameMatchesID(volumeName, volumeID string) bool {
	// For our CSI driver, we can try to get the volume name from the volume ID
	// and compare it with the mount path volume name
	expectedVolumeName, err := ns.volume.GetVolumeName(volumeID)
	if err != nil {
		// If we can't resolve the volume name, fall back to string matching
		// Volume IDs like "containerstorage#pvc-{uuid}" should match volume names containing the PVC part
		if strings.Contains(volumeID, "#") {
			volumePart := strings.Split(volumeID, "#")[1]
			return strings.Contains(volumeName, volumePart)
		}
		return false
	}

	return volumeName == expectedVolumeName
}
