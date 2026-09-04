// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"local-csi-driver/internal/pkg/block"
	"local-csi-driver/internal/pkg/lvm"
)

// TestQuarantineErrorClassification pins the error contract that the deletion
// path depends on, against real LVM rather than a mock.
//
// Quarantine tags and renames a logical volume instead of removing it, and
// DeleteVolume decides whether a failure means "already deleted" from the
// error those two calls return. The unit tests can only assert that the driver
// reacts correctly to lvm.ErrNotFound; this asserts that the client actually
// produces it, and that a missing volume group is reported distinguishably so
// that a temporarily invisible group is never mistaken for a deleted volume.
func TestQuarantineErrorClassification(t *testing.T) {
	if !isRoot() {
		t.Skip("Skipping TestQuarantineErrorClassification as it requires root permissions.")
	}
	if _, err := os.Stat("/sbin/lvm"); err != nil {
		t.Skip("Skipping TestQuarantineErrorClassification as /sbin/lvm is not available.")
	}

	c := lvm.NewClient(lvm.WithBlockDeviceUtilities(block.New()))
	ctx := context.Background()
	vgName := newTestVolumeGroup(t, c)

	missing := vgName + "/" + uniqueName("absent")

	t.Run("TagMissingVolumeIsNotFound", func(t *testing.T) {
		err := c.UpdateLogicalVolume(ctx, lvm.UpdateLVOptions{
			Name:    missing,
			AddTags: []string{"local-csi-wipe-pending"},
		})
		if !errors.Is(err, lvm.ErrNotFound) {
			t.Errorf("UpdateLogicalVolume() error = %v, want ErrNotFound", err)
		}
		if errors.Is(err, lvm.ErrVolumeGroupNotFound) {
			t.Errorf("a missing logical volume must not be reported as a missing volume group: %v", err)
		}
	})

	t.Run("RenameMissingVolumeIsNotFound", func(t *testing.T) {
		err := c.RenameLogicalVolume(ctx, lvm.RenameLVOptions{
			From: missing,
			To:   vgName + "/" + uniqueName("wipe"),
		})
		if !errors.Is(err, lvm.ErrNotFound) {
			t.Errorf("RenameLogicalVolume() error = %v, want ErrNotFound", err)
		}
		// lvrename reports a missing source as "Existing logical volume "lv"
		// not found in volume group "vg"", which names the group. It must
		// still be classified as a missing volume, or deletion would never be
		// idempotent.
		if errors.Is(err, lvm.ErrVolumeGroupNotFound) {
			t.Errorf("a missing logical volume must not be reported as a missing volume group: %v", err)
		}
	})

	// An invisible volume group is usually temporary. Deletion must be able to
	// tell it apart from a genuinely absent volume, or it would report success
	// and leave a populated volume behind with nothing referencing it.
	t.Run("MissingVolumeGroupIsDistinguishable", func(t *testing.T) {
		err := c.UpdateLogicalVolume(ctx, lvm.UpdateLVOptions{
			Name:    uniqueName("absentvg") + "/lv",
			AddTags: []string{"local-csi-wipe-pending"},
		})
		if !errors.Is(err, lvm.ErrVolumeGroupNotFound) {
			t.Errorf("UpdateLogicalVolume() error = %v, want ErrVolumeGroupNotFound", err)
		}
	})
}
