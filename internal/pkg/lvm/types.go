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
	"encoding/json"
	"strconv"
	"strings"
)

// BoolString is a JSON type that treats "1" as true and "0" as false.
type BoolString bool

func (b *BoolString) UnmarshalJSON(data []byte) error {
	var v string
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}

	switch v {
	case "1":
		*b = true
	default:
		*b = false
	}

	return nil
}

// IntString is a JSON type for strings that are actually integers.
type IntString int

func (i *IntString) UnmarshalJSON(data []byte) error {
	var v string
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}

	if v == "" {
		*i = 0
	} else {
		ival, err := strconv.Atoi(v)
		if err != nil {
			return err
		}
		*i = IntString(ival)
	}

	return nil
}

// Int64String is a custom type for handling int64 values represented as strings in JSON.
type Int64String int64

// UnmarshalJSON implements the json.Unmarshaler interface.
func (i *Int64String) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}

	s = strings.TrimSuffix(s, "B")
	value, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return err
	}
	*i = Int64String(value)
	return nil
}

// YesNo is a boolean type that marshals to "y" or "n".
type YesNo bool

var (
	Yes = new(YesNo(true))
	No  = new(YesNo(false))
)

func (yn *YesNo) MarshalArg() string {
	if *yn {
		return "y"
	}
	return "n"
}

// PhysicalVolume represents an LVM2 Physical Volume (PV).
type PhysicalVolume struct {
	Format                 string     `json:"pv_fmt"`            // Type of metadata.
	UUID                   string     `json:"pv_uuid"`           // Unique identifier of the PV.
	DeviceSize             string     `json:"dev_size"`          // Size of the underlying device in current units.
	Name                   string     `json:"pv_name"`           // Name of the PV.
	Major                  IntString  `json:"pv_major"`          // Device major number.
	Minor                  IntString  `json:"pv_minor"`          // Device minor number.
	MetadataFree           string     `json:"pv_mda_free"`       // Free metadata area space on this device in current units.
	MetadataSize           string     `json:"pv_mda_size"`       // Size of smallest metadata area on this device in current units.
	HeaderExtensionVersion IntString  `json:"pv_ext_vsn"`        // PV header extension version.
	ExtentStart            string     `json:"pe_start"`          // Offset to start of data on the underlying device.
	Size                   string     `json:"pv_size"`           // Size of the physical volume in current units.
	FreeSpace              string     `json:"pv_free"`           // Total unallocated space in current units.
	UsedSpace              string     `json:"pv_used"`           // Total allocated space in current units.
	Attributes             string     `json:"pv_attr"`           // Various attributes of the PV.
	Allocatable            BoolString `json:"pv_allocatable"`    // Indicates if device can be used for allocation.
	Exported               BoolString `json:"pv_exported"`       // Indicates if device is exported.
	Missing                BoolString `json:"pv_missing"`        // Indicates if device is missing in system.
	ExtentCount            IntString  `json:"pv_pe_count"`       // Total number of Physical Extents.
	ExtentAllocCount       IntString  `json:"pv_pe_alloc_count"` // Total number of allocated Physical Extents.
	Tags                   string     `json:"pv_tags"`           // Tags associated with the physical volume, if any.
	MetadataCount          IntString  `json:"pv_mda_count"`      // Number of metadata areas on this device.
	MetadataUsedCount      IntString  `json:"pv_mda_used_count"` // Number of metadata areas in use on this device.
	BootloaderAreaStart    string     `json:"pv_ba_start"`       // Offset to start of PV Bootloader Area on device in current units.
	BootloaderAreaSize     string     `json:"pv_ba_size"`        // Size of the PV Bootloader Area in current units.
	InUse                  BoolString `json:"pv_in_use"`         // Indicates if the physical volume is used.
	Duplicate              BoolString `json:"pv_duplicate"`      // Indicates if PV is an unchosen duplicate.
	DeviceID               string     `json:"pv_device_id"`      // Device ID such as the WWID.
	DeviceIDType           string     `json:"pv_device_id_type"` // Type of the device ID, such as WWID.
	VGName                 string     `json:"vg_name"`           // Name of the VG the PV belongs to.
}

// ListPVOptions provides options for listing PVs (pvs).
type ListPVOptions struct {
	CommonOptions
	Names                []string `arg:"0"`                    // Specific PVs to display.
	All                  bool     `arg:"all"`                  // Display devices not initialized by LVM.
	Select               string   `arg:"select"`               // Filters objects based on criteria.
	Foreign              bool     `arg:"foreign"`              // Lists foreign VGs.
	IgnoreLockingFailure bool     `arg:"ignorelockingfailure"` // Whether to proceed in read-only mode after lock failures.
	ReadOnly             bool     `arg:"readonly"`             // Read metadata without locks.
	Shared               bool     `arg:"shared"`               // Displays shared VGs without active lvmlockd.
}

// CreatePVOptions provides options for creating PVs (pvcreate).
type CreatePVOptions struct {
	CommonOptions
	Name                  string `arg:"0"`                     // Device or PV to create.
	Force                 bool   `arg:"force"`                 // Override checks and protections.
	UUID                  string `arg:"uuid"`                  // Specific UUID for the device.
	Zero                  *YesNo `arg:"zero"`                  // Wipe first 4 sectors of the device unless RestoreFile or UUID is given.
	DataAlignment         string `arg:"dataalignment"`         // Align PV data's start, may be shifted by DataAlignmentOffset.
	DataAlignmentOffset   string `arg:"dataalignmentoffset"`   // Additional shift for PV data's start.
	BootloaderAreaSize    string `arg:"bootloaderareasize"`    // Reserved space for the bootloader.
	LabelSector           *int   `arg:"labelsector"`           // Sector for the LVM2 identifier.
	MetadataCopies        *int   `arg:"pvmetadatacopies"`      // Number of metadata areas on a PV.
	MetadataSize          string `arg:"metadatasize"`          // Space for each VG metadata area.
	MetadataIgnore        *YesNo `arg:"metadataignore"`        // If set, metadata won't be stored on the PV.
	NoRestoreFile         bool   `arg:"norestorefile"`         // Specify UUID without a metadata backup.
	SetPhysicalVolumeSize string `arg:"setphysicalvolumesize"` // Manually set the PV size.
	RestoreFile           string `arg:"restorefile"`           // Align physical extents based on file's content with UUID.
}

// UpdatePVOptions provides options to modify PVs (pvchange).
type UpdatePVOptions struct {
	CommonOptions
	Name           string   `arg:"0"`              // Device or PV to modify.
	Force          bool     `arg:"force"`          // Override checks and protections.
	AutoBackup     *YesNo   `arg:"autobackup"`     // Auto backup metadata after changes.
	All            bool     `arg:"all"`            // Modify all visible PVs.
	Allocatable    *YesNo   `arg:"allocatable"`    // Toggle physical extents allocation.
	UUID           bool     `arg:"uuid"`           // Generate a new UUID for the PV.
	AddTags        []string `arg:"addtag"`         // Add tag/s to the PV.
	DelTags        []string `arg:"deltag"`         // Remove tag/s from the PV.
	MetadataIgnore *YesNo   `arg:"metadataignore"` // If set, metadata won't be stored on the PV.
	Select         string   `arg:"select"`         // Filter objects based on criteria.
}

// RemovePVOptions provides options for removing PVs (pvremove).
type RemovePVOptions struct {
	CommonOptions
	Name  string `arg:"0"`     // Device or PV to remove.
	Force bool   `arg:"force"` // Overrides checks and protections.
}

// CheckPVOptions provides options for checking PVs (pvck).
type CheckPVOptions struct {
	CommonOptions
	Name             string   `arg:"0"`                // Device or PV to check.
	Dump             string   `arg:"dump"`             // Which header or metadata to dump.
	File             string   `arg:"file"`             // Metadata file to read or write.
	Repair           bool     `arg:"repair"`           // Repair headers and metadata.
	RepairType       string   `arg:"repairtype"`       // Repair type.
	LabelSector      *int     `arg:"labelsector"`      // Sector for the LVM2 identifier.
	PVMetadataCopies *int     `arg:"pvmetadatacopies"` // Number of metadata areas on a PV.
	Settings         []string `arg:"settings"`         // Command specific settings in `key=value` format.
}

// MovePEOptions provides options for moving PVs (pvmove).
type MovePEOptions struct {
	CommonOptions
	Source      string   `arg:"0"`          // Device or PV to move.
	Destination []string `arg:"1"`          // Device or PV to move to.
	LVName      string   `arg:"name"`       // Move only the extents belonging to the LV.
	AutoBackup  *YesNo   `arg:"autobackup"` // Auto backup metadata after changes.
	Alloc       string   `arg:"alloc"`      // Allocation policy for Physical Extents.
	Abort       bool     `arg:"abort"`      // Abort any PV move operations in progress.
	Atomic      bool     `arg:"atomic"`     // Atomic migration with mirrored temp LV; if interrupted, data remains on source PV.
	Background  bool     `arg:"background"` // Move extents in the background.
	Interval    *int     `arg:"interval"`   // Report progress at regular intervals.
	NoUdevSync  bool     `arg:"noudevsync"` // Allow operations to proceed without waiting for udev notifications.
}

// ResizePVOptions provides options for resizing PVs (pvresize).
type ResizePVOptions struct {
	CommonOptions
	Name                  string `arg:"0"`                     // Device or PV to resize.
	SetPhysicalVolumeSize string `arg:"setphysicalvolumesize"` // Manually set the PV size.
}

// VolumeGroup represents an LVM2 Volume Group (VG).
type VolumeGroup struct {
	Format             string      `json:"vg_fmt"`               // Type of metadata.
	UUID               string      `json:"vg_uuid"`              // Unique identifier.
	Name               string      `json:"vg_name"`              // Name of the VG.
	Attributes         string      `json:"vg_attr"`              // Various attributes.
	Permissions        string      `json:"vg_permissions"`       // Permissions.
	Extendable         BoolString  `json:"vg_extendable"`        // Set if VG is extendable.
	Exported           BoolString  `json:"vg_exported"`          // Set if VG is exported.
	AutoActivation     BoolString  `json:"vg_autoactivation"`    // Set if VG autoactivation is enabled.
	Partial            BoolString  `json:"vg_partial"`           // Set if VG is partial.
	AllocationPolicy   string      `json:"vg_allocation_policy"` // VG allocation policy.
	Clustered          BoolString  `json:"vg_clustered"`         // Set if VG is clustered.
	Shared             BoolString  `json:"vg_shared"`            // Set if VG is shared.
	Size               Int64String `json:"vg_size"`              // Total size of VG in current units.
	Free               Int64String `json:"vg_free"`              // Total amount of free space in current units.
	SystemID           string      `json:"vg_systemid"`          // System ID of the VG indicating which host owns it.
	LockType           string      `json:"vg_lock_type"`         // Lock type of the VG used by lvmlockd.
	LockArgs           string      `json:"vg_lock_args"`         // Lock args of the VG used by lvmlockd.
	ExtentSize         Int64String `json:"vg_extent_size"`       // Size of Physical Extents in current units.
	ExtentCount        IntString   `json:"vg_extent_count"`      // Total number of Physical Extents.
	ExtentFreeCount    IntString   `json:"vg_free_count"`        // Total number of unallocated Physical Extents.
	MaxLogicalVolumes  IntString   `json:"max_lv"`               // Maximum number of LVs allowed in VG or 0 if unlimited.
	MaxPhysicalVolumes IntString   `json:"max_pv"`               // Maximum number of PVs allowed in VG or 0 if unlimited.
	PVCount            IntString   `json:"pv_count"`             // Number of PVs in VG.
	MissingPVCount     IntString   `json:"vg_missing_pv_count"`  // Number of PVs in VG which are missing.
	LVCount            IntString   `json:"lv_count"`             // Number of LVs.
	SnapshotCount      IntString   `json:"snap_count"`           // Number of snapshots.
	SeqNo              IntString   `json:"vg_seqno"`             // Revision number of internal metadata. Incremented whenever it changes.
	Tags               string      `json:"vg_tags"`              // Tags, if any.
	Profile            string      `json:"vg_profile"`           // Configuration profile attached to this VG.
	MetadataCount      IntString   `json:"vg_mda_count"`         // Number of metadata areas on this VG.
	MetadataUsedCount  IntString   `json:"vg_mda_used_count"`    // Number of metadata areas in use on this VG.
	MetadataFree       string      `json:"vg_mda_free"`          // Free metadata area space for this VG in current units.
	MetadataSize       string      `json:"vg_mda_size"`          // Size of smallest metadata area for this VG in current units.
	MetadataCopies     string      `json:"vg_mda_copies"`        // Target number of in use metadata areas in the VG.
}

// ListVGOptions provides options for listing VGs (vgs).
type ListVGOptions struct {
	CommonOptions
	Names                []string `arg:"0"`                    // Specific VGs to display.
	Select               string   `arg:"select"`               // Filters objects based on criteria.
	Foreign              bool     `arg:"foreign"`              // Lists foreign VGs.
	IgnoreLockingFailure bool     `arg:"ignorelockingfailure"` // Whether to proceed in read-only mode after lock failures.
	ReadOnly             bool     `arg:"readonly"`             // Read metadata without locks.
	Shared               bool     `arg:"shared"`               // Displays shared VGs without active lvmlockd.
}

// CreateVGOptions provides options for creating VGs (vgcreate).
type CreateVGOptions struct {
	CommonOptions
	Name                string   `arg:"0"`                   // Name of the VG to create.
	PVNames             []string `arg:"1"`                   // List of PVs to add to the VG.
	Force               bool     `arg:"force"`               // Overrides checks and protections.
	AutoBackup          *YesNo   `arg:"autobackup"`          // Auto backup metadata after changes.
	MaxLogicalVolumes   *int     `arg:"maxlogicalvolumes"`   // Max number of LVs allowed in a VG.
	MaxPhysicalVolumes  *int     `arg:"maxphysicalvolumes"`  // Max number of PVs that can belong to the VG.
	PhysicalExtentSize  string   `arg:"physicalextentsize"`  // Extent size of PVs in the group.
	Zero                *YesNo   `arg:"zero"`                // Wipe first 4 sectors of the device.
	Tags                []string `arg:"addtag"`              // Tags to add to the VG.
	Alloc               string   `arg:"alloc"`               // Allocation policy for Physical Extents.
	LabelSector         *int     `arg:"labelsector"`         // Sector for the LVM2 identifier.
	MetadataSize        string   `arg:"metadatasize"`        // Space for each VG metadata area.
	PVMetadataCopies    *int     `arg:"pvmetadatacopies"`    // Number of metadata areas on a PV.
	VGMetadataCopies    string   `arg:"vgmetadatacopies"`    // Number of copies of VG metadata.
	DataAlignment       string   `arg:"dataalignment"`       // Align PV data's start, may be shifted by DataAlignmentOffset.
	DataAlignmentOffset string   `arg:"dataalignmentoffset"` // Additional shift for PV data's start.
	Shared              bool     `arg:"shared"`              // If set, VG is shared across multiple hosts using lvmlockd.
	SystemID            string   `arg:"systemid"`            // Specific system ID for the new VG.
	LockType            string   `arg:"locktype"`            // Directly specifies the VG lock type.
	SetAutoActivation   *YesNo   `arg:"setautoactivation"`   // Enable autoactivation for the VG.
}

// UpdateVGOptions provides options for modifying VGs (vgchange).
type UpdateVGOptions struct {
	CommonOptions
	Name                 string   `arg:"0"`                    // Name of the VG to modify.
	MaxLogicalVolumes    *int     `arg:"logicalvolume"`        // Max number of LVs allowed in a VG.
	MaxPhysicalVolumes   *int     `arg:"maxphysicalvolumes"`   // Max number of PVs that can belong to the VG.
	UUID                 bool     `arg:"uuid"`                 // Generate a new UUID for the VG.
	PhysicalExtentSize   string   `arg:"physicalextentsize"`   // Extent size of PVs in the group.
	Resizeable           *YesNo   `arg:"resizeable"`           // Toggle whether PVs can be added or removed.
	AddTags              []string `arg:"addtag"`               // Add tag/s to the VG.
	DelTags              []string `arg:"deltag"`               // Remove tag/s from the VG.
	Alloc                string   `arg:"alloc"`                // Allocation policy for Physical Extents.
	PVMetadataCopies     *int     `arg:"pvmetadatacopies"`     // Number of metadata areas on a PV.
	VGMetadataCopies     string   `arg:"vgmetadatacopies"`     // Number of copies of VG metadata.
	DetachProfile        bool     `arg:"detachprofile"`        // Detach a metadata profile.
	SetAutoActivation    *YesNo   `arg:"setautoactivation"`    // Enable autoactivation for the VG.
	AutoBackup           *YesNo   `arg:"autobackup"`           // Auto backup metadata after changes.
	Select               string   `arg:"select"`               // Filters objects based on criteria.
	Force                bool     `arg:"force"`                // Overrides checks and protections.
	Poll                 *YesNo   `arg:"poll"`                 // Resume background operations that were halted due to disruptions.
	IgnoreMonitoring     bool     `arg:"ignoremonitoring"`     // Ignore dmeventd monitoring.
	NoUdevSync           bool     `arg:"noudevsync"`           // Ignore udev notifications.
	Monitor              *YesNo   `arg:"monitor"`              // Toggle monitoring by dmeventd.
	Refresh              bool     `arg:"refresh"`              // Refreshes the VG metadata.
	Activate             *YesNo   `arg:"activate"`             // Activate the VG.
	IgnoreActivationSkip bool     `arg:"ignoreactivationskip"` // Ignore the "activation skip" flag.
	Partial              bool     `arg:"partial"`              // Attempt activation with missing Physical Extents.
	ActivationMode       string   `arg:"activationmode"`       // Conditions under which a LV can be activated with missing PVs.
	AutoActivation       string   `arg:"autoactivation"`       // Activation should occur automatically in response to specific events.
	LockType             string   `arg:"locktype"`             // Directly specifies the VG lock type.
	LockStart            bool     `arg:"lockstart"`            // Start the lockspace of a shared VG in lvmlockd.
	LockStop             bool     `arg:"lockstop"`             // Stop the lockspace of a shared VG in lvmlockd.
	SysInit              bool     `arg:"sysinit"`              // Indicates that the command is being invoked from early system init scripts.
	IgnoreLockingFailure bool     `arg:"ignorelockingfailure"` // Whether to proceed in read-only mode after lock failures.
	ReadOnly             bool     `arg:"readonly"`             // Read metadata without locks.
	SystemID             string   `arg:"systemid"`             // Changes the system ID of the VG.
}

// RemoveVGOptions are options for removing VGs (vgremove).
type RemoveVGOptions struct {
	CommonOptions
	Name       string `arg:"0"`          // Name of the VG to remove.
	Force      bool   `arg:"force"`      // Overrides checks and protections.
	Select     string `arg:"select"`     // Filters objects based on criteria.
	NoUdevSync bool   `arg:"noudevsync"` // Allow operations to proceed without waiting for udev notifications.
}

// CheckVGOptions provides options for checking VGs (vgck).
type CheckVGOptions struct {
	CommonOptions
	Name           string `arg:"0"`              // Name of the VG to check.
	UpdateMetadata bool   `arg:"updatemetadata"` // Correct VG metadata inconsistencies.
}

// ExportVGOptions provides options for exporting VGs (vgexport).
type ExportVGOptions struct {
	CommonOptions
	Name   string `arg:"0"`      // Name of the VG to export.
	Select string `arg:"select"` // Filters objects based on criteria.
	All    bool   `arg:"all"`    // Export all VGs.
}

// ImportVGOptions provides options for importing VGs (vgimport).
type ImportVGOptions struct {
	CommonOptions
	Name   string `arg:"0"`      // Name of the VG to import.
	Select string `arg:"select"` // Filters objects based on criteria.
	All    bool   `arg:"all"`    // Import all VGs.
	Force  bool   `arg:"force"`  // Overrides checks and protections.
}

// ImportVGFromClonedOptions provides options for importing VGs from cloned PVs (vgimportclone).
type ImportVGFromClonedOptions struct {
	CommonOptions
	Name          string   `arg:"name"`          // Name of the VG to import.
	PVNames       []string `arg:"0"`             // List of PVs to import from.
	Import        bool     `arg:"import"`        // Import exported VGs.
	ImportDevices bool     `arg:"importdevices"` // Add devices to the devices file.
}

// MergeVGOptions provides options for merging VGs (vgmerge).
type MergeVGOptions struct {
	CommonOptions
	Destination       string `arg:"0"`                 // Name of the VG to merge into.
	Source            string `arg:"1"`                 // Name of the VG to merge.
	AutoBackup        *YesNo `arg:"autobackup"`        // Auto backup metadata after changes.
	PoolMetadataSpare *YesNo `arg:"poolmetadataspare"` // Toggles the automatic creation and management of a spare pool metadata LV in the VG.
}

// ExtendVGOptions provides options for extending VGs (vgextend).
type ExtendVGOptions struct {
	CommonOptions
	Name                string   `arg:"0"`                   // Name of the VG to extend.
	PVNames             []string `arg:"1"`                   // List of PVs to add to the VG.
	AutoBackup          *YesNo   `arg:"autobackup"`          // Auto backup metadata after changes.
	Force               bool     `arg:"force"`               // Override checks and protections.
	Zero                *YesNo   `arg:"zero"`                // Wipe first 4 sectors of the device.
	LabelSector         *int     `arg:"labelsector"`         // Sector for the LVM2 identifier.
	MetadataSize        string   `arg:"metadatasize"`        // Space for each VG metadata area.
	PVMetadataCopies    *int     `arg:"pvmetadatacopies"`    // Number of metadata areas on a PV.
	MetadataIgnore      *YesNo   `arg:"metadataignore"`      // If set, metadata won't be stored on the PV.
	DataAlignment       string   `arg:"dataalignment"`       // Align PV data's start, may be shifted by DataAlignmentOffset.
	DataAlignmentOffset string   `arg:"dataalignmentoffset"` // Additional shift for PV data's start.
	RestoreMissing      bool     `arg:"restoremissing"`      // Add a PV back into a VG after the PV was missing and then returned.
}

// ReduceVGOptions provides options for reducing VGs (vgreduce).
type ReduceVGOptions struct {
	CommonOptions
	Name          string   `arg:"0"`             // Name of the VG to reduce.
	PVNames       []string `arg:"1"`             // List of PVs to remove from the VG.
	All           bool     `arg:"all"`           // Remove all unused PVs from the VG.
	RemoveMissing bool     `arg:"removemissing"` // Remove missing PVs from the VG.
	MirrorsOnly   bool     `arg:"mirrorsonly"`   // Only remove missing PVs from mirror LVs.
	AutoBackup    *YesNo   `arg:"autobackup"`    // Auto backup metadata after changes.
	Force         bool     `arg:"force"`         // Override checks and protections.
}

// RenameVGOptions provides options for renaming VGs (vgrename).
type RenameVGOptions struct {
	CommonOptions
	From       string `arg:"0"`          // Name of the VG to rename.
	To         string `arg:"1"`          // New name for the VG.
	AutoBackup *YesNo `arg:"autobackup"` // Auto backup metadata after changes.
	Force      bool   `arg:"force"`      // Override checks and protections.
}

// MovePVOptions provides options for moving PVs between VGs (vgsplit).
type MovePVOptions struct {
	CommonOptions
	Source             string   `arg:"0"`                  // Name of the VG to move PVs from.
	Destination        string   `arg:"1"`                  // Name of the VG to move PVs to.
	PVNames            []string `arg:"2"`                  // List of PVs to move.
	LVName             string   `arg:"name"`               // Move only PVs used by the LV.
	AutoBackup         *YesNo   `arg:"autobackup"`         // Auto backup metadata after changes.
	MaxLogicalVolumes  *int     `arg:"maxlogicalvolumes"`  // Max number of LVs allowed in a VG.
	MaxPhysicalVolumes *int     `arg:"maxphysicalvolumes"` // Max number of PVs that can belong to the VG.
	Alloc              string   `arg:"alloc"`              // Allocation policy for Physical Extents.
	PoolMetadataSpare  *YesNo   `arg:"poolmetadataspare"`  // Toggles the automatic creation and management of a spare pool metadata LV in the VG.
	VGMetadataCopies   string   `arg:"vgmetadatacopies"`   // Number of copies of VG metadata.
}

// MakeVGDeviceNodesOptions provides options for creating LV device nodes for a VG (vgmknodes).
type MakeVGDeviceNodesOptions struct {
	CommonOptions
	Name                 string `arg:"0"`                    // Name of the VG.
	IgnoreLockingFailure bool   `arg:"ignorelockingfailure"` // Whether to proceed in read-only mode after lock failures.
	Refresh              bool   `arg:"refresh"`              // Refreshes the VG metadata.
}

// LogicalVolume represents an LVM2 Logical Volume (LV).
type LogicalVolume struct {
	UUID                               string      `json:"lv_uuid"`                     // Unique identifier.
	Name                               string      `json:"lv_name"`                     // Name.  LVs created for internal use are enclosed in brackets.
	FullName                           string      `json:"lv_full_name"`                // Full name of LV including its VG, namely VG/LV.
	Path                               string      `json:"lv_path"`                     // Full pathname for LV. Blank for internal LVs.
	DMPath                             string      `json:"lv_dm_path"`                  // Internal device//mapper pathname for LV (in /dev/mapper directory).
	Parent                             string      `json:"lv_parent"`                   // For LVs that are components of another LV, the parent LV.
	VGName                             string      `json:"vg_name"`                     // Name of the VG the LV belongs to.
	Layout                             string      `json:"lv_layout"`                   // LV layout.
	Role                               string      `json:"lv_role"`                     // LV role.
	InitialImageSync                   BoolString  `json:"lv_initial_image_sync"`       // Set if mirror/RAID images underwent initial resynchronization.
	ImageSynced                        BoolString  `json:"lv_image_synced"`             // Set if mirror/RAID image is synchronized.
	Merging                            BoolString  `json:"lv_merging"`                  // Set if snapshot LV is being merged to origin.
	Converting                         BoolString  `json:"lv_converting"`               // Set if LV is being converted.
	AllocationPolicy                   string      `json:"lv_allocation_policy"`        // LV allocation policy.
	AllocationLocked                   BoolString  `json:"lv_allocation_locked"`        // Set if LV is locked against allocation changes.
	FixedMinor                         BoolString  `json:"lv_fixed_minor"`              // Set if LV has fixed minor number assigned.
	SkipActivation                     BoolString  `json:"lv_skip_activation"`          // Set if LV is skipped on activation.
	AutoActivation                     BoolString  `json:"lv_autoactivation"`           // Set if LV autoactivation is enabled.
	WhenFull                           string      `json:"lv_when_full"`                // For thin pools, behavior when full.
	Active                             string      `json:"lv_active"`                   // Active state of the LV.
	ActiveLocally                      BoolString  `json:"lv_active_locally"`           // Set if the LV is active locally.
	ActiveRemotely                     BoolString  `json:"lv_active_remotely"`          // Set if the LV is active remotely.
	ActiveExclusively                  BoolString  `json:"lv_active_exclusively"`       // Set if the LV is active exclusively.
	Major                              IntString   `json:"lv_major"`                    // Persistent major number or -1 if not persistent.
	Minor                              IntString   `json:"lv_minor"`                    // Persistent minor number or -1 if not persistent.
	ReadAhead                          string      `json:"lv_read_ahead"`               // Read ahead setting in current units.
	Size                               Int64String `json:"lv_size"`                     // Size of LV in current units.
	MetadataSize                       string      `json:"lv_metadata_size"`            // For thin and cache pools, the size of the LV that holds the metadata.
	SegmentCount                       IntString   `json:"seg_count"`                   // Number of segments in LV.
	Origin                             string      `json:"origin"`                      // For snapshots and thins, the origin device of this LV.
	OriginUUID                         string      `json:"origin_uuid"`                 // For snapshots and thins, the UUID of origin device of this LV.
	OriginSize                         string      `json:"origin_size"`                 // For snapshots, the size of the origin device of this LV.
	Ancestors                          string      `json:"lv_ancestors"`                // LV ancestors ignoring any stored history of the ancestry chain.
	FullAncestors                      string      `json:"lv_full_ancestors"`           // LV ancestors including stored history of the ancestry chain.
	Descendants                        string      `json:"lv_descendants"`              // LV descendants ignoring any stored history of the ancestry chain.
	FullDescendants                    string      `json:"lv_full_descendants"`         // LV descendants including stored history of the ancestry chain.
	RAIDMismatchCount                  IntString   `json:"raid_mismatch_count"`         // For RAID, number of mismatches found or repaired.
	RAIDSyncAction                     string      `json:"raid_sync_action"`            // For RAID, the current synchronization action being performed.
	RAIDWriteBehind                    string      `json:"raid_write_behind"`           // For RAID1, the number of outstanding writes allowed to writemostly devices.
	RAIDMinRecoveryRate                string      `json:"raid_min_recovery_rate"`      // For RAID1, the minimum recovery I/O load in kiB/sec/disk.
	RAIDMaxRecoveryRate                string      `json:"raid_max_recovery_rate"`      // For RAID1, the maximum recovery I/O load in kiB/sec/disk.
	RAIDIntegrityMode                  string      `json:"raidintegritymode"`           // The integrity mode
	RAIDIntegrityBlockSize             string      `json:"raidintegrityblocksize"`      // The integrity block size
	RAIDIntegrityMismatches            IntString   `json:"integritymismatches"`         // The number of integrity mismatches.
	MovePV                             string      `json:"move_pv"`                     // For pvmove, Source PV of temporary LV created by pvmove.
	MovePVUUID                         string      `json:"move_pv_uuid"`                // For pvmove, the UUID of Source PV of temporary LV created by pvmove.
	ConvertLV                          string      `json:"convert_lv"`                  // For lvconvert, Name of temporary LV created by lvconvert.
	ConvertLVUUID                      string      `json:"convert_lv_uuid"`             // For lvconvert, UUID of temporary LV created by lvconvert.
	MirrorLog                          string      `json:"mirror_log"`                  // For mirrors, the LV holding the synchronisation log.
	MirrorLogUUID                      string      `json:"mirror_log_uuid"`             // For mirrors, the UUID of the LV holding the synchronisation log.
	DataLV                             string      `json:"data_lv"`                     // For cache/thin/vdo pools, the LV holding the associated data.
	DataLVUUID                         string      `json:"data_lv_uuid"`                // For cache/thin/vdo pools, the UUID of the LV holding the associated data.
	MetadataLV                         string      `json:"metadata_lv"`                 // For cache/thin pools, the LV holding the associated metadata.
	MetadataLVUUID                     string      `json:"metadata_lv_uuid"`            // For cache/thin pools, the UUID of the LV holding the associated metadata.
	PoolLV                             string      `json:"pool_lv"`                     // For cache/thin/vdo volumes, the cache/thin/vdo pool LV for this volume.
	PoolLVUUID                         string      `json:"pool_lv_uuid"`                // For cache/thin/vdo volumes, the UUID of the cache/thin/vdo pool LV for this volume.
	Tags                               string      `json:"lv_tags"`                     // Tags, if any.
	Profile                            string      `json:"lv_profile"`                  // Configuration profile attached to this LV.
	LockArgs                           string      `json:"lv_lockargs"`                 // Lock args of the LV used by lvmlockd.
	CreationTime                       string      `json:"lv_time"`                     // Creation time of the LV, if known
	RemovalTime                        string      `json:"lv_time_removed"`             // Removal time of the LV, if known
	CreationHost                       string      `json:"lv_host"`                     // Creation host of the LV, if known.
	RequiredModules                    string      `json:"lv_modules"`                  // Kernel device-mapper modules required for this LV.
	Historical                         BoolString  `json:"lv_historical"`               // Set if the LV is historical.
	WriteCacheBlockSize                string      `json:"writecache_block_size"`       // The writecache block size
	KernelMajor                        string      `json:"lv_kernel_major"`             // Currently assigned major number or -1 if LV is not active.
	KernelMinor                        string      `json:"lv_kernel_minor"`             // Currently assigned minor number or -1 if LV is not active.
	KernelReadAhead                    string      `json:"lv_kernel_read_ahead"`        // Currently-in-use read ahead setting in current units.
	Attributes                         string      `json:"lv_attr"`                     // LV attributes.
	Permissions                        BoolString  `json:"lv_permissions"`              // LV permissions.
	Suspended                          BoolString  `json:"lv_suspended"`                // Set if LV is suspended.
	LiveTable                          BoolString  `json:"lv_live_table"`               // Set if LV has live table present.
	InactiveTable                      BoolString  `json:"lv_inactive_table"`           // Set if LV has inactive table present.
	DeviceOpen                         BoolString  `json:"lv_device_open"`              // Set if LV device is open.
	DataPercent                        string      `json:"data_percent"`                // For snapshot, cache and thin pools and volumes, the percentage full if LV is active.
	SnapshotPercent                    string      `json:"snap_percent"`                // For snapshots, the percentage full if LV is active.
	MetadataPercent                    string      `json:"metadata_percent"`            // For cache and thin pools, the percentage of metadata full if LV is active.
	CopyPercent                        string      `json:"copy_percent"`                // For Cache, RAID, mirrors and pvmove, current percentage in-sync.
	SyncPercent                        string      `json:"sync_percent"`                // For Cache, RAID, mirrors and pvmove, current percentage in-sync.
	CacheTotalBlocks                   IntString   `json:"cache_total_blocks"`          // Total cache blocks.
	CacheUsedBlocks                    IntString   `json:"cache_used_blocks"`           // Used cache blocks.
	CacheDirtyBlocks                   IntString   `json:"cache_dirty_blocks"`          // Dirty cache blocks.
	CacheReadHits                      IntString   `json:"cache_read_hits"`             // Cache read hits.
	CacheReadMisses                    IntString   `json:"cache_read_misses"`           // Cache read misses.
	CacheWriteHits                     IntString   `json:"cache_write_hits"`            // Cache write hits.
	CacheWriteMisses                   IntString   `json:"cache_write_misses"`          // Cache write misses.
	KernelCacheSettings                string      `json:"kernel_cache_settings"`       // Cache settings/parameters as set in kernel, including default values (cached segments only).
	KernelCachePolicy                  string      `json:"kernel_cache_policy"`         // Cache policy used in kernel.
	KernelMetadataFormat               string      `json:"kernel_metadata_format"`      // Cache metadata format used in kernel.
	HealthStatus                       string      `json:"lv_health_status"`            // LV health status.
	KernelDiscards                     string      `json:"kernel_discards"`             // For thin pools, how discards are handled in kernel.
	CheckNeeded                        string      `json:"lv_check_needed"`             // For thin pools and cache volumes, whether metadata check is needed.
	MergeFailed                        BoolString  `json:"lv_merge_failed"`             // Set if snapshot merge failed.
	SnapshotInvalid                    BoolString  `json:"lv_snapshot_invalid"`         // Set if snapshot LV is invalid.
	VDOOperatingMode                   string      `json:"vdo_operating_mode"`          // For vdo pools, its current operating mode.
	VDOCompressionState                string      `json:"vdo_compression_state"`       // For vdo pools, whether compression is running.
	VDOIndexState                      string      `json:"vdo_index_state"`             // For vdo pools, state of index for deduplication.
	VDOUsedSize                        string      `json:"vdo_used_size"`               // For vdo pools, currently used space.
	VDOSavingPercent                   string      `json:"vdo_saving_percent"`          // For vdo pools, percentage of saved space.
	WriteCacheTotalBlocks              IntString   `json:"writecache_total_blocks"`     // Total writecache blocks.
	WriteCacheFreeBlocks               IntString   `json:"writecache_free_blocks"`      // Total writecache free blocks.
	WriteCacheWritebackBlocks          IntString   `json:"writecache_writeback_blocks"` // Total writecache writeback blocks.
	WriteCacheErrors                   IntString   `json:"writecache_error"`            // Total writecache errors.
	Type                               string      `json:"segtype"`                     // Type of LV segment.
	Stripes                            IntString   `json:"stripes"`                     // Number of stripes or mirror/raid1 legs.
	DataStripes                        IntString   `json:"data_stripes"`                // Number of data stripes or mirror/raid1 legs.
	ReshapeLength                      string      `json:"reshape_len"`                 // Size of out-of-place reshape space in current units.
	ReshapeLengthExtents               string      `json:"reshape_len_le"`              // Size of out-of-place reshape space in logical extents.
	DataCopies                         IntString   `json:"data_copies"`                 // Number of data copies.
	DataOffset                         string      `json:"data_offset"`                 // Data offset on each image device.
	NewDataOffset                      string      `json:"new_data_offset"`             // New data offset after any reshape on each image device.
	ParityChunks                       IntString   `json:"parity_chunks"`               // Number of (rotating) parity chunks.
	StripeSize                         string      `json:"stripe_size"`                 // For stripes, amount of data placed on one device before switching to the next.
	RegionSize                         string      `json:"region_size"`                 // For mirrors/raids, the unit of data per leg when synchronizing devices.
	ChunkSize                          string      `json:"chunk_size"`                  // For snapshots, the unit of data used when tracking changes.
	ThinCount                          IntString   `json:"thin_count"`                  // For thin pools, the number of thin volumes in this pool.
	Discards                           string      `json:"discards"`                    // For thin pools, how discards are handled.
	CacheMetadataFormat                string      `json:"cache_metadata_format"`       // For cache, metadata format in use.
	CacheMode                          string      `json:"cache_mode"`                  // For cache, how writes are cached.
	Zero                               BoolString  `json:"zero"`                        // For thin pools and volumes, if zeroing is enabled.
	TransactionID                      string      `json:"transaction_id"`              // For thin pools, the transaction id and creation transaction id for thins.
	ThinID                             string      `json:"thin_id"`                     // For thin volume, the thin device id.
	SegmentStart                       string      `json:"seg_start"`                   // Offset within the LV to the start of the segment in current units.
	SegmentStartExtents                string      `json:"seg_start_pe"`                // Offset within the LV to the start of the segment in physical extents.
	SegmentSize                        string      `json:"seg_size"`                    // Size of segment in current units.
	SegmentSizeExtents                 string      `json:"seg_size_pe"`                 // Size of segment in physical extents.
	SegmentTags                        string      `json:"seg_tags"`                    // Tags, if any.
	SegmentLogicalExtentRanges         string      `json:"seg_le_ranges"`               // Ranges of Logical Extents of underlying devices in command line format.
	SegmentMetadataLogicalExtentRanges string      `json:"seg_metadata_le_ranges"`      // Ranges of Logical Extents of underlying metadata devices in command line format.
	Devices                            string      `json:"devices"`                     // Underlying devices used with starting extent numbers.
	MetadataDevices                    string      `json:"metadata_devices"`            // Underlying metadata devices used with starting extent numbers.
	Monitor                            string      `json:"seg_monitor"`                 // Dmeventd monitoring status of the segment.
	CachePolicy                        string      `json:"cache_policy"`                // The cache policy (cached segments only).
	CacheSettings                      string      `json:"cache_settings"`              // Cache settings/parameters (cached segments only).
	VDOCompression                     BoolString  `json:"vdo_compression"`             // Set for compressed LV (vdopool).
	VDODeduplication                   BoolString  `json:"vdo_deduplication"`           // Set for deduplicated LV (vdopool).
}

// ListLVOptions provides options for listing LVs (lvs).
type ListLVOptions struct {
	CommonOptions
	Names                []string `arg:"0"`                    // Specific LVs to display.
	History              bool     `arg:"history"`              // Include historical LVs if `record_lvs_history` is enabled.
	All                  bool     `arg:"all"`                  // Display information about hidden internal LVs
	Select               string   `arg:"select"`               // Filters objects based on criteria.
	Foreign              bool     `arg:"foreign"`              // Lists foreign VGs.
	IgnoreLockingFailure bool     `arg:"ignorelockingfailure"` // Whether to proceed in read-only mode after lock failures.
	ReadOnly             bool     `arg:"readonly"`             // Read metadata without locks.
	Shared               bool     `arg:"shared"`               // Displays shared VGs without active lvmlockd.
}

// CreateLVOptions provides options for creating LVs (lvcreate).
type CreateLVOptions struct {
	CommonOptions
	Name                   string   `arg:"name"`                   // Name of the LV to create.
	VGName                 string   `arg:"0"`                      // Name of the VG to create the LV in.
	Activate               *YesNo   `arg:"activate"`               // Activate the LV.
	AutoBackup             *YesNo   `arg:"autobackup"`             // Auto backup metadata after changes.
	Contiguous             *YesNo   `arg:"contiguous"`             // Allocate physical extents next to each other.
	Persistent             *YesNo   `arg:"persistent"`             // Make the specified block device minor number persistent.
	Major                  *int     `arg:"major"`                  // Major number of the LV block device.
	Minor                  *int     `arg:"minor"`                  // Minor number of the LV block device.
	SetActivationSkip      *YesNo   `arg:"setactivationskip"`      // Set the "activation skip" flag.
	IgnoreActivationSkip   bool     `arg:"ignoreactivationskip"`   // Ignore the "activation skip" flag.
	Permission             string   `arg:"permission"`             // Access permission, either read only `r` or read and write `rw`.
	ReadAhead              string   `arg:"readahead"`              // Read-ahead sector count.
	WipeSignatures         *YesNo   `arg:"wipesignatures"`         // Wipe existing filesystem signatures.
	Zero                   *YesNo   `arg:"zero"`                   // Zero the first 4KiB of data in the new LV.
	Tags                   []string `arg:"addtag"`                 // Tags to add to the LV.
	Alloc                  string   `arg:"alloc"`                  // Allocation policy for Physical Extents.
	SetAutoActivation      *YesNo   `arg:"setautoactivation"`      // Enable autoactivation for the LV.
	IgnoreMonitoring       bool     `arg:"ignoremonitoring"`       // Ignore dmeventd monitoring.
	NoUdevSync             bool     `arg:"noudevsync"`             // Ignore udev notifications.
	Monitor                *YesNo   `arg:"monitor"`                // Toggle monitoring by dmeventd.
	NoSync                 bool     `arg:"nosync"`                 // Skips initial sync for mirror, raid*; useful for empty volumes.
	Type                   string   `arg:"type"`                   // Type of LV to create.
	Size                   string   `arg:"size"`                   // Size of the LV.
	Extents                string   `arg:"extents"`                // Size of the LV in logical extents.
	Stripes                *int     `arg:"stripes"`                // Number of stripes in a striped LV.
	StripeSize             string   `arg:"stripesize"`             // Amount of data that is written to one device before moving to the next.
	MirrorLog              string   `arg:"mirrorlog"`              // The type of mirror log for mirrored LVs.
	Mirrors                *int     `arg:"mirrors"`                // Number of mirror images in addition to the original LV image.
	RegionSize             string   `arg:"regionsize"`             // Size of each raid or mirror synchronization region.
	MinRecoveryRate        string   `arg:"minrecoveryrate"`        // Minimum recovery rate for a RAID LV.
	MaxRecoveryRate        string   `arg:"maxrecoveryrate"`        // Maximum recovery rate for a RAID LV.
	RAIDIntegrity          *YesNo   `arg:"raidintegrity"`          // Enable or disable data integrity checksums.
	RAIDIntegrityMode      string   `arg:"raidintegritymode"`      // Chooses between using a journal (default) or bitmap for integrity checksums.
	RAIDIntegrityBlockSize *int     `arg:"raidintegrityblocksize"` // Defines block size for dm-integrity on raid images.
	Snapshot               bool     `arg:"snapshot"`               // Create a snapshot.
	ChunkSize              string   `arg:"chunksize"`              // Size of chunks in a snapshot, cache pool or thin pool.
	VirtualSize            string   `arg:"virtualsize"`            // Virtual size of a new thin LV.
	Thin                   bool     `arg:"thin"`                   // Create a thin LV.
	ThinPool               string   `arg:"thinpool"`               // Name of the thin pool LV.
	Discards               string   `arg:"discards"`               // How the device-mapper thin pool layer in the kernel should handle discards.
	ErrorWhenFull          *YesNo   `arg:"errorwhenfull"`          // Whether to fail when the thin pool is full.
	PoolMetadataSize       string   `arg:"poolmetadatasize"`       // Specifies the size of the new pool metadata LV.
	PoolMetadataSpare      *YesNo   `arg:"poolmetadataspare"`      // Toggles the automatic creation and management of a spare pool metadata LV in the VG.
	Cache                  bool     `arg:"cache"`                  // Specifies the command is handling a cache LV or cache pool.
	CacheDevice            string   `arg:"cachedevice"`            // The PV to use for the cache.
	CacheVol               string   `arg:"cachevol"`               // The name of the cache LV.
	CacheMode              string   `arg:"cachemode"`              // When writes to a cache LV should be considered complete.
	CachePolicy            string   `arg:"cachepolicy"`            // The cache policy to use.
	CachePool              string   `arg:"cachepool"`              // The name of a cache pool.
	CacheSettings          []string `arg:"cachesettings"`          // Cache settings in `key=value` format.
	CacheSize              string   `arg:"cachesize"`              // Size of the cache LV.
	VDO                    bool     `arg:"vdo"`                    // Specifies the command is handling a VDO LV.
	VDOPool                string   `arg:"vdopool"`                // The name of the VDO pool LV.
	VDOSettings            []string `arg:"vdosettings"`            // VDO settings in `key=value` format.
	Compression            *YesNo   `arg:"compression"`            // Whether to enable compression.
	Deduplication          *YesNo   `arg:"deduplication"`          // Whether to enable deduplication.
}

// UpdateLVOptions provides options for modifying LVs (lvchange).
type UpdateLVOptions struct {
	CommonOptions
	Name                 string   `arg:"0"`                    // Name of the LV to modify.
	Force                bool     `arg:"force"`                // Override checks and protections.
	Select               string   `arg:"select"`               // Filters objects based on criteria.
	Refresh              bool     `arg:"refresh"`              // Refreshes the LV metadata.
	Activate             *YesNo   `arg:"activate"`             // Activate the LV.
	AutoBackup           *YesNo   `arg:"autobackup"`           // Auto backup metadata after changes.
	Contiguous           *YesNo   `arg:"contiguous"`           // Allocate physical extents next to each other.
	Persistent           *YesNo   `arg:"persistent"`           // Make the specified block device minor number persistent.
	Major                *int     `arg:"major"`                // Major number of the LV block device.
	Minor                *int     `arg:"minor"`                // Minor number of the LV block device.
	SetActivationSkip    *YesNo   `arg:"setactivationskip"`    // Set the "activation skip" flag.
	IgnoreActivationSkip bool     `arg:"ignoreactivationskip"` // Ignore the "activation skip" flag.
	Permission           string   `arg:"permission"`           // Access permission, either read only `r` or read and write `rw`.
	ReadAhead            string   `arg:"readahead"`            // Read-ahead sector count.
	Zero                 *YesNo   `arg:"zero"`                 // Set zeroing mode for thin pool.
	AddTags              []string `arg:"addtag"`               // Add tag/s to the LV.
	DelTags              []string `arg:"deltag"`               // Remove tag/s from the LV.
	Alloc                string   `arg:"alloc"`                // Allocation policy for Physical Extents.
	DetachProfile        bool     `arg:"detachprofile"`        // Detach a metadata profile.
	Partial              bool     `arg:"partial"`              // Attempt activation with missing Physical Extents.
	ActivationMode       string   `arg:"activationmode"`       // Conditions under which a LV can be activated with missing PVs.
	SetAutoActivation    *YesNo   `arg:"setautoactivation"`    // Enable autoactivation for the LV.
	Poll                 *YesNo   `arg:"poll"`                 // Resume background operations that were halted due to disruptions.
	IgnoreMonitoring     bool     `arg:"ignoremonitoring"`     // Ignore dmeventd monitoring.
	NoUdevSync           bool     `arg:"noudevsync"`           // Ignore udev notifications.
	Monitor              *YesNo   `arg:"monitor"`              // Toggle monitoring by dmeventd.
	Resync               bool     `arg:"resync"`               // Initiate mirror synchronization.
	MinRecoveryRate      string   `arg:"minrecoveryrate"`      // Minimum recovery rate for a RAID LV.
	MaxRecoveryRate      string   `arg:"maxrecoveryrate"`      // Maximum recovery rate for a RAID LV.
	WriteBehind          *int     `arg:"writebehind"`          // Maximum number of outstanding writes that are allowed to devices in a RAID1 LV that is marked write-mostly.
	WriteMostly          []string `arg:"writemostly"`          // Mark a device in a RAID1 LV as write-mostly.
	Rebuild              string   `arg:"rebuild"`              // Selects a PV to rebuild in a raid LV.
	SyncAction           string   `arg:"syncaction"`           // Initiate different types of RAID synchronization.
	Discards             string   `arg:"discards"`             // How the device-mapper thin pool layer in the kernel should handle discards.
	ErrorWhenFull        *YesNo   `arg:"errorwhenfull"`        // Whether to fail when the thin pool is full.
	CacheMode            string   `arg:"cachemode"`            // When writes to a cache LV should be considered complete.
	CachePolicy          string   `arg:"cachepolicy"`          // The cache policy to use.
	CacheSettings        string   `arg:"cachesettings"`        // Cache settings in `key=value` format.
	Compression          *YesNo   `arg:"compression"`          // Whether to enable compression.
	Deduplication        *YesNo   `arg:"deduplication"`        // Whether to enable deduplication.
	VDOSettings          string   `arg:"vdosettings"`          // VDO settings in `key=value` format.
	SysInit              bool     `arg:"sysinit"`              // Indicates that the command is being invoked from early system init scripts.
	IgnoreLockingFailure bool     `arg:"ignorelockingfailure"` // Whether to proceed in read-only mode after lock failures.
	ReadOnly             bool     `arg:"readonly"`             // Read metadata without locks.
}

// RemoveLVOptions provides options for removing LVs (lvremove).
type RemoveLVOptions struct {
	CommonOptions
	Name       string `arg:"0"`          // Name of the LV to remove.
	AutoBackup *YesNo `arg:"autobackup"` // Auto backup metadata after changes.
	Force      bool   `arg:"force"`      // Override checks and protections.
	Select     string `arg:"select"`     // Filters objects based on criteria.
	NoHistory  bool   `arg:"nohistory"`  // Do not record history of LV being removed.
	NoUdevSync bool   `arg:"noudevsync"` // Ignore udev notifications.
}

// ConvertLVLayoutOptions provides options for changing LV layouts (lvconvert).
type ConvertLVLayoutOptions struct {
	CommonOptions
	Name                   string   `arg:"0"`                      // Name of the LV to convert.
	NewName                string   `arg:"name"`                   // The name of the new LV. When unspecified one is generated.
	PVNames                []string `arg:"1"`                      // Specific PVs to convert.
	Background             bool     `arg:"background"`             // Run conversion in background.
	Interval               *int     `arg:"interval"`               // Report progress at regular intervals.
	StartPoll              bool     `arg:"startpoll"`              // Start polling an LV to continue processing a conversion.
	Force                  bool     `arg:"force"`                  // Override checks and protections.
	UsePolicies            bool     `arg:"usepolicies"`            // Use the policy configured in lvm.conf or a profile.
	Stripes                *int     `arg:"stripes"`                // Number of stripes in a striped LV.
	StripeSize             string   `arg:"stripesize"`             // Amount of data that is written to one device before moving to the next.
	MirrorLog              string   `arg:"mirrorlog"`              // The type of mirror log for mirrored LVs.
	Mirrors                *int     `arg:"mirrors"`                // Number of mirror images in addition to the original LV image.
	RegionSize             string   `arg:"regionsize"`             // Size of each raid or mirror synchronization region.
	Alloc                  string   `arg:"alloc"`                  // Allocation policy for Physical Extents.
	NoUdevSync             bool     `arg:"noudevsync"`             // Ignore udev notifications.
	Type                   string   `arg:"type"`                   // Type of LV to convert to.
	ReadAhead              string   `arg:"readahead"`              // Read-ahead sector count.
	Zero                   *YesNo   `arg:"zero"`                   // For snapshots, zero the first 4KiB (unless read-only); for thin pools, zero newly provisioned blocks.
	RAIDIntegrity          *YesNo   `arg:"raidintegrity"`          // Enable or disable data integrity checksums.
	RAIDIntegrityMode      string   `arg:"raidintegritymode"`      // Chooses between using a journal (default) or bitmap for integrity checksums.
	RAIDIntegrityBlockSize *int     `arg:"raidintegrityblocksize"` // Defines block size for dm-integrity on raid images.
	Snapshot               bool     `arg:"snapshot"`               // Combine a former COW snapshot LV with a former origin LV.
	ChunkSize              string   `arg:"chunksize"`              // Size of chunks in a snapshot, cache pool or thin pool.
	VirtualSize            string   `arg:"virtualsize"`            // Virtual size of a new thin LV.
	Thin                   bool     `arg:"thin"`                   // Create a thin LV.
	ThinPool               string   `arg:"thinpool"`               // Name of the thin pool LV.
	Discards               string   `arg:"discards"`               // How the device-mapper thin pool layer in the kernel should handle discards.
	ErrorWhenFull          *YesNo   `arg:"errorwhenfull"`          // Whether to fail when the thin pool is full.
	OriginName             string   `arg:"originname"`             // Specifies the name to use for the external origin LV when converting an LV to a thin LV.
	PoolMetadata           string   `arg:"poolmetadata"`           // The name of a an LV to use for storing pool metadata.
	PoolMetadataSize       string   `arg:"poolmetadatasize"`       // Specifies the size of the new pool metadata LV.
	PoolMetadataSpare      *YesNo   `arg:"poolmetadataspare"`      // Toggles the automatic creation and management of a spare pool metadata LV in the VG.
	SwapMetadata           bool     `arg:"swapmetadata"`           // Extracts the metadata LV from a pool and replaces it with another specified LV.
	Cache                  bool     `arg:"cache"`                  // Specifies the command is handling a cache LV or cache pool.
	CacheDevice            string   `arg:"cachedevice"`            // The PV to use for the cache.
	CacheVol               string   `arg:"cachevol"`               // The name of the cache LV.
	CacheMode              string   `arg:"cachemode"`              // When writes to a cache LV should be considered complete.
	CachePolicy            string   `arg:"cachepolicy"`            // The cache policy to use.
	CachePool              string   `arg:"cachepool"`              // The name of a cache pool.
	CacheSettings          []string `arg:"cachesettings"`          // Cache settings in `key=value` format.
	CacheSize              string   `arg:"cachesize"`              // Size of the cache LV.
	VDOPool                string   `arg:"vdopool"`                // The name of the VDO pool LV.
	VDOSettings            []string `arg:"vdosettings"`            // VDO settings in `key=value` format.
	Compression            *YesNo   `arg:"compression"`            // Whether to enable compression.
	Deduplication          *YesNo   `arg:"deduplication"`          // Whether to enable deduplication.
	Merge                  bool     `arg:"merge"`                  // An alias for MergeMirrors, MergeSnapshot, or MergeThin depending on LV type.
	MergeMirrors           bool     `arg:"mergemirrors"`           // Merge LV images that were split from a raid1 LV.
	MergeSnapshot          bool     `arg:"-mergesnapshot"`         // Merge COW snapshot LV into its origin.
	MergeThin              bool     `arg:"mergethin"`              // Merge thin LV into its origin LV.
	SplitCache             bool     `arg:"splitcache"`             // Separates a cache pool from a cache LV, and keeps the unused cache pool LV.
	SplitMirrors           *int     `arg:"splitmirrors"`           // Splits the specified number of images from a raid1 or mirror LV and uses them to create a new LV.
	SplitSnapshot          bool     `arg:"splitsnapshot"`          // Separates a COW snapshot from its origin LV.
	Uncache                bool     `arg:"uncache"`                // Separates a cache pool from a cache LV, and deletes the unused cache pool LV.
	TrackChanges           bool     `arg:"trackchanges"`           // Tracks changes to a raid1 LV while the split images remain detached.
	Repair                 bool     `arg:"repair"`                 // Replace failed PVs in a raid or mirror LV, or run a repair utility on a thin pool.
	Replace                string   `arg:"replace"`                // Replace a specific PV in a raid LV with another PV.
}

// ExtendLVOptions provides options for adding space to an LV (lvextend).
type ExtendLVOptions struct {
	CommonOptions
	Name             string   `arg:"0"`                // Name of the LV to extend.
	PVNames          []string `arg:"1"`                // Specific PVs to extend onto.
	AutoBackup       *YesNo   `arg:"autobackup"`       // Auto backup metadata after changes.
	Force            bool     `arg:"force"`            // Override checks and protections.
	Alloc            string   `arg:"alloc"`            // Allocation policy for Physical Extents.
	UsePolicies      bool     `arg:"usepolicies"`      // Use the policy configured in lvm.conf or a profile.
	Type             string   `arg:"type"`             // Type of LV to extend to.
	Size             string   `arg:"size"`             // The new size of the LV.
	Extents          string   `arg:"extents"`          // The new size of the LV in logical extents.
	Stripes          *int     `arg:"stripes"`          // Number of stripes in a striped LV.
	StripeSize       string   `arg:"stripesize"`       // Amount of data that is written to one device before moving to the next.
	PoolMetadataSize string   `arg:"poolmetadatasize"` // The new size of the pool metadata LV.
	NoSync           bool     `arg:"nosync"`           // Skips initial sync for mirror, raid*; useful for empty volumes.
	NoUdevSync       bool     `arg:"noudevsync"`       // Ignore udev notifications.
	ResizeFS         bool     `arg:"resizefs"`         // Resize underlying filesystem together with the LV.
	NoFsck           bool     `arg:"nofsck"`           // Skip performing fsck before resizing the filesystem.
}

// ReduceLVOptions provides options for reducing the size of an LV (lvreduce).
type ReduceLVOptions struct {
	CommonOptions
	Name       string `arg:"0"`          // Name of the LV to reduce.
	AutoBackup *YesNo `arg:"autobackup"` // Auto backup metadata after changes.
	Force      bool   `arg:"force"`      // Override checks and protections.
	NoUdevSync bool   `arg:"noudevsync"` // Ignore udev notifications.
	Size       string `arg:"size"`       // The new size of the LV.
	Extents    string `arg:"extents"`    // The new size of the LV in logical extents.
	ResizeFS   bool   `arg:"resizefs"`   // Resize underlying filesystem together with the LV.
	NoFsck     bool   `arg:"nofsck"`     // Skip performing fsck before resizing the filesystem.
}

// RenameLVOptions provides options for renaming LVs (lvrename).
type RenameLVOptions struct {
	CommonOptions
	From       string `arg:"0"`          // Name of the LV to rename.
	To         string `arg:"1"`          // New name for the LV.
	AutoBackup *YesNo `arg:"autobackup"` // Auto backup metadata after changes.
	NoUdevSync bool   `arg:"noudevsync"` // Ignore udev notifications.
}

// CommonOptions holds configurations for LVM2 commands.
type CommonOptions struct {
	Config      string   `arg:"config"`      // Overrides lvm.conf settings.
	NoLocking   bool     `arg:"nolocking"`   // Disables locking.
	LockOpt     string   `arg:"lockopt"`     // Options for lvmlockd.
	Profile     string   `arg:"profile"`     // Command profile.
	DevicesFile string   `arg:"devicesfile"` // LVM device file (from /etc/lvm/devices/).
	Devices     []string `arg:"devices"`     // Overrides lvm.conf devices.
	NoHints     bool     `arg:"nohints"`     // Disables PV location hint.
	Journal     string   `arg:"journal"`     // Logs in systemd journal.
}
