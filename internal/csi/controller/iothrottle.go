// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// IOThrottleParams represents the IO throttling parameters
type IOThrottleParams struct {
	RBPS  *int64 // Read bytes per second
	WBPS  *int64 // Write bytes per second
	RIOPS *int64 // Read IOPS per second
	WIOPS *int64 // Write IOPS per second
}

// ClearThrottleParams represents a request to clear/remove all throttling
// This is used when a "no-throttle" VAC is applied to restore original performance
type ClearThrottleParams struct {
	ShouldClear bool
}

// ValidateIOThrottleParams validates the IO throttling parameters from mutable parameters
// Returns IOThrottleParams for setting throttling, ClearThrottleParams for removing throttling, or nil for no change
// Supports a special "clear-throttling" parameter that can be set to "true" in a VAC to remove all throttling
func ValidateIOThrottleParams(params map[string]string) (*IOThrottleParams, *ClearThrottleParams, error) {
	if len(params) == 0 {
		return nil, nil, nil
	}

	// Check for explicit clear throttling request via special parameter
	// This allows users to apply a "no-throttle" VAC that removes all throttling
	if clearStr, exists := params["clear-throttling"]; exists {
		if clearStr == "true" {
			return nil, &ClearThrottleParams{ShouldClear: true}, nil
		}
	}

	throttleParams := &IOThrottleParams{}
	hasThrottleParams := false

	// Validate rbps (read bytes per second)
	if rbpsStr, exists := params["rbps"]; exists {
		hasThrottleParams = true
		if rbpsStr == "" {
			return nil, nil, fmt.Errorf("rbps parameter cannot be empty")
		}
		rbps, err := strconv.ParseInt(rbpsStr, 10, 64)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid rbps value '%s': must be a valid integer", rbpsStr)
		}
		if rbps < 0 {
			return nil, nil, fmt.Errorf("rbps value %d cannot be negative", rbps)
		}
		throttleParams.RBPS = &rbps
	}

	// Validate wbps (write bytes per second)
	if wbpsStr, exists := params["wbps"]; exists {
		hasThrottleParams = true
		if wbpsStr == "" {
			return nil, nil, fmt.Errorf("wbps parameter cannot be empty")
		}
		wbps, err := strconv.ParseInt(wbpsStr, 10, 64)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid wbps value '%s': must be a valid integer", wbpsStr)
		}
		if wbps < 0 {
			return nil, nil, fmt.Errorf("wbps value %d cannot be negative", wbps)
		}
		throttleParams.WBPS = &wbps
	}

	// Validate riops (read IOPS per second)
	if riopsStr, exists := params["riops"]; exists {
		hasThrottleParams = true
		if riopsStr == "" {
			return nil, nil, fmt.Errorf("riops parameter cannot be empty")
		}
		riops, err := strconv.ParseInt(riopsStr, 10, 64)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid riops value '%s': must be a valid integer", riopsStr)
		}
		if riops < 0 {
			return nil, nil, fmt.Errorf("riops value %d cannot be negative", riops)
		}
		throttleParams.RIOPS = &riops
	}

	// Validate wiops (write IOPS per second)
	if wiopsStr, exists := params["wiops"]; exists {
		hasThrottleParams = true
		if wiopsStr == "" {
			return nil, nil, fmt.Errorf("wiops parameter cannot be empty")
		}
		wiops, err := strconv.ParseInt(wiopsStr, 10, 64)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid wiops value '%s': must be a valid integer", wiopsStr)
		}
		if wiops < 0 {
			return nil, nil, fmt.Errorf("wiops value %d cannot be negative", wiops)
		}
		throttleParams.WIOPS = &wiops
	}

	// Return nil if no throttling parameters were found
	if !hasThrottleParams {
		return nil, nil, nil
	}

	return throttleParams, nil, nil
}

// GetDeviceMajorMinor gets the major:minor device numbers for a device path
func GetDeviceMajorMinor(devicePath string) (string, error) {
	// Resolve any symlinks to get the actual device
	realPath, err := filepath.EvalSymlinks(devicePath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve device path %s: %w", devicePath, err)
	}

	// Extract device name from path
	deviceName := filepath.Base(realPath)

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
