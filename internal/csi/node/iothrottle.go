// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package node

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// IOThrottleParams represents the IO throttling parameters
type IOThrottleParams struct {
	RBPS  *int64 // Read bytes per second
	WBPS  *int64 // Write bytes per second
	RIOPS *int64 // Read IOPS per second
	WIOPS *int64 // Write IOPS per second
}

// extractThrottlingParamsFromVolumeContext extracts throttling parameters from volume context
func extractThrottlingParamsFromVolumeContext(volumeContext map[string]string) *IOThrottleParams {
	if len(volumeContext) == 0 {
		return nil
	}

	// Debug logging to see all available keys in volume context
	fmt.Printf("Debug: extractThrottlingParamsFromVolumeContext - all volume context keys:\n")
	for key, value := range volumeContext {
		fmt.Printf("  %s = %s\n", key, value)
	}

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

	// Debug logging to show the constructed path
	fmt.Printf("Debug: configurePodCgroupIOMax - ioMaxPath: %s\n", ioMaxPath)

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
	err = os.WriteFile(ioMaxPath, []byte(configStr), 0644)
	if err != nil {
		return fmt.Errorf("failed to write io.max config '%s' to %s: %w", configStr, ioMaxPath, err)
	}

	return nil
}

// getDeviceMajorMinor gets the major:minor device numbers for a device path
func getDeviceMajorMinor(devicePath string) (string, error) {
	// Debug logging
	fmt.Printf("Debug: getDeviceMajorMinor - input devicePath: %s\n", devicePath)

	// Resolve any symlinks to get the actual device
	realPath, err := filepath.EvalSymlinks(devicePath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve device path %s: %w", devicePath, err)
	}

	fmt.Printf("Debug: getDeviceMajorMinor - resolved realPath: %s\n", realPath)

	// For device-mapper devices (like LVM), we need to get the major:minor directly from the device file
	if strings.Contains(realPath, "/dev/mapper/") || strings.Contains(realPath, "/dev/dm-") {
		return getDeviceMajorMinorFromStat(realPath)
	}

	// Extract device name from path for regular block devices
	deviceName := filepath.Base(realPath)
	fmt.Printf("Debug: getDeviceMajorMinor - deviceName: %s\n", deviceName)

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
	fmt.Printf("Debug: getDeviceMajorMinorFromStat - devicePath: %s\n", devicePath)

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
	fmt.Printf("Debug: getDeviceMajorMinorFromStat - result: %s\n", majorMinor)

	return majorMinor, nil
}
