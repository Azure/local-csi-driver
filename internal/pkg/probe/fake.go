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

// ScanDevices returns the list of devices or an error if no devices are found.
func (f *Fake) ScanDevices(ctx context.Context, log logr.Logger) ([]string, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	if len(f.Devices) == 0 {
		return nil, ErrNoDevicesFound
	}
	return f.Devices, nil
}

// GetDevices returns a list of devices.
func (f *Fake) GetDevices(ctx context.Context) (*block.DeviceList, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	devices := []block.Device{}
	for _, path := range f.Devices {
		devices = append(devices, block.Device{Path: path})
	}
	return &block.DeviceList{Devices: devices}, nil
}
