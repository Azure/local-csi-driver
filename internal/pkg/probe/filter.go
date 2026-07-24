// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package probe

import (
	"strings"

	"local-csi-driver/internal/pkg/block"
)

// Default disk selection values. These preserve the historical hardcoded
// behavior when no overrides are provided via CLI flags.
var (
	DefaultDiskPathPrefixes = []string{"/dev/nvme"}
	DefaultDiskModels       = []string{
		"Microsoft NVMe Direct Disk",
		"Microsoft NVMe Direct Disk v2",
		"Amazon EC2 NVMe Instance Storage",
	}
	DefaultDiskTypes = []string{"disk"}
)

// NewEphemeralDiskFilter builds a Filter from the built-in defaults plus any
// addon path prefixes, models, and types (for example, sourced from CLI flags).
// A device must match all three categories (path AND model AND type); within a
// category it matches if it satisfies any entry. Addon entries are appended to
// the defaults; empty/whitespace entries and duplicates are ignored.
func NewEphemeralDiskFilter(addonPathPrefixes, addonModels, addonTypes []string) *Filter {
	pathPrefixes := appendUnique(DefaultDiskPathPrefixes, addonPathPrefixes)
	models := appendUnique(DefaultDiskModels, addonModels)
	types := appendUnique(DefaultDiskTypes, addonTypes)

	anyPath := make([]FilterPredicate, 0, len(pathPrefixes))
	for _, p := range pathPrefixes {
		anyPath = append(anyPath, &PathFilter{Path: p})
	}

	anyType := make([]FilterPredicate, 0, len(types))
	for _, t := range types {
		anyType = append(anyType, &TypeFilter{Type: t})
	}

	return &Filter{Filters: []FilterPredicate{
		&anyFilter{filters: anyPath},
		NewModelFilter(models...),
		&anyFilter{filters: anyType},
	}}
}

// appendUnique returns base followed by the entries of addon that are not
// already present. Entries are trimmed, empty entries are dropped and
// duplicates are removed.
func appendUnique(base, addon []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(base)+len(addon))
	for _, list := range [][]string{base, addon} {
		for _, s := range list {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if _, ok := seen[s]; ok {
				continue
			}
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}

// anyFilter matches if any of its contained predicates match (OR).
type anyFilter struct {
	filters []FilterPredicate
}

func (f *anyFilter) Match(device block.Device) bool {
	for _, filter := range f.filters {
		if filter.Match(device) {
			return true
		}
	}
	return false
}

// FilterPredicate defines a predicate for filtering devices.
type FilterPredicate interface {
	Match(device block.Device) bool
}

// Filter holds multiple filters and matches if all contained filters match.
type Filter struct {
	Filters []FilterPredicate
}

func (f *Filter) Match(device block.Device) bool {
	for _, filter := range f.Filters {
		if !filter.Match(device) {
			return false
		}
	}
	return true
}

// PathFilter matches devices by path prefix.
type PathFilter struct {
	Path string
}

func (f *PathFilter) Match(device block.Device) bool {
	return strings.HasPrefix(device.Path, f.Path)
}

// TypeFilter matches devices by type.
type TypeFilter struct {
	Type string
}

func (f *TypeFilter) Match(device block.Device) bool {
	return strings.EqualFold(device.Type, f.Type)
}

func NewModelFilter(models ...string) *ModelFilter {
	modelsMap := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		model = strings.ToLower(model)
		modelsMap[model] = struct{}{}
	}
	return &ModelFilter{models: modelsMap}
}

// ModelFilter matches devices by model.
type ModelFilter struct {
	models map[string]struct{}
}

func (f *ModelFilter) Match(device block.Device) bool {
	deviceModel := strings.TrimSpace(device.Model)
	deviceModel = strings.ToLower(deviceModel)
	_, exists := f.models[deviceModel]
	return exists
}
