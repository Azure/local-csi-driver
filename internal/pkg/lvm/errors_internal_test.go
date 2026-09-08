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

// TestVolumeGroupNotFoundClassification pins the distinction between a message
// about a missing volume group and one about a missing logical volume that
// merely names the group it was looked for in.
//
// Deletion depends on this. A missing logical volume is proof that the volume
// is gone, so DeleteVolume reports success; an invisible volume group is
// usually temporary, so deletion must retry rather than report success and let
// the PersistentVolume be removed while a populated volume survives on disk.
func TestVolumeGroupNotFoundClassification(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantVGAbsent bool
	}{
		{
			name:         "missing volume group",
			input:        `Volume group "vg0" not found`,
			wantVGAbsent: true,
		},
		{
			name:         "missing volume group with cannot process",
			input:        `Volume group "vg0" not found. Cannot process volume group vg0`,
			wantVGAbsent: true,
		},
		{
			// Names the group, but is about the logical volume.
			name:         "missing logical volume names its volume group",
			input:        `Failed to find logical volume 'lv0' in volume group 'vg0'`,
			wantVGAbsent: false,
		},
		{
			// lvrename's wording for a missing source volume.
			name:         "missing rename source names its volume group",
			input:        `Existing logical volume "lv0" not found in volume group "vg0".`,
			wantVGAbsent: false,
		},
		{
			name:         "missing logical volume by path",
			input:        `Failed to find logical volume "vg0/lv0"`,
			wantVGAbsent: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := getErrorType(fmt.Errorf("exit status 5: %s", tt.input))

			// Every case is a missing object, so callers that only care about
			// that must keep working regardless of the distinction.
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("getErrorType(%q) is not ErrNotFound", tt.input)
			}

			if got := errors.Is(err, ErrVolumeGroupNotFound); got != tt.wantVGAbsent {
				t.Errorf("getErrorType(%q) is ErrVolumeGroupNotFound = %v, want %v", tt.input, got, tt.wantVGAbsent)
			}
		})
	}
}
