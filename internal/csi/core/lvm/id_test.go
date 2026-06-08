// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm

import (
	"strings"
	"testing"
)

func TestNewVolumeId(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		vg        string
		lv        string
		wantErr   bool
		errSubstr string
	}{
		{
			name:    "valid volume id",
			vg:      "vg1",
			lv:      "lv1",
			wantErr: false,
		},
		{
			name:      "empty volume group",
			vg:        "",
			lv:        "lv1",
			wantErr:   true,
			errSubstr: "volume group name is empty",
		},
		{
			name:      "empty logical volume",
			vg:        "vg1",
			lv:        "",
			wantErr:   true,
			errSubstr: "logical volume name is empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := newVolumeId(tt.vg, tt.lv)
			if (err != nil) != tt.wantErr {
				t.Errorf("newVolumeId() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil && !strings.Contains(err.Error(), tt.errSubstr) {
				t.Errorf("newVolumeId() error = %v, expected to contain %v", err, tt.errSubstr)
				return
			}
			if !tt.wantErr {
				if got.VolumeGroup != tt.vg {
					t.Errorf("newVolumeId() VolumeGroup = %v, want %v", got.VolumeGroup, tt.vg)
				}
				if got.LogicalVolume != tt.lv {
					t.Errorf("newVolumeId() LogicalVolume = %v, want %v", got.LogicalVolume, tt.lv)
				}
			}
		})
	}
}

func TestNewIdFromString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		id        string
		wantVG    string
		wantLV    string
		wantErr   bool
		errSubstr string
	}{
		{
			name:    "valid volume id",
			id:      "vg1#lv1",
			wantVG:  "vg1",
			wantLV:  "lv1",
			wantErr: false,
		},
		{
			name:      "missing separator",
			id:        "vg1lv1",
			wantErr:   true,
			errSubstr: "expected 2 segments",
		},
		{
			name:      "too many separators",
			id:        "vg1#lv1#extra",
			wantErr:   true,
			errSubstr: "expected 2 segments",
		},
		{
			name:      "empty volume group",
			id:        "#lv1",
			wantErr:   true,
			errSubstr: "volume group name is empty",
		},
		{
			name:      "empty logical volume",
			id:        "vg1#",
			wantErr:   true,
			errSubstr: "logical volume name is empty",
		},
		{
			name:      "empty string",
			id:        "",
			wantErr:   true,
			errSubstr: "expected 2 segments",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := newIdFromString(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("newIdFromString() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil && !strings.Contains(err.Error(), tt.errSubstr) {
				t.Errorf("newIdFromString() error = %v, expected to contain %v", err, tt.errSubstr)
				return
			}
			if !tt.wantErr {
				if got.VolumeGroup != tt.wantVG {
					t.Errorf("newIdFromString() VolumeGroup = %v, want %v", got.VolumeGroup, tt.wantVG)
				}
				if got.LogicalVolume != tt.wantLV {
					t.Errorf("newIdFromString() LogicalVolume = %v, want %v", got.LogicalVolume, tt.wantLV)
				}
			}
		})
	}
}

func TestVolumeIdString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		vg   string
		lv   string
		want string
	}{
		{
			name: "basic volume id",
			vg:   "vg1",
			lv:   "lv1",
			want: "vg1#lv1",
		},
		{
			name: "volume id with special characters",
			vg:   "my-vg",
			lv:   "my_lv",
			want: "my-vg#my_lv",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			vid := &volumeId{
				VolumeGroup:   tt.vg,
				LogicalVolume: tt.lv,
			}
			if got := vid.String(); got != tt.want {
				t.Errorf("volumeId.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReconstructLogicalVolumePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		vg   string
		lv   string
		want string
	}{
		{
			name: "basic volume path",
			vg:   "vg1",
			lv:   "lv1",
			want: "/dev/vg1/lv1",
		},
		{
			name: "volume path with special characters",
			vg:   "my-vg",
			lv:   "my_lv",
			want: "/dev/my-vg/my_lv",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			vid := &volumeId{
				VolumeGroup:   tt.vg,
				LogicalVolume: tt.lv,
			}
			if got := vid.ReconstructLogicalVolumePath(); got != tt.want {
				t.Errorf("volumeId.ReconstructLogicalVolumePath() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReconstructMapperPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		vg   string
		lv   string
		want string
	}{
		{
			name: "basic mapper path",
			vg:   "vg1",
			lv:   "lv1",
			want: "/dev/mapper/vg1-lv1",
		},
		{
			name: "hyphen in names is doubled",
			vg:   "my-vg",
			lv:   "pvc-1234",
			want: "/dev/mapper/my--vg-pvc--1234",
		},
		{
			name: "multiple hyphens",
			vg:   "a-b-c",
			lv:   "d-e",
			want: "/dev/mapper/a--b--c-d--e",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			vid := &volumeId{
				VolumeGroup:   tt.vg,
				LogicalVolume: tt.lv,
			}
			if got := vid.ReconstructMapperPath(); got != tt.want {
				t.Errorf("volumeId.ReconstructMapperPath() = %v, want %v", got, tt.want)
			}
		})
	}
}
