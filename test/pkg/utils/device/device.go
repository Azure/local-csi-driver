//go:build linux && cgo

// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package device

import (
	"fmt"
	"os"

	"pault.ag/go/loopback"
)

// Create creates a new loopback device and returns the device name, a cleanup function, and an error if any.
// The device is created on a file with a small unspecified size. Use this function where file size is not important.
func CreateLoopDev() (string, func(), error) {
	// Create a temporary img to be used as a loopback device
	img, err := os.CreateTemp("", "loopback")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temporary file: %w", err)
	}

	// Ensure the file is closed before returning
	defer func() {
		img.Close() //nolint:errcheck
	}()

	// Get next loop device.
	dev, err := loopback.NextLoopDevice()
	if err != nil {
		return "", nil, fmt.Errorf("failed to get next loop device: %w", err)
	}

	// Set up the loopback device
	if err := loopback.Loop(dev, img); err != nil {
		return "", nil, fmt.Errorf("failed to set up loopback device: %w", err)
	}

	// Cleanup function to detach the loopback device and remove the temporary file
	cleanup := func() {
		if err := loopback.Unloop(dev); err != nil {
			fmt.Printf("failed to detach loopback device: %v\n", err)
		}
		if err := os.Remove(img.Name()); err != nil {
			fmt.Printf("failed to remove temporary file: %v\n", err)
		}
	}

	return dev.Name(), cleanup, nil
}

// Create creates a new loopback device and returns the device name, a cleanup function, and an error if any.
// The device is created on a file with a specified size. Use this function where file size is important.
// Please note that the file create is not zeroed out, its a sparse file.
func CreateLoopDevWithSize(size int64) (string, func(), error) {
	// Create a temporary filePtr to be used as a loopback device
	filePtr, err := os.CreateTemp("", "loopback")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temporary file: %w", err)
	}

	// Ensure the file is closed before returning
	defer func() {
		filePtr.Close() //nolint:errcheck
	}()

	if os.Truncate(filePtr.Name(), size) != nil {
		filePtr.Close() //nolint:errcheck
		return "", nil, fmt.Errorf("failed to truncate file: %w", err)
	}

	// Get next loop device.
	dev, err := loopback.NextLoopDevice()
	if err != nil {
		return "", nil, fmt.Errorf("failed to get next loop device: %w", err)
	}

	// Set up the loopback device
	if err := loopback.Loop(dev, filePtr); err != nil {
		return "", nil, fmt.Errorf("failed to set up loopback device: %w", err)
	}

	// Cleanup function to detach the loopback device and remove the temporary file
	cleanup := func() {
		if err := loopback.Unloop(dev); err != nil {
			fmt.Printf("failed to detach loopback device: %v\n", err)
		}
		if err := os.Remove(filePtr.Name()); err != nil {
			fmt.Printf("failed to remove temporary file: %v\n", err)
		}
	}

	return dev.Name(), cleanup, nil
}
