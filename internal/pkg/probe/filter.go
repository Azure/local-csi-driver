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
	DefaultDiskModels       = []string{"Microsoft NVMe Direct Disk", "Microsoft NVMe Direct Disk v2"}
	DefaultDiskTypes        = []string{"disk"}
)

// EphemeralDiskFilter is a filter for ephemeral disks using default values.
var EphemeralDiskFilter = NewEphemeralDiskFilter(DefaultDiskPathPrefixes, DefaultDiskModels, DefaultDiskTypes)

// NewEphemeralDiskFilter builds a Filter from the given path prefixes, models,
// and types. A device must match all non-empty categories (path AND model AND
// type); within a category it matches if it satisfies any entry. Empty or
// whitespace-only entries are ignored, and an empty category is skipped so it
// does not filter anything out.
func NewEphemeralDiskFilter(pathPrefixes, models, types []string) *Filter {
	filters := make([]FilterPredicate, 0, 3)

	if paths := nonEmpty(pathPrefixes); len(paths) > 0 {
		anyPath := make([]FilterPredicate, 0, len(paths))
		for _, p := range paths {
			anyPath = append(anyPath, &PathFilter{Path: p})
		}
		filters = append(filters, &anyFilter{filters: anyPath})
	}

	if m := nonEmpty(models); len(m) > 0 {
		filters = append(filters, NewModelFilter(m...))
	}

	if ts := nonEmpty(types); len(ts) > 0 {
		anyType := make([]FilterPredicate, 0, len(ts))
		for _, t := range ts {
			anyType = append(anyType, &TypeFilter{Type: t})
		}
		filters = append(filters, &anyFilter{filters: anyType})
	}

	return &Filter{Filters: filters}
}

// nonEmpty returns the input with whitespace trimmed and empty entries removed.
func nonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
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
