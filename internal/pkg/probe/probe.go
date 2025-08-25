// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package probe

import (
	"context"
	"fmt"

	"local-csi-driver/internal/pkg/block"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

var (
	// ErrNoDevicesFound is returned when no devices are found.
	ErrNoDevicesFound = fmt.Errorf("no devices found")
)

//go:generate mockgen -copyright_file ../../../hack/mockgen_copyright.txt -destination=mock_probe.go -mock_names=Interface=Mock -package=probe -source=probe.go Interface
type Interface interface {
	ScanAvailableDevices(ctx context.Context) (*block.DeviceList, error)
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
func (m *deviceScanner) ScanAvailableDevices(ctx context.Context) (*block.DeviceList, error) {
	log := log.FromContext(ctx)
	devices, err := m.GetDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get devices: %w", err)
	}

	var availableDevices []block.Device
	for _, device := range devices.Devices {
		if !m.filter.Match(device) {
			log.V(3).Info("device filtered out", "device", device)
			continue
		}
		fsType, isFormatted, err := m.IsFormatted(device.Path)
		if err != nil {
			return nil, fmt.Errorf("failed to check if device is unformatted: %w", err)
		}
		if !isFormatted {
			log.V(3).Info("unformatted device found", "device", device)
			availableDevices = append(availableDevices, device)
			continue
		}

		if fsType == block.Lvm2Type {
			log.V(3).Info("device is LVM physical volume, returning", "device", device)
			availableDevices = append(availableDevices, device)
			continue
		}

		log.V(3).Info("device is formatted, skipping", "device", device)
	}

	if len(availableDevices) == 0 {
		return nil, ErrNoDevicesFound
	}
	return &block.DeviceList{Devices: availableDevices}, nil
}
