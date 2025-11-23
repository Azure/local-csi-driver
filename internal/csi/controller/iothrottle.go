// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

// parseQuantityOrInt parses a string value that can be either a plain integer or a Kubernetes quantity.
// For bytes per second (BPS) parameters, it supports standard Kubernetes quantity formats like:
// - Plain numbers: "1000", "50000"
// - Binary units: "1Ki", "10Mi", "1Gi", "100Ki"
// - Decimal units: "1k", "10M", "1G", "100k" (case-insensitive: "20K" = "20k", "2g" = "2G")
// For IOPS parameters, it typically uses plain numbers or decimal multipliers like "50k"
//
// Examples:
// - rbps: "100Mi" = 100 * 1024 * 1024 = 104,857,600 bytes/sec
// - wbps: "50M" = 50 * 1000 * 1000 = 50,000,000 bytes/sec
// - riops: "50k" or "50K" = 50 * 1000 = 50,000 IOPS
// - wiops: "25000" = 25,000 IOPS
func parseQuantityOrInt(value, paramName string) (int64, error) {
	if value == "" {
		return 0, fmt.Errorf("%s parameter cannot be empty", paramName)
	}

	// Normalize case for decimal units: K->k, M->M (already correct), G->g
	// Binary units (Ki, Mi, Gi) are already case-correct in typical usage
	// This allows users to use "20K" instead of requiring "20k"
	normalizedValue := normalizeQuantityCase(value)

	// First try parsing as a Kubernetes quantity
	if quantity, err := resource.ParseQuantity(normalizedValue); err == nil {
		quantityValue := quantity.Value()
		if quantityValue < 0 {
			return 0, fmt.Errorf("%s value %d cannot be negative", paramName, quantityValue)
		}
		return quantityValue, nil
	}

	// Fall back to parsing as a plain integer for backward compatibility
	intValue, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s value '%s': must be a valid integer or Kubernetes quantity (e.g., '50k', '100Mi', '1G')", paramName, value)
	}

	if intValue < 0 {
		return 0, fmt.Errorf("%s value %d cannot be negative", paramName, intValue)
	}

	return intValue, nil
}

// normalizeQuantityCase normalizes case for decimal unit suffixes to match Kubernetes quantity format.
// Kubernetes resource.ParseQuantity expects specific cases: k (kilo), M (mega), G (giga)
// This function converts: K->k, g->G, while preserving binary units (Ki, Mi, Gi) and M/G (which are already correct)
func normalizeQuantityCase(value string) string {
	if len(value) == 0 {
		return value
	}

	// Handle common case-insensitive patterns
	// Convert K to k (but preserve Ki for binary)
	if strings.HasSuffix(value, "K") && !strings.HasSuffix(value, "Ki") {
		return value[:len(value)-1] + "k"
	}

	// Convert lowercase g to uppercase G (but preserve Gi for binary)
	if strings.HasSuffix(value, "g") && !strings.HasSuffix(value, "Gi") {
		return value[:len(value)-1] + "G"
	}

	// M and G are already correct case, Ki/Mi/Gi are correct
	return value
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
		rbps, err := parseQuantityOrInt(rbpsStr, "rbps")
		if err != nil {
			return nil, nil, err
		}
		throttleParams.RBPS = &rbps
	}

	// Validate wbps (write bytes per second)
	if wbpsStr, exists := params["wbps"]; exists {
		hasThrottleParams = true
		wbps, err := parseQuantityOrInt(wbpsStr, "wbps")
		if err != nil {
			return nil, nil, err
		}
		throttleParams.WBPS = &wbps
	}

	// Validate riops (read IOPS per second)
	if riopsStr, exists := params["riops"]; exists {
		hasThrottleParams = true
		riops, err := parseQuantityOrInt(riopsStr, "riops")
		if err != nil {
			return nil, nil, err
		}
		throttleParams.RIOPS = &riops
	}

	// Validate wiops (write IOPS per second)
	if wiopsStr, exists := params["wiops"]; exists {
		hasThrottleParams = true
		wiops, err := parseQuantityOrInt(wiopsStr, "wiops")
		if err != nil {
			return nil, nil, err
		}
		throttleParams.WIOPS = &wiops
	}

	// Return nil if no throttling parameters were found
	if !hasThrottleParams {
		return nil, nil, nil
	}

	return throttleParams, nil, nil
}

// GetThrottlingParamsFromVAC retrieves IO throttling parameters from a VolumeAttributesClass
// This is the preferred method as it reads the latest parameters directly from the VAC
func GetThrottlingParamsFromVAC(ctx context.Context, k8sClient client.Client, vacName string) (*IOThrottleParams, *ClearThrottleParams, error) {
	if vacName == "" {
		return nil, nil, nil
	}

	// Get VolumeAttributesClass from Kubernetes API
	vac := &storagev1.VolumeAttributesClass{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: vacName}, vac); err != nil {
		return nil, nil, fmt.Errorf("failed to get VolumeAttributesClass %s: %w", vacName, err)
	}

	// Validate parameters from VAC
	return ValidateIOThrottleParams(vac.Parameters)
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
