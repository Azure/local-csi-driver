// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package probe

import (
	"context"
	"fmt"
	"sort"

	"github.com/go-logr/logr"

	"local-csi-driver/internal/pkg/block"
)

var (
	// ErrNoDevicesFound is returned when no devices are found.
	ErrNoDevicesFound = fmt.Errorf("no devices found")

	// ErrNoDevicesMatchingFilter is returned when no devices match the filter.
	ErrNoDevicesMatchingFilter = fmt.Errorf("no devices matching filter found")
)

//go:generate mockgen -copyright_file ../../../hack/mockgen_copyright.txt -destination=mock_probe.go -mock_names=Interface=Mock -package=probe -source=probe.go Interface
type Interface interface {
	ScanDevices(ctx context.Context, log logr.Logger) ([]string, error)
	ScanAvailableDevices(ctx context.Context, log logr.Logger) (*block.DeviceList, error)
}

var _ Interface = &deviceScanner{}

// deviceScanner is a struct that implements the DeviceScanner interface.
type deviceScanner struct {
	block.Interface
	filter *Filter
}

// New creates a new deviceScanner instance.
func New(b block.Interface, f *Filter) Interface {
	return &deviceScanner{b, f}
}

// ScanDevices scans for devices and returns their paths.
func (m *deviceScanner) ScanDevices(ctx context.Context, log logr.Logger) ([]string, error) {
	devices, err := m.GetDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get devices: %w", err)
	}
	if len(devices.Devices) == 0 {
		return nil, ErrNoDevicesFound
	}

	// we do not know the size of the slice, it is likely to be small
	// so we do not preallocate it
	var paths []string //nolint:prealloc
	for _, device := range devices.Devices {
		if !m.filter.Match(device) {
			log.V(2).Info("device filtered out", "device", device)
			continue
		}
		paths = append(paths, device.Path)
		log.V(1).Info("device found", "device", device)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil, ErrNoDevicesMatchingFilter
	}
	return paths, nil
}

// GetUnfomattedDevices retrieves devices that are unformatted.
func (m *deviceScanner) ScanAvailableDevices(ctx context.Context, log logr.Logger) (*block.DeviceList, error) {
	devices, err := m.GetDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get devices: %w", err)
	}

	var unformatted []block.Device
	for _, device := range devices.Devices {
		if !m.filter.Match(device) {
			log.V(2).Info("device filtered out", "device", device)
			continue
		}
		isUnformatted, err := m.IsBlkDevUnformatted(device.Path)
		if err != nil {
			log.V(2).Error(err, "failed to check if device is unformatted", "device", device)
			return nil, fmt.Errorf("failed to check if device is unformatted: %w", err)
		}
		if isUnformatted {
			log.V(2).Info("unformatted device found", "device", device)
			unformatted = append(unformatted, device)
			continue
		}
		log.V(2).Info("device is formatted, skipping", "device", device)
	}

	if len(unformatted) == 0 {
		return nil, ErrNoDevicesFound
	}
	return &block.DeviceList{Devices: unformatted}, nil
}
