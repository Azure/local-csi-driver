// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package probe

import (
	"testing"

	"local-csi-driver/internal/pkg/block"
)

func TestPathFilter(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		device   block.Device
		expected bool
	}{
		{
			name:     "match path prefix",
			path:     "/dev/sd",
			device:   block.Device{Path: "/dev/sda"},
			expected: true,
		},
		{
			name:     "no match path prefix",
			path:     "/dev/sd",
			device:   block.Device{Path: "/dev/nvme0n1"},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := &PathFilter{Path: tt.path}
			result := filter.Match(tt.device)
			if result != tt.expected {
				t.Errorf("Match(%v) = %v, want %v", tt.device, result, tt.expected)
			}
		})
	}
}

func TestTypeFilter(t *testing.T) {
	tests := []struct {
		name     string
		typeStr  string
		device   block.Device
		expected bool
	}{
		{
			name:     "match type",
			typeStr:  "SSD",
			device:   block.Device{Type: "ssd"},
			expected: true,
		},
		{
			name:     "no match type",
			typeStr:  "HDD",
			device:   block.Device{Type: "ssd"},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := &TypeFilter{Type: tt.typeStr}
			result := filter.Match(tt.device)
			if result != tt.expected {
				t.Errorf("Match(%v) = %v, want %v", tt.device, result, tt.expected)
			}
		})
	}
}

func TestModelFilter(t *testing.T) {
	tests := []struct {
		name     string
		models   []string
		device   block.Device
		expected bool
	}{
		{
			name:     "match model",
			models:   []string{"Samsung v2", "Samsung"},
			device:   block.Device{Model: " Samsung "},
			expected: true,
		},
		{
			name:     "match model",
			models:   []string{"Samsung v2", "Samsung"},
			device:   block.Device{Model: " Samsung v2 "},
			expected: true,
		},
		{
			name:     "no match model",
			models:   []string{"Intel"},
			device:   block.Device{Model: "Samsung"},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := NewModelFilter(tt.models...)
			result := filter.Match(tt.device)
			if result != tt.expected {
				t.Errorf("Match(%v) = %v, want %v", tt.device, result, tt.expected)
			}
		})
	}
}

func TestFilter(t *testing.T) {
	tests := []struct {
		name     string
		filters  []FilterPredicate
		device   block.Device
		expected bool
	}{
		{
			name: "all filters match",
			filters: []FilterPredicate{
				&PathFilter{Path: "/dev/sd"},
				&TypeFilter{Type: "SSD"},
				NewModelFilter("Samsung", "Samsung v2"),
			},
			device:   block.Device{Path: "/dev/sda", Type: "ssd", Model: "Samsung"},
			expected: true,
		},
		{
			name: "one filter does not match",
			filters: []FilterPredicate{
				&PathFilter{Path: "/dev/sd"},
				&TypeFilter{Type: "SSD"},
				NewModelFilter("Intel"),
			},
			device:   block.Device{Path: "/dev/sda", Type: "ssd", Model: "Samsung"},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := &Filter{Filters: tt.filters}
			result := filter.Match(tt.device)
			if result != tt.expected {
				t.Errorf("Match(%v) = %v, want %v", tt.device, result, tt.expected)
			}
		})
	}
}

func TestAnyFilter(t *testing.T) {
	tests := []struct {
		name     string
		filters  []FilterPredicate
		device   block.Device
		expected bool
	}{
		{
			name: "matches when first predicate matches",
			filters: []FilterPredicate{
				&PathFilter{Path: "/dev/nvme"},
				&PathFilter{Path: "/dev/sda"},
			},
			device:   block.Device{Path: "/dev/nvme0n1"},
			expected: true,
		},
		{
			name: "matches when second predicate matches",
			filters: []FilterPredicate{
				&PathFilter{Path: "/dev/nvme"},
				&PathFilter{Path: "/dev/sda"},
			},
			device:   block.Device{Path: "/dev/sda"},
			expected: true,
		},
		{
			name: "no match when none match",
			filters: []FilterPredicate{
				&PathFilter{Path: "/dev/nvme"},
				&PathFilter{Path: "/dev/sda"},
			},
			device:   block.Device{Path: "/dev/vdb"},
			expected: false,
		},
		{
			name:     "no match when empty",
			filters:  []FilterPredicate{},
			device:   block.Device{Path: "/dev/nvme0n1"},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := &anyFilter{filters: tt.filters}
			result := filter.Match(tt.device)
			if result != tt.expected {
				t.Errorf("Match(%v) = %v, want %v", tt.device, result, tt.expected)
			}
		})
	}
}

func TestAppendUnique(t *testing.T) {
	tests := []struct {
		name     string
		base     []string
		extra    []string
		expected []string
	}{
		{
			name:     "appends new entries",
			base:     []string{"/dev/nvme"},
			extra:    []string{"/dev/sda"},
			expected: []string{"/dev/nvme", "/dev/sda"},
		},
		{
			name:     "drops exact duplicates but keeps case variants",
			base:     []string{"Microsoft NVMe Direct Disk"},
			extra:    []string{"Microsoft NVMe Direct Disk", "microsoft nvme direct disk", "Contoso Disk"},
			expected: []string{"Microsoft NVMe Direct Disk", "microsoft nvme direct disk", "Contoso Disk"},
		},
		{
			name:     "trims and drops empty extras",
			base:     []string{"disk"},
			extra:    []string{"  ", "loop "},
			expected: []string{"disk", "loop"},
		},
		{
			name:     "nil extra returns base",
			base:     []string{"disk"},
			extra:    nil,
			expected: []string{"disk"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := appendUnique(tt.base, tt.extra)
			if len(result) != len(tt.expected) {
				t.Fatalf("appendUnique(%v, %v) = %v, want %v", tt.base, tt.extra, result, tt.expected)
			}
			for i := range result {
				if result[i] != tt.expected[i] {
					t.Errorf("appendUnique[%d] = %q, want %q", i, result[i], tt.expected[i])
				}
			}
		})
	}
}

func TestNewEphemeralDiskFilter(t *testing.T) {
	tests := []struct {
		name        string
		addonPaths  []string
		addonModels []string
		addonTypes  []string
		device      block.Device
		expected    bool
	}{
		{
			name:     "built-in default model matches with no extras",
			device:   block.Device{Path: "/dev/nvme0n1", Model: "Microsoft NVMe Direct Disk", Type: "disk"},
			expected: true,
		},
		{
			name:     "default amazon model matches with no extras",
			device:   block.Device{Path: "/dev/nvme0n1", Model: "Amazon EC2 NVMe Instance Storage", Type: "disk"},
			expected: true,
		},
		{
			name:        "extra model is matched",
			addonModels: []string{"Contoso NVMe Disk"},
			device:      block.Device{Path: "/dev/nvme0n1", Model: "Contoso NVMe Disk", Type: "disk"},
			expected:    true,
		},
		{
			name:        "default model still matches after adding an extra",
			addonModels: []string{"Contoso NVMe Disk"},
			device:      block.Device{Path: "/dev/nvme0n1", Model: "Microsoft NVMe Direct Disk v2", Type: "disk"},
			expected:    true,
		},
		{
			name:       "extra path prefix matches with a default model",
			addonPaths: []string{"/dev/sda"},
			device:     block.Device{Path: "/dev/sda", Model: "Microsoft NVMe Direct Disk", Type: "disk"},
			expected:   true,
		},
		{
			name:       "extra type is matched with a default model",
			addonTypes: []string{"loop"},
			device:     block.Device{Path: "/dev/nvme0n1", Model: "Microsoft NVMe Direct Disk", Type: "loop"},
			expected:   true,
		},
		{
			name:     "unknown model is rejected",
			device:   block.Device{Path: "/dev/nvme0n1", Model: "Unknown Disk", Type: "disk"},
			expected: false,
		},
		{
			name:     "non-default path is rejected without an extra prefix",
			device:   block.Device{Path: "/dev/sda", Model: "Microsoft NVMe Direct Disk", Type: "disk"},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := NewEphemeralDiskFilter(tt.addonPaths, tt.addonModels, tt.addonTypes)
			if got := filter.Match(tt.device); got != tt.expected {
				t.Errorf("Match(%v) = %v, want %v", tt.device, got, tt.expected)
			}
		})
	}
}
