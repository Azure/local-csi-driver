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
	return &volumeId{
		VolumeGroup:   vg,
		LogicalVolume: lv,
	}, nil
}

func (v *volumeId) ReconstructLogicalVolumePath() string {
	return fmt.Sprintf("/dev/%s/%s", v.VolumeGroup, v.LogicalVolume)
}

// ReconstructMapperPath returns the device-mapper node path for the logical
// volume: /dev/mapper/<vg>-<lv>. Literal hyphens in the volume group or logical
// volume name are doubled to match device-mapper's name mangling.
func (v *volumeId) ReconstructMapperPath() string {
	escape := func(s string) string { return strings.ReplaceAll(s, "-", "--") }
	return fmt.Sprintf("/dev/mapper/%s-%s", escape(v.VolumeGroup), escape(v.LogicalVolume))
}
