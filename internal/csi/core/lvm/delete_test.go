// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"go.uber.org/mock/gomock"

	"local-csi-driver/internal/csi/core/lvm"
	lvmMgr "local-csi-driver/internal/pkg/lvm"
)

// TestDeleteQuarantinesInsteadOfRemoving is the core regression test for
// deletion-time sanitization.
//
// Removing a logical volume returns its extents to the volume group's free
// pool with the tenant's data still on them, where the next volume created in
// that group can read them back. Delete must therefore hand the volume to the
// wipe reaper rather than removing it.
func TestDeleteQuarantinesInsteadOfRemoving(t *testing.T) {
	t.Parallel()

	l := newQuarantineTestLVM(t, func(m *lvmMgr.MockManager) {
		m.EXPECT().
			UpdateLogicalVolume(gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, opts lvmMgr.UpdateLVOptions) error {
				if len(opts.AddTags) != 1 || opts.AddTags[0] != lvm.WipePendingTag {
					t.Errorf("added tags %v, want [%s]", opts.AddTags, lvm.WipePendingTag)
				}
				return nil
			})
		m.EXPECT().RenameLogicalVolume(gomock.Any(), gomock.Any()).Return(nil)

		// The absence of a RemoveLogicalVolume expectation is the assertion:
		// gomock fails the test if the volume is removed here.
	})

	err := l.Delete(context.Background(), &csi.DeleteVolumeRequest{
		VolumeId: testVolumeGroup + "#" + testLogicalVolume,
	})
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
}

// TestDeleteIsIdempotent checks that deleting a volume that is already gone
// succeeds.
//
// The CSI specification requires DeleteVolume to return OK when the volume
// does not exist, and the sidecar retries deletions, so this is the common
// case on a retry rather than an edge case.
func TestDeleteIsIdempotent(t *testing.T) {
	t.Parallel()

	l := newQuarantineTestLVM(t, func(m *lvmMgr.MockManager) {
		m.EXPECT().
			UpdateLogicalVolume(gomock.Any(), gomock.Any()).
			Return(lvmMgr.ErrNotFound)
	})

	err := l.Delete(context.Background(), &csi.DeleteVolumeRequest{
		VolumeId: testVolumeGroup + "#" + testLogicalVolume,
	})
	if err != nil {
		t.Fatalf("Delete() error = %v, want nil for a volume that is already gone", err)
	}
}

// TestDeleteReturnsErrorWhenQuarantineFails checks that a volume which could
// not be quarantined is reported as a failed deletion.
//
// Reporting success would let the provisioner delete the PersistentVolume
// while the logical volume is still live and unsanitized, leaving it to be
// collected later by a path that may not zero it.
func TestDeleteReturnsErrorWhenQuarantineFails(t *testing.T) {
	t.Parallel()

	l := newQuarantineTestLVM(t, func(m *lvmMgr.MockManager) {
		m.EXPECT().
			UpdateLogicalVolume(gomock.Any(), gomock.Any()).
			Return(errors.New("metadata write failed"))
	})

	err := l.Delete(context.Background(), &csi.DeleteVolumeRequest{
		VolumeId: testVolumeGroup + "#" + testLogicalVolume,
	})
	if err == nil {
		t.Fatal("Delete() error = nil, want an error when the volume could not be quarantined")
	}
}

// TestDeleteIgnoresUnparseableVolumeID checks that an ID this driver could not
// have issued is treated as already deleted, since by definition no such
// volume exists.
func TestDeleteIgnoresUnparseableVolumeID(t *testing.T) {
	t.Parallel()

	// No LVM calls may be made at all.
	l := newQuarantineTestLVM(t, nil)

	err := l.Delete(context.Background(), &csi.DeleteVolumeRequest{
		VolumeId: "not-a-valid-volume-id",
	})
	if err != nil {
		t.Fatalf("Delete() error = %v, want nil", err)
	}
}

// TestDeleteFailsClosedWhenVolumeGroupIsInvisible checks that an invisible
// volume group is not mistaken for a deleted volume.
//
// DeleteVolume must be idempotent, but the evidence for "already gone" has to
// be about the volume itself. LVM reports a missing group with the same
// not-found wording, and that condition is usually temporary, for example
// before device scanning has finished after a reboot. Reporting success would
// let the PersistentVolume be removed while a populated logical volume
// survives on disk with nothing left referencing it.
func TestDeleteFailsClosedWhenVolumeGroupIsInvisible(t *testing.T) {
	t.Parallel()

	l := newQuarantineTestLVM(t, func(m *lvmMgr.MockManager) {
		m.EXPECT().
			UpdateLogicalVolume(gomock.Any(), gomock.Any()).
			Return(fmt.Errorf("%w: Volume group %q not found", lvmMgr.ErrVolumeGroupNotFound, testVolumeGroup))
	})

	err := l.Delete(context.Background(), &csi.DeleteVolumeRequest{
		VolumeId: testVolumeGroup + "#" + testLogicalVolume,
	})
	if err == nil {
		t.Fatal("expected an error when the volume group cannot be seen, got nil")
	}
}
