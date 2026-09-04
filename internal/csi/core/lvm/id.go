// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm

import (
	"fmt"
	"strings"
)

const (
	// separator is used to separate the volume group name and logical volume name in the volume id.
	separator = "#"
)

// volumeId is used to represent a volume id in the format <volume-group>#<logical-volume> for LVM volumes.
type volumeId struct {
	// VolumeGroup is the name of the volume group
	VolumeGroup string
	// LogicalVolume is the name of the logical volume
	LogicalVolume string
}

func (v *volumeId) String() string {
	return fmt.Sprintf("%s%s%s", v.VolumeGroup, separator, v.LogicalVolume)
}

// NewVolumeID returns a new volume id generated from the input.
func newVolumeId(vg, lv string) (*volumeId, error) {
	if len(vg) == 0 {
		return nil, fmt.Errorf("volume group name is empty")
	}
	if len(lv) == 0 {
		return nil, fmt.Errorf("logical volume name is empty")
	}
	return &volumeId{
		VolumeGroup:   vg,
		LogicalVolume: lv,
	}, nil
}

func newIdFromString(id string) (*volumeId, error) {
	segments := strings.Split(id, separator)
	if len(segments) != 2 {
		return nil, fmt.Errorf("error parsing volume id: %q, expected 2 segments, got %d", id, len(segments))
	}
	vg := segments[0]
	if len(vg) == 0 {
		return nil, fmt.Errorf("error parsing volume id: %q, volume group name is empty", id)
	}
	lv := segments[1]
	if len(lv) == 0 {
		return nil, fmt.Errorf("error parsing volume id: %q, logical volume name is empty", id)
	}
	// The quarantine namespace is reserved for volumes the driver has taken
	// out of service. A volume handle is operator-supplied for a
	// pre-provisioned PersistentVolume, and one naming itself into that
	// namespace would be treated as awaiting destruction and zeroed.
	if isQuarantineName(lv) {
		return nil, fmt.Errorf("error parsing volume id: %q, logical volume name is reserved", id)
	}
	return &volumeId{
		VolumeGroup:   vg,
		LogicalVolume: lv,
	}, nil
}

func (v *volumeId) ReconstructLogicalVolumePath() string {
	return fmt.Sprintf("/dev/%s/%s", v.VolumeGroup, v.LogicalVolume)
}
