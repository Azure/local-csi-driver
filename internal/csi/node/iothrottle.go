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

// ThrottlingUpdater is an interface for updating running pod throttling settings
// This allows the controller to trigger updates to running pods
type ThrottlingUpdater interface {
	UpdateRunningPodsThrottling(volumeID string, throttleParams *IOThrottleParams) error
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

// extractThrottlingParamsFromAnnotations extracts throttling parameters from PV annotations
func extractThrottlingParamsFromAnnotations(annotations map[string]string) *IOThrottleParams {
	if len(annotations) == 0 {
		return nil
	}

	params := &IOThrottleParams{}
	hasParams := false

	// Parse RBPS from annotations
	if rbpsStr, exists := annotations["csi.storage.k8s.io/throttle.rbps"]; exists {
		if rbps, err := strconv.ParseInt(rbpsStr, 10, 64); err == nil {
			params.RBPS = &rbps
			hasParams = true
		}
	}

	// Parse WBPS from annotations
	if wbpsStr, exists := annotations["csi.storage.k8s.io/throttle.wbps"]; exists {
		if wbps, err := strconv.ParseInt(wbpsStr, 10, 64); err == nil {
			params.WBPS = &wbps
			hasParams = true
		}
	}

	// Parse RIOPS from annotations
	if riopsStr, exists := annotations["csi.storage.k8s.io/throttle.riops"]; exists {
		if riops, err := strconv.ParseInt(riopsStr, 10, 64); err == nil {
			params.RIOPS = &riops
			hasParams = true
		}
	}

	// Parse WIOPS from annotations
	if wiopsStr, exists := annotations["csi.storage.k8s.io/throttle.wiops"]; exists {
		if wiops, err := strconv.ParseInt(wiopsStr, 10, 64); err == nil {
			params.WIOPS = &wiops
			hasParams = true
		}
	}

	if !hasParams {
		return nil
	}

	return params
}

// UpdateRunningPodsThrottling updates the IO throttling parameters for all running pods using a specific volume
// This function is called after ControllerModifyVolume successfully updates PV annotations
func (ns *Server) UpdateRunningPodsThrottling(volumeID string, throttleParams *IOThrottleParams) error {
	if throttleParams == nil {
		return nil // No throttling to update
	}

	fmt.Printf("Debug: UpdateRunningPodsThrottling called for volume %s with params %+v\n", volumeID, throttleParams)

	// Find all active volume mounts for this volume ID
	activeMounts, err := ns.findActiveVolumeMounts(volumeID)
	if err != nil {
		return fmt.Errorf("failed to find active mounts for volume %s: %w", volumeID, err)
	}

	if len(activeMounts) == 0 {
		fmt.Printf("Debug: No active mounts found for volume %s\n", volumeID)
		return nil
	}

	fmt.Printf("Debug: Found %d active mounts for volume %s\n", len(activeMounts), volumeID)

	// Update throttling for each active mount
	var updateErrors []error
	for _, mountInfo := range activeMounts {
		fmt.Printf("Debug: Updating throttling for mount %s, device %s, cgroup %s\n",
			mountInfo.MountPath, mountInfo.DevicePath, mountInfo.CgroupPath)

		if err := ns.configurePodCgroupIOMax(mountInfo.DevicePath, throttleParams, mountInfo.CgroupPath); err != nil {
			updateErrors = append(updateErrors, fmt.Errorf("failed to update throttling for mount %s: %w", mountInfo.MountPath, err))
		} else {
			fmt.Printf("Debug: Successfully updated throttling for mount %s\n", mountInfo.MountPath)
		}
	}

	if len(updateErrors) > 0 {
		return fmt.Errorf("failed to update some mounts: %v", updateErrors)
	}

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
					fmt.Printf("Debug: Could not find cgroup for mount %s: %v\n", mountPath, err)
					continue
				}

				// Resolve device path to actual device
				realDevicePath, err := ns.findDeviceForMountPath(mountPath)
				if err != nil {
					fmt.Printf("Debug: Could not find device for mount %s: %v\n", mountPath, err)
					// Fall back to the device path from /proc/mounts
					realDevicePath = devicePath
				}

				mounts = append(mounts, VolumeMount{
					MountPath:  mountPath,
					DevicePath: realDevicePath,
					CgroupPath: cgroupPath,
				})

				fmt.Printf("Debug: Found active mount - path: %s, device: %s, cgroup: %s\n",
					mountPath, realDevicePath, cgroupPath)
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
