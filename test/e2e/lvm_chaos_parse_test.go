// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package e2e

import (
	"slices"
	"testing"

	"local-csi-driver/internal/csi/core/lvm"
)

// liveSet is a small helper to build the live-LV-name set used by the orphan
// detectors.
func liveSet(names ...string) map[string]struct{} {
	s := make(map[string]struct{}, len(names))
	for _, n := range names {
		s[n] = struct{}{}
	}
	return s
}

// TestParseOrphanDmDevices verifies that device-mapper nodes left behind by an
// aborted lvremove (present in /dev/mapper but with no live LV) are flagged,
// while live, control and unrelated-VG devices are ignored. This is the parsing
// that makes the chaos spec fail when a node-pod kill leaks a dm node.
func TestParseOrphanDmDevices(t *testing.T) {
	vg := lvm.DefaultVolumeGroup // "containerstorage"

	tests := []struct {
		name     string
		listing  string
		live     map[string]struct{}
		expected []string
	}{
		{
			name:     "empty",
			listing:  "",
			live:     liveSet(),
			expected: nil,
		},
		{
			name: "all live, none orphaned",
			// /dev/mapper doubles literal hyphens within the LV name.
			listing:  "control\ncontainerstorage-pvc--abcd\n",
			live:     liveSet("pvc-abcd"),
			expected: nil,
		},
		{
			name: "leaked dm node after killed node pod",
			listing: "control\n" +
				"containerstorage-pvc--abcd\n" +
				"containerstorage-pvc--dead\n",
			live:     liveSet("pvc-abcd"),
			expected: []string{"containerstorage-pvc--dead"},
		},
		{
			name: "ignores devices from other VGs",
			listing: "containerstorage-pvc--dead\n" +
				"someothervg-foo\n" +
				"control\n",
			live:     liveSet(),
			expected: []string{"containerstorage-pvc--dead"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseOrphanDmDevices(tc.listing, tc.live, vg)
			if !slices.Equal(got, tc.expected) {
				t.Fatalf("parseOrphanDmDevices() = %v, want %v", got, tc.expected)
			}
		})
	}
}

// TestParseOrphanDeviceLinks verifies that dangling symlinks under
// /dev/<defaultVG> (left behind when an aborted lvremove fails to tear down the
// device node) are flagged, while links backed by a live LV are ignored.
func TestParseOrphanDeviceLinks(t *testing.T) {
	tests := []struct {
		name     string
		lsOutput string
		live     map[string]struct{}
		expected []string
	}{
		{
			name:     "empty directory",
			lsOutput: "",
			live:     liveSet(),
			expected: nil,
		},
		{
			name:     "all links backed by live LVs",
			lsOutput: "pvc-abcd\npvc-ef01\n",
			live:     liveSet("pvc-abcd", "pvc-ef01"),
			expected: nil,
		},
		{
			name:     "leaked /dev/containerstorage symlink",
			lsOutput: "pvc-abcd\npvc-dead\n",
			live:     liveSet("pvc-abcd"),
			expected: []string{"pvc-dead"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseOrphanDeviceLinks(tc.lsOutput, tc.live)
			if !slices.Equal(got, tc.expected) {
				t.Fatalf("parseOrphanDeviceLinks() = %v, want %v", got, tc.expected)
			}
		})
	}
}
