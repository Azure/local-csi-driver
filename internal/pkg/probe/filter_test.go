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

func TestEphemeralDiskFilter(t *testing.T) {
	tests := []struct {
		name     string
		device   block.Device
		expected bool
	}{
		{
			// Standard_NC40ads_H100_v5 nodes have a different model name
			// for the ephemeral disk. This test case is to ensure that
			// the filter matches the model name for these nodes.
			name: "match disk for v2 direct disk nodes",
			device: block.Device{
				Path:  "/dev/nvme0n1",
				Type:  "disk",
				Model: "Microsoft NVMe Direct Disk v2           ",
			},
			expected: true,
		},
		{
			name: "match disk for direct disk nodes",
			device: block.Device{
				Path:  "/dev/nvme0n1",
				Type:  "disk",
				Model: "Microsoft NVMe Direct Disk           ",
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := EphemeralDiskFilter.Match(tt.device)
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

func TestNonEmpty(t *testing.T) {
        tests := []struct {
                name     string
                in       []string
                expected []string
        }{
                {
                        name:     "trims whitespace",
                        in:       []string{"  /dev/nvme  ", "\t/dev/sda\n"},
                        expected: []string{"/dev/nvme", "/dev/sda"},
                },
                {
                        name:     "removes empty and whitespace-only entries",
                        in:       []string{"/dev/nvme", "", "   ", "/dev/sda"},
                        expected: []string{"/dev/nvme", "/dev/sda"},
                },
                {
                        name:     "all empty yields empty slice",
                        in:       []string{"", "  ", "\t"},
                        expected: []string{},
                },
                {
                        name:     "nil input yields empty slice",
                        in:       nil,
                        expected: []string{},
                },
        }

        for _, tt := range tests {
                t.Run(tt.name, func(t *testing.T) {
                        result := nonEmpty(tt.in)
                        if len(result) != len(tt.expected) {
                                t.Fatalf("nonEmpty(%v) = %v, want %v", tt.in, result, tt.expected)
                        }
                        for i := range result {
                                if result[i] != tt.expected[i] {
                                        t.Errorf("nonEmpty(%v)[%d] = %q, want %q", tt.in, i, result[i], tt.expected[i])
                                }
                        }
                })
        }
}

func TestNewEphemeralDiskFilter(t *testing.T) {
        tests := []struct {
                name         string
                pathPrefixes []string
                models       []string
                types        []string
                device       block.Device
                expected     bool
        }{
                {
                        name:         "default selection matches nvme disk",
                        pathPrefixes: []string{"/dev/nvme"},
                        models:       []string{"Microsoft NVMe Direct Disk"},
                        types:        []string{"disk"},
                        device:       block.Device{Path: "/dev/nvme0n1", Model: "Microsoft NVMe Direct Disk", Type: "disk"},
                        expected:     true,
                },
                {
                        name:         "path OR matches second prefix",
                        pathPrefixes: []string{"/dev/nvme", "/dev/sda"},
                        models:       []string{"Contoso Disk"},
                        types:        []string{"disk"},
                        device:       block.Device{Path: "/dev/sda", Model: "Contoso Disk", Type: "disk"},
                        expected:     true,
                },
                {
                        name:         "path matches no prefix is rejected",
                        pathPrefixes: []string{"/dev/nvme", "/dev/sda"},
                        models:       []string{"Contoso Disk"},
                        types:        []string{"disk"},
                        device:       block.Device{Path: "/dev/vdb", Model: "Contoso Disk", Type: "disk"},
                        expected:     false,
                },
                {
                        name:         "type OR matches second type",
                        pathPrefixes: []string{"/dev/nvme"},
                        models:       []string{"Contoso Disk"},
                        types:        []string{"disk", "loop"},
                        device:       block.Device{Path: "/dev/nvme0n1", Model: "Contoso Disk", Type: "loop"},
                        expected:     true,
                },
                {
                        name:         "model mismatch rejected despite matching path and type",
                        pathPrefixes: []string{"/dev/nvme"},
                        models:       []string{"Contoso Disk"},
                        types:        []string{"disk"},
                        device:       block.Device{Path: "/dev/nvme0n1", Model: "Intel Disk", Type: "disk"},
                        expected:     false,
                },
                {
                        name:         "whitespace-only entries are ignored within a category",
                        pathPrefixes: []string{"  ", "/dev/nvme"},
                        models:       []string{"Contoso Disk"},
                        types:        []string{"disk"},
                        device:       block.Device{Path: "/dev/nvme0n1", Model: "Contoso Disk", Type: "disk"},
                        expected:     true,
                },
                {
                        name:         "empty model category is not filtered",
                        pathPrefixes: []string{"/dev/nvme"},
                        models:       []string{},
                        types:        []string{"disk"},
                        device:       block.Device{Path: "/dev/nvme0n1", Model: "Any Model At All", Type: "disk"},
                        expected:     true,
                },
                {
                        name:         "empty model category still enforces path and type",
                        pathPrefixes: []string{"/dev/nvme"},
                        models:       []string{},
                        types:        []string{"disk"},
                        device:       block.Device{Path: "/dev/sda", Model: "Any Model At All", Type: "disk"},
                        expected:     false,
                },
                {
                        name:         "all categories empty matches any device",
                        pathPrefixes: []string{},
                        models:       []string{},
                        types:        []string{},
                        device:       block.Device{Path: "/dev/anything", Model: "whatever", Type: "part"},
                        expected:     true,
                },
        }

        for _, tt := range tests {
                t.Run(tt.name, func(t *testing.T) {
                        filter := NewEphemeralDiskFilter(tt.pathPrefixes, tt.models, tt.types)
                        result := filter.Match(tt.device)
                        if result != tt.expected {
                                t.Errorf("Match(%v) = %v, want %v", tt.device, result, tt.expected)
                        }
                })
        }
}

