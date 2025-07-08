// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

/* SPDX-License-Identifier: Apache-2.0
 *
 * Copyright 2023 Damian Peckett <damian@pecke.tt>.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package lvm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"os/exec"
	"strings"

	"github.com/dpeckett/args"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	sysutils "local-csi-driver/internal/pkg/block"
	"local-csi-driver/internal/pkg/telemetry"
)

var (
	// ErrUnsupportedFormat is returned when the lvm format is not supported.
	ErrUnsupportedFormat = errors.New("lvm2 not supported")

	// ErrInvalidInput is returned when the input is invalid.
	ErrInvalidInput = errors.New("invalid input")

	// ErrNotFound is returned when the lvm object is not found.
	ErrNotFound = errors.New("not found")

	// AlreadyExists is returned when the lvm object already exists.
	ErrAlreadyExists = errors.New("already exists")

	// ErrTooMany is returned when multiple objects are found with the same
	// name, when only one is expected. This should never happen.
	ErrTooMany = errors.New("multiple objects with the same name")

	// ErrPVMissing is returned when the physical volume is configured in LVM,.
	ErrPVMissing = errors.New("physical volume is missing")

	// ErrInUse is returned when the logical volume is in use.
	ErrInUse = errors.New("in use")

	// ErrResourceExhausted is returned when the resource is exhausted.
	ErrResourceExhausted = errors.New("resource exhausted")

	// ErrPVAlreadyInVolumeGroup is returned when the physical volume is already in a
	// volume group.
	ErrPVAlreadyInVolumeGroup = errors.New("physical volume already in volume group")
)

// IgnoreNotFound returns nil if the error is ErrNotFound.
func IgnoreNotFound(err error) error {
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

type Client struct {
	sysutils sysutils.Interface
	lvmPath  string
	tracer   trace.Tracer
}

// Construct a new lvm2 client.
func NewClient(opts ...ClientOption) *Client {
	c := &Client{
		lvmPath: "/sbin/lvm",
		tracer:  telemetry.NewNoopTracerProvider().Tracer("localdisk.csi.acstor.io/internal/pkg/lvm"),
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// IsSupported returns true if lvm2 is supported on the current node.
func (c *Client) IsSupported() bool {
	cmdArgs := []string{"formats"}

	output, err := c.run(context.Background(), cmdArgs...)
	if err != nil {
		return false
	}

	// Check if the output contains "lvm2".
	if strings.Contains(string(output), SupportedFormat) {
		return true
	}

	return false
}

// Get physical volume/s information specified by name.
func (c *Client) GetPhysicalVolume(ctx context.Context, pvName string) (*PhysicalVolume, error) {
	ctx, span := c.tracer.Start(ctx, "lvm/GetPhysicalVolume", trace.WithAttributes(
		attribute.String("pv.name", pvName),
	))
	defer span.End()

	if pvName == "" {
		return nil, fmt.Errorf("physical volume name cannot be empty")
	}
	opts := ListPVOptions{Names: []string{pvName}}
	pvs, err := c.ListPhysicalVolumes(ctx, &opts)
	if err != nil {
		return nil, getErrorType(err)
	}

	switch {
	case len(pvs) <= 0:
		return nil, ErrNotFound
	case len(pvs) > 1:
		return nil, ErrTooMany
	default:
		span.AddEvent("found physical volume", trace.WithAttributes(
			attribute.String("pv.name", pvs[0].Name),
		))
		return &pvs[0], nil
	}
}

// Display attributes of a physical volume/s.
func (c *Client) ListPhysicalVolumes(ctx context.Context, opts *ListPVOptions) ([]PhysicalVolume, error) {
	ctx, span := c.tracer.Start(ctx, "lvm/ListPhysicalVolumes")
	defer span.End()

	cmdArgs := []string{"pvs", "--reportformat=json", "--binary", "--options=pv_all,vg_name", "--units=b"}
	if opts != nil {
		span.SetAttributes(
			attribute.StringSlice("pv.names", opts.Names),
		)
		cmdArgs = append(cmdArgs, args.Marshal(opts)...)
	}

	reportJSON, err := c.run(ctx, cmdArgs...)
	if err != nil {
		return nil, err
	}

	var report struct {
		Report []struct {
			PV []PhysicalVolume `json:"pv"`
		} `json:"report"`
	}
	if err := json.Unmarshal(reportJSON, &report); err != nil {
		return nil, fmt.Errorf("failed to parse lvm output: %w", err)
	}

	if len(report.Report) > 0 && len(report.Report[0].PV) > 0 {
		span.AddEvent("found physical volumes", trace.WithAttributes(
			attribute.Int("pv.count", len(report.Report[0].PV)),
		))
		return report.Report[0].PV, nil
	}

	return nil, nil
}

// Create a new physical volume on a device.
// The device must be unformatted. If the device is already formatted, an error will be returned.
// Assumes that the opts.Name is a block device and the path is absolute.
func (c *Client) CreatePhysicalVolume(ctx context.Context, opts CreatePVOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/CreatePhysicalVolume", trace.WithAttributes(
		attribute.String("pv.name", opts.Name),
	))
	defer span.End()

	_, err := c.sysutils.IsBlkDevUnformatted(opts.Name)
	if err != nil {
		return getErrorType(err)
	}

	// We won't provide --yes to the pvcreate command as we want to confirm the creation of the PV.
	// This can be dangerous in production environments, as it overwrites existing filesystem.
	cmdArgs := []string{"pvcreate"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err = c.run(ctx, cmdArgs...)
	if err != nil {
		return getErrorType(err)
	}
	return nil
}

// Change physical volume attributes.
func (c *Client) UpdatePhysicalVolume(ctx context.Context, opts UpdatePVOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/UpdatePhysicalVolume", trace.WithAttributes(
		attribute.String("pv.name", opts.Name),
	))
	defer span.End()

	cmdArgs := []string{"pvchange", "--yes"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err := c.run(ctx, cmdArgs...)
	if err != nil {
		return getErrorType(err)
	}
	return nil
}

// Remove a physical volume from a device.
func (c *Client) RemovePhysicalVolume(ctx context.Context, opts RemovePVOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/RemovePhysicalVolume", trace.WithAttributes(
		attribute.String("pv.name", opts.Name),
	))
	defer span.End()

	cmdArgs := []string{"pvremove", "--yes"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err := c.run(ctx, cmdArgs...)
	if err != nil {
		return getErrorType(err)
	}
	return nil
}

// Check / repair physical volume metadata.
func (c *Client) CheckPhysicalVolume(ctx context.Context, opts CheckPVOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/CheckPhysicalVolume", trace.WithAttributes(
		attribute.String("pv.name", opts.Name),
	))
	defer span.End()

	cmdArgs := []string{"pvck", "--yes"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err := c.run(ctx, cmdArgs...)
	return err
}

// Move extents from one physical volume to another.
func (c *Client) MovePhysicalExtents(ctx context.Context, opts MovePEOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/MovePhysicalExtents", trace.WithAttributes(
		attribute.String("lv.name", opts.LVName),
	))
	defer span.End()

	cmdArgs := []string{"pvmove", "--yes"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err := c.run(ctx, cmdArgs...)
	return err
}

// Resize a physical volume.
func (c *Client) ResizePhysicalVolume(ctx context.Context, opts ResizePVOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/ResizePhysicalVolume", trace.WithAttributes(
		attribute.String("lv.name", opts.Name),
	))
	defer span.End()

	cmdArgs := []string{"pvresize", "--yes"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err := c.run(ctx, cmdArgs...)
	return err
}

// Get volume group information specified by name.
func (c *Client) GetVolumeGroup(ctx context.Context, vgName string) (*VolumeGroup, error) {
	ctx, span := c.tracer.Start(ctx, "lvm/GetVolumeGroup", trace.WithAttributes(
		attribute.String("vg.name", vgName),
	))
	defer span.End()

	if vgName == "" {
		return nil, fmt.Errorf("volume group name cannot be empty")
	}

	opts := ListVGOptions{Names: []string{vgName}}
	vgs, err := c.ListVolumeGroups(ctx, &opts)
	if err != nil {
		return nil, getErrorType(err)
	}

	switch {
	case len(vgs) <= 0:
		return nil, ErrNotFound
	case len(vgs) > 1:
		return nil, ErrTooMany
	default:
		return &vgs[0], nil
	}
}

// Display volume group/s information.
func (c *Client) ListVolumeGroups(ctx context.Context, opts *ListVGOptions) ([]VolumeGroup, error) {
	ctx, span := c.tracer.Start(ctx, "lvm/ListVolumeGroups")
	defer span.End()

	cmdArgs := []string{"vgs", "--reportformat=json", "--binary", "--options=vg_all", "--units=b"}
	if opts != nil {
		span.SetAttributes(
			attribute.StringSlice("vg.names", opts.Names),
		)
		cmdArgs = append(cmdArgs, args.Marshal(opts)...)
	}

	reportJSON, err := c.run(ctx, cmdArgs...)
	if err != nil {
		return nil, err
	}

	var report struct {
		Report []struct {
			VG []VolumeGroup `json:"vg"`
		} `json:"report"`
	}
	if err := json.Unmarshal(reportJSON, &report); err != nil {
		return nil, fmt.Errorf("failed to parse lvm output: %w", err)
	}

	if len(report.Report) > 0 && len(report.Report[0].VG) > 0 {
		span.AddEvent("found volume groups", trace.WithAttributes(
			attribute.Int("vg.count", len(report.Report[0].VG)),
		))
		return report.Report[0].VG, nil
	}

	return nil, nil
}

// Create a new volume group.
func (c *Client) CreateVolumeGroup(ctx context.Context, opts CreateVGOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/CreateVolumeGroup", trace.WithAttributes(
		attribute.String("vg.name", opts.Name),
		attribute.StringSlice("pv.names", opts.PVNames),
	))
	defer span.End()

	cmdArgs := []string{"vgcreate", "--yes"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err := c.run(ctx, cmdArgs...)
	if err != nil {
		return getErrorType(err)
	}
	return nil
}

// Change volume group attributes.
func (c *Client) UpdateVolumeGroup(ctx context.Context, opts UpdateVGOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/UpdateVolumeGroup", trace.WithAttributes(
		attribute.String("vg.name", opts.Name),
	))
	defer span.End()

	cmdArgs := []string{"vgchange", "--yes"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err := c.run(ctx, cmdArgs...)
	return err
}

// Remove a volume group.
func (c *Client) RemoveVolumeGroup(ctx context.Context, opts RemoveVGOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/RemoveVolumeGroup", trace.WithAttributes(
		attribute.String("vg.name", opts.Name),
	))
	defer span.End()

	cmdArgs := []string{"vgremove", "--yes"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err := c.run(ctx, cmdArgs...)
	if err != nil {
		return getErrorType(err)
	}
	return nil
}

// Check / repair volume group metadata.
func (c *Client) CheckVolumeGroup(ctx context.Context, opts CheckVGOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/CheckVolumeGroup", trace.WithAttributes(
		attribute.String("vg.name", opts.Name),
	))
	defer span.End()

	cmdArgs := []string{"vgck", "--yes"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err := c.run(ctx, cmdArgs...)
	return err
}

// Unregister a volume group from the system.
func (c *Client) ExportVolumeGroup(ctx context.Context, opts ExportVGOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/ExportVolumeGroup", trace.WithAttributes(
		attribute.String("vg.name", opts.Name),
	))
	defer span.End()

	cmdArgs := []string{"vgexport", "--yes"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err := c.run(ctx, cmdArgs...)
	return err
}

// Register a volume group with the system.
func (c *Client) ImportVolumeGroup(ctx context.Context, opts ImportVGOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/ImportVolumeGroup", trace.WithAttributes(
		attribute.String("vg.name", opts.Name),
	))
	defer span.End()

	cmdArgs := []string{"vgimport", "--yes"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err := c.run(ctx, cmdArgs...)
	return err
}

// Import a volume group from cloned physical volumes.
func (c *Client) ImportVolumeGroupFromCloned(ctx context.Context, opts ImportVGFromClonedOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/ImportVolumeGroupFromCloned", trace.WithAttributes(
		attribute.String("vg.name", opts.Name),
	))
	defer span.End()

	cmdArgs := []string{"vgimportclone", "--yes"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err := c.run(ctx, cmdArgs...)
	return err
}

// Merge volume groups.
func (c *Client) MergeVolumeGroups(ctx context.Context, opts MergeVGOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/ImportVolumeGroupFromCloned", trace.WithAttributes(
		attribute.String("vg.src", opts.Source),
		attribute.String("vg.dst", opts.Destination),
	))
	defer span.End()

	cmdArgs := []string{"vgmerge", "--yes"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err := c.run(ctx, cmdArgs...)
	return err
}

// Add physical volumes to a volume group.
func (c *Client) ExtendVolumeGroup(ctx context.Context, opts ExtendVGOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/ExtendVolumeGroup", trace.WithAttributes(
		attribute.String("vg.name", opts.Name),
	))
	defer span.End()

	cmdArgs := []string{"vgextend", "--yes"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err := c.run(ctx, cmdArgs...)
	return err
}

// Remove physical volumes from a volume group.
func (c *Client) ReduceVolumeGroup(ctx context.Context, opts ReduceVGOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/ReduceVolumeGroup", trace.WithAttributes(
		attribute.String("vg.name", opts.Name),
	))
	defer span.End()

	cmdArgs := []string{"vgreduce", "--yes"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err := c.run(ctx, cmdArgs...)
	return err
}

// Rename a volume group.
func (c *Client) RenameVolumeGroup(ctx context.Context, opts RenameVGOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/RenameVolumeGroup", trace.WithAttributes(
		attribute.String("vg.src", opts.From),
		attribute.String("vg.dst", opts.To),
	))
	defer span.End()

	cmdArgs := []string{"vgrename", "--yes"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err := c.run(ctx, cmdArgs...)
	return err
}

// Move physical volumes between volume groups.
func (c *Client) MovePhysicalVolumes(ctx context.Context, opts MovePVOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/MovePhysicalVolumes", trace.WithAttributes(
		attribute.String("vg.src", opts.Source),
		attribute.String("vg.dst", opts.Destination),
	))
	defer span.End()

	cmdArgs := []string{"vgsplit", "--yes"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err := c.run(ctx, cmdArgs...)
	return err
}

// Create device files for active logical volumes in the volume group.
func (c *Client) MakeVolumeGroupDeviceNodes(ctx context.Context, opts MakeVGDeviceNodesOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/MakeVolumeGroupDeviceNodes", trace.WithAttributes(
		attribute.String("vg.name", opts.Name),
	))
	defer span.End()

	cmdArgs := []string{"vgmknodes", "--yes"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err := c.run(ctx, cmdArgs...)
	return err
}

// Get logical volume/s information specified by name.
func (c *Client) GetLogicalVolume(ctx context.Context, vgName string, lvName string) (*LogicalVolume, error) {
	ctx, span := c.tracer.Start(ctx, "lvm/GetLogicalVolume", trace.WithAttributes(
		attribute.String("lv.name", lvName),
		attribute.String("vg.name", vgName),
	))
	defer span.End()

	vgLvName := fmt.Sprintf("%s/%s", vgName, lvName)
	if vgName == "" || lvName == "" {
		return nil, fmt.Errorf("volume group/logical volume name cannot be empty: %s", vgLvName)
	}
	opts := ListLVOptions{Names: []string{vgLvName}}
	lvs, err := c.ListLogicalVolumes(ctx, &opts)
	if err != nil {
		return nil, getErrorType(err)
	}

	switch {
	case len(lvs) <= 0:
		return nil, ErrNotFound
	case len(lvs) > 1:
		return nil, ErrTooMany
	default:
		return &lvs[0], nil
	}
}

// Display logical volume/s information.
func (c *Client) ListLogicalVolumes(ctx context.Context, opts *ListLVOptions) ([]LogicalVolume, error) {
	ctx, span := c.tracer.Start(ctx, "lvm/ListLogicalVolumes")
	defer span.End()

	// Note: seg_all option will include all segments of the logical volume and
	// the same logical volume may be listed multiple times. Do not use seg_all
	// option in this case.
	cmdArgs := []string{"lvs", "--reportformat=json", "--binary", "--options=lv_all,vg_name", "--units=b"}
	if opts != nil {
		span.SetAttributes(
			attribute.StringSlice("lv.names", opts.Names),
		)
		cmdArgs = append(cmdArgs, args.Marshal(opts)...)
	}

	reportJSON, err := c.run(ctx, cmdArgs...)
	if err != nil {
		return nil, err
	}

	var report struct {
		Report []struct {
			LV []LogicalVolume `json:"lv"`
		} `json:"report"`
	}
	if err := json.Unmarshal(reportJSON, &report); err != nil {
		return nil, fmt.Errorf("failed to parse lvm output: %w", err)
	}

	if len(report.Report) > 0 && len(report.Report[0].LV) > 0 {
		span.AddEvent("found logical volumes", trace.WithAttributes(
			attribute.Int("lv.count", len(report.Report[0].LV)),
		))
		return report.Report[0].LV, nil
	}

	return nil, nil
}

// Create a new logical volume in a volume group.
func (c *Client) CreateLogicalVolume(ctx context.Context, opts CreateLVOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/CreateLogicalVolume", trace.WithAttributes(
		attribute.String("lv.name", opts.Name),
		attribute.String("vg.name", opts.VGName),
		attribute.String("lv.size", opts.Size),
	))
	defer span.End()

	cmdArgs := []string{"lvcreate", "--yes"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err := c.run(ctx, cmdArgs...)
	if err != nil {
		return getErrorType(err)
	}
	return nil
}

// Change logical volume attributes.
func (c *Client) UpdateLogicalVolume(ctx context.Context, opts UpdateLVOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/UpdateLogicalVolume", trace.WithAttributes(
		attribute.String("lv.name", opts.Name),
	))
	defer span.End()

	cmdArgs := []string{"lvchange", "--yes"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err := c.run(ctx, cmdArgs...)
	return err
}

// Remove a logical volume.
// Make sure include the name of the volume group in the name of the logical volume.
// For example, "vg1/lv1".
func (c *Client) RemoveLogicalVolume(ctx context.Context, opts RemoveLVOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/RemoveLogicalVolume", trace.WithAttributes(
		attribute.String("lv.name", opts.Name),
	))
	defer span.End()

	// Check if opts.Name is in the format "vgname/lvname"
	parts := strings.Split(opts.Name, "/")
	if len(parts) != 2 {
		return ErrInvalidInput
	}

	cmdArgs := []string{"lvremove", "--yes"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err := c.run(ctx, cmdArgs...)
	if err != nil {
		return getErrorType(err)
	}
	return err
}

// Change logical volume layout.
func (c *Client) ConvertLogicalVolumeLayout(ctx context.Context, opts ConvertLVLayoutOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/ConvertLogicalVolumeLayout", trace.WithAttributes(
		attribute.String("lv.name", opts.Name),
	))
	defer span.End()

	cmdArgs := []string{"lvconvert", "--yes"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err := c.run(ctx, cmdArgs...)
	return err
}

// Add space to a logical volume.
func (c *Client) ExtendLogicalVolume(ctx context.Context, opts ExtendLVOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/ExtendLogicalVolume", trace.WithAttributes(
		attribute.String("lv.name", opts.Name),
		attribute.String("lv.size", opts.Size),
	))
	defer span.End()

	lvPath := "/dev/" + opts.Name
	unformatted, err := c.sysutils.IsBlkDevUnformatted(lvPath)
	if err != nil {
		return err
	}

	// If the logical volume is unformatted, we cannot resize the filesystem.
	// lvextend will fail looking for a filesystem to resize.
	if opts.ResizeFS && unformatted {
		opts.ResizeFS = false
	}

	cmdArgs := []string{"lvextend", "--yes"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err = c.run(ctx, cmdArgs...)
	if err != nil {
		return getErrorType(err)
	}
	return nil
}

// Reduce the size of a logical volume.
func (c *Client) ReduceLogicalVolume(ctx context.Context, opts ReduceLVOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/ReduceLogicalVolume", trace.WithAttributes(
		attribute.String("lv.name", opts.Name),
		attribute.String("lv.size", opts.Size),
	))
	defer span.End()

	cmdArgs := []string{"lvreduce", "--yes"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err := c.run(ctx, cmdArgs...)
	return err
}

// Rename a logical volume.
func (c *Client) RenameLogicalVolume(ctx context.Context, opts RenameLVOptions) error {
	ctx, span := c.tracer.Start(ctx, "lvm/ReduceLogicalVolume", trace.WithAttributes(
		attribute.String("lv.src", opts.From),
		attribute.String("lv.dst", opts.To),
	))
	defer span.End()

	cmdArgs := []string{"lvrename", "--yes"}
	cmdArgs = append(cmdArgs, args.Marshal(opts)...)

	_, err := c.run(ctx, cmdArgs...)
	return err
}

func (c *Client) run(ctx context.Context, cmdArgs ...string) ([]byte, error) {
	ctx, span := c.tracer.Start(ctx, "lvm/run", trace.WithAttributes(
		attribute.String("cmd.name", c.lvmPath),
		attribute.StringSlice("cmd.args", cmdArgs),
	))
	defer span.End()

	cmd := exec.CommandContext(ctx, c.lvmPath, cmdArgs...)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	defer func(stdout, stderr *bytes.Buffer) {
		span.SetAttributes(
			attribute.String("cmd.stdout", strings.TrimSpace(stdout.String())),
			attribute.String("cmd.stderr", strings.TrimSpace(stderr.String())),
		)
	}(&stdout, &stderr)

	if err := cmd.Run(); err != nil {
		// Let caller decide whether to set span status to error since it may retry.
		span.RecordError(err)
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}

	span.SetStatus(codes.Ok, "lvm command succeeded")
	return stdout.Bytes(), nil
}

// getErrorType determines the error type based on the error message.
//
// LVM error messages are inconsistent, so parsing is required to identify
// errors.
//
// Example scenarios during GetLogicalVolume:
//  1. Missing VG: "Volume group 'NoVolumeGroup' not found".
//  2. Missing LV: "Failed to find logical volume 'NoLogicalVolume' in volume
//     group 'NoVolumeGroup'".
//
// Example scenarios during GetPhysicalVolume:
// Missing PV: Cannot use /dev/noPhyDev device not found
//
// Example scenarios during GetVolumeGroup:
// Missing VG: Volume group 'NoVolumeGroup'  not found.
func getErrorType(err error) error {
	switch {
	case containsIgnoreCase(err.Error(), "failed to find"),
		containsIgnoreCase(err.Error(), "not found"):
		return fmt.Errorf("%w: %s", ErrNotFound, err.Error())
	case containsIgnoreCase(err.Error(), "contains a filesystem in use"):
		return fmt.Errorf("%w: %s", ErrInUse, err.Error())
	case containsIgnoreCase(err.Error(), "already exists"):
		return fmt.Errorf("%w: %s", ErrAlreadyExists, err.Error())
	case containsIgnoreCase(err.Error(), "insufficient free space"):
		return fmt.Errorf("%w: %s", ErrResourceExhausted, err.Error())
	case containsIgnoreCase(err.Error(), "is already in volume group"):
		return fmt.Errorf("%w: %s", ErrPVAlreadyInVolumeGroup, err.Error())
	case containsIgnoreCase(err.Error(), "device has a signature"):
		return fmt.Errorf("%w: %s", ErrInUse, err.Error())
	default:
		return err
	}
}

// containsIgnoreCase checks if a string contains a substring ignoring case.
func containsIgnoreCase(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
