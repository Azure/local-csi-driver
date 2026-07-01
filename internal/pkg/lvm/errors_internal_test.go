// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm

import (
	"errors"
	"fmt"
	"testing"
)

func TestGetErrorType(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{
			name:    "volume group not found",
			input:   "Volume group \"vg0\" not found",
			wantErr: ErrNotFound,
		},
		{
			name:    "failed to find logical volume",
			input:   "Failed to find logical volume vg0/lv0",
			wantErr: ErrNotFound,
		},
		{
			// vgcreate blocked by a leftover /dev node with no LVM metadata.
			name:    "stale device node blocks vgcreate",
			input:   "/dev/vg0: already exists in filesystem",
			wantErr: ErrStaleDeviceNode,
		},
		{
			// A genuine duplicate volume group in LVM metadata.
			name:    "volume group already exists in metadata",
			input:   "A volume group called vg0 already exists.",
			wantErr: ErrAlreadyExists,
		},
		{
			name:    "filesystem in use",
			input:   "Can't open /dev/loop0 exclusively. Device contains a filesystem in use.",
			wantErr: ErrInUse,
		},
		{
			name:    "insufficient free space",
			input:   "Volume group \"vg0\" has insufficient free space",
			wantErr: ErrResourceExhausted,
		},
		{
			name:    "physical volume already in volume group",
			input:   "Physical volume /dev/loop0 is already in volume group vg0",
			wantErr: ErrPVAlreadyInVolumeGroup,
		},
		{
			name:    "unrecognized error is passed through",
			input:   "some unexpected lvm failure",
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getErrorType(errors.New(tt.input))
			if tt.wantErr == nil {
				if got.Error() != tt.input {
					t.Fatalf("expected passthrough %q, got %q", tt.input, got.Error())
				}
				return
			}
			if !errors.Is(got, tt.wantErr) {
				t.Fatalf("expected error to wrap %v, got %v", tt.wantErr, got)
			}
			// The stale-node case must not be conflated with ErrAlreadyExists.
			if tt.wantErr == ErrStaleDeviceNode && errors.Is(got, ErrAlreadyExists) {
				t.Fatalf("stale device node error must not be categorized as ErrAlreadyExists: %v", got)
			}
		})
	}
}

// Ensure the sentinel errors remain distinct.
func TestStaleDeviceNodeDistinctFromAlreadyExists(t *testing.T) {
	wrapped := fmt.Errorf("%w: detail", ErrStaleDeviceNode)
	if errors.Is(wrapped, ErrAlreadyExists) {
		t.Fatal("ErrStaleDeviceNode must not match ErrAlreadyExists")
	}
	if !errors.Is(wrapped, ErrStaleDeviceNode) {
		t.Fatal("wrapped error should match ErrStaleDeviceNode")
	}
}
