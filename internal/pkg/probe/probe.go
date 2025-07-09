// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package probe

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"

	"local-csi-driver/internal/pkg/block"
)

var (
	// ErrNoDevicesFound is returned when no devices are found.
	ErrNoDevicesFound = fmt.Errorf("no devices found")
)

//go:generate mockgen -copyright_file ../../../hack/mockgen_copyright.txt -destination=mock_probe.go -mock_names=Interface=Mock -package=probe -source=probe.go Interface
type Interface interface {
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

// ScanAvailableDevices retrieves devices that are unformatted.
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
		isFormatted, err := m.IsFormatted(device.Path)
		if err != nil {
			return nil, fmt.Errorf("failed to check if device is unformatted: %w", err)
		}
		if !isFormatted {
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
