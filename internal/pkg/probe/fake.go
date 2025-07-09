// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package probe

import (
	"context"

	logr "github.com/go-logr/logr"

	"local-csi-driver/internal/pkg/block"
)

type Fake struct {
	Devices []string
	Err     error
}

// NewFake creates a new fake probe.
func NewFake(devices []string, err error) *Fake {
	return &Fake{
		Devices: devices,
		Err:     err,
	}
}

// ScanAvailableDevices simulates scanning for available devices.
func (f *Fake) ScanAvailableDevices(ctx context.Context, log logr.Logger) (*block.DeviceList, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	if len(f.Devices) == 0 {
		return nil, ErrNoDevicesFound
	}

	devices := make([]block.Device, len(f.Devices))
	for i, path := range f.Devices {
		devices[i] = block.Device{Path: path}
	}

	return &block.DeviceList{Devices: devices}, nil
}
