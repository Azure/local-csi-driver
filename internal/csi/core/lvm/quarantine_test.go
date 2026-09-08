// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"local-csi-driver/internal/csi/core/lvm"
	lvmMgr "local-csi-driver/internal/pkg/lvm"
	"local-csi-driver/internal/pkg/probe"
	"local-csi-driver/internal/pkg/telemetry"
)

const testLogicalVolume = "test-lv"

// newQuarantineTestLVM builds an LVM core backed by a mock manager.
func newQuarantineTestLVM(t *testing.T, expect func(*lvmMgr.MockManager)) *lvm.LVM {
	t.Helper()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockLVM := lvmMgr.NewMockManager(ctrl)
	if expect != nil {
		expect(mockLVM)
	}

	l, err := lvm.New(
		"test-pod", "test-node", "test-namespace", false,
		probe.NewFake([]string{"device1"}, nil), mockLVM,
		telemetry.NewNoopTracerProvider(),
	)
	if err != nil {
		t.Fatal(err)
	}

	return l
}

// TestQuarantineTagsBeforeRenaming pins the ordering that makes quarantine
// recoverable.
//
// The reaper finds volumes by tag. If a volume were renamed before it was
// tagged and the process died in between, the volume would be invisible to the
// reaper and would hold its extents indefinitely. Tagging first means a crash
// at any point leaves a volume that is still found and still wiped.
func TestQuarantineTagsBeforeRenaming(t *testing.T) {
	t.Parallel()

	var calls []string

	l := newQuarantineTestLVM(t, func(m *lvmMgr.MockManager) {
		m.EXPECT().
			UpdateLogicalVolume(gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, opts lvmMgr.UpdateLVOptions) error {
				calls = append(calls, "tag")
				if got, want := opts.Name, testVolumeGroup+"/"+testLogicalVolume; got != want {
					t.Errorf("tagged %q, want %q", got, want)
				}
				if len(opts.AddTags) != 1 || opts.AddTags[0] != lvm.WipePendingTag {
					t.Errorf("added tags %v, want [%s]", opts.AddTags, lvm.WipePendingTag)
				}
				return nil
			})

		m.EXPECT().
			RenameLogicalVolume(gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, opts lvmMgr.RenameLVOptions) error {
				calls = append(calls, "rename")
				if got, want := opts.From, testVolumeGroup+"/"+testLogicalVolume; got != want {
					t.Errorf("renamed from %q, want %q", got, want)
				}
				if !strings.HasPrefix(opts.To, testVolumeGroup+"/local-csi-wipe-") {
					t.Errorf("renamed to %q, want the local-csi-wipe- prefix in %s", opts.To, testVolumeGroup)
				}
				return nil
			})
	})

	name, err := l.Quarantine(context.Background(), testVolumeGroup, testLogicalVolume)
	if err != nil {
		t.Fatalf("Quarantine returned error: %v", err)
	}

	if !strings.HasPrefix(name, "local-csi-wipe-") {
		t.Errorf("quarantined name %q does not carry the local-csi-wipe- prefix", name)
	}
	if want := []string{"tag", "rename"}; !equalStrings(calls, want) {
		t.Errorf("call order was %v, want %v", calls, want)
	}
}

// TestQuarantineNamesAreUnique verifies that two quarantined volumes cannot
// collide.
//
// Names are generated rather than derived from the volume, because the same
// volume ID can be created and deleted repeatedly and an earlier quarantined
// volume may still be waiting to be wiped.
func TestQuarantineNamesAreUnique(t *testing.T) {
	t.Parallel()

	l := newQuarantineTestLVM(t, func(m *lvmMgr.MockManager) {
		m.EXPECT().UpdateLogicalVolume(gomock.Any(), gomock.Any()).Return(nil).Times(2)
		m.EXPECT().RenameLogicalVolume(gomock.Any(), gomock.Any()).Return(nil).Times(2)
	})

	first, err := l.Quarantine(context.Background(), testVolumeGroup, testLogicalVolume)
	if err != nil {
		t.Fatalf("first Quarantine returned error: %v", err)
	}
	second, err := l.Quarantine(context.Background(), testVolumeGroup, testLogicalVolume)
	if err != nil {
		t.Fatalf("second Quarantine returned error: %v", err)
	}

	if first == second {
		t.Errorf("expected distinct quarantine names, got %q twice", first)
	}
}

// TestQuarantineDoesNotRenameWhenTaggingFails verifies that a failed tag stops
// the operation.
//
// Renaming an untagged volume would hide it from both the driver and the
// reaper, so the volume must be left exactly as it was for the caller to
// retry.
func TestQuarantineDoesNotRenameWhenTaggingFails(t *testing.T) {
	t.Parallel()

	tagErr := errors.New("tag failed")

	l := newQuarantineTestLVM(t, func(m *lvmMgr.MockManager) {
		m.EXPECT().UpdateLogicalVolume(gomock.Any(), gomock.Any()).Return(tagErr)
		// No RenameLogicalVolume expectation: the controller fails the test if
		// it is called.
	})

	if _, err := l.Quarantine(context.Background(), testVolumeGroup, testLogicalVolume); err == nil {
		t.Fatal("expected an error when tagging fails, got nil")
	}
}

// TestQuarantineRollsBackTagOnRenameFailure pins the commit-point contract.
//
// Destruction is committed by the rename, not by the tag. If the rename fails
// the volume is still in service under its original name, so the tag must be
// rolled back: leaving it in place would mark a live volume as pending
// destruction and hide it from the orphan scanner.
func TestQuarantineRollsBackTagOnRenameFailure(t *testing.T) {
	t.Parallel()

	var deltagged bool

	l := newQuarantineTestLVM(t, func(m *lvmMgr.MockManager) {
		m.EXPECT().
			UpdateLogicalVolume(gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, opts lvmMgr.UpdateLVOptions) error {
				if len(opts.AddTags) == 1 && opts.AddTags[0] == lvm.WipePendingTag {
					return nil
				}
				if len(opts.DelTags) == 1 && opts.DelTags[0] == lvm.WipePendingTag {
					deltagged = true
					return nil
				}
				t.Errorf("unexpected tag update: %+v", opts)
				return nil
			}).
			Times(2)
		m.EXPECT().RenameLogicalVolume(gomock.Any(), gomock.Any()).Return(errors.New("rename failed"))
	})

	name, err := l.Quarantine(context.Background(), testVolumeGroup, testLogicalVolume)
	if err == nil {
		t.Fatal("expected an error when renaming fails, got nil")
	}
	if name != "" {
		t.Errorf("expected no quarantine name on failure, got %q", name)
	}
	if !deltagged {
		t.Error("expected the wipe tag to be rolled back after a failed rename")
	}
}

// TestReaperIgnoresUncommittedQuarantine is the counterpart safety property: a
// volume that carries the tag but was never renamed out of the live namespace
// must never be treated as a wipe target, because it may still be in service
// and referenced by a PersistentVolume.
func TestReaperIgnoresUncommittedQuarantine(t *testing.T) {
	t.Parallel()

	tagged := lvmMgr.LogicalVolume{
		Name:   testLogicalVolume,
		VGName: testVolumeGroup,
		Tags:   lvm.WipePendingTag,
	}

	if lvm.IsQuarantineCommitted(tagged) {
		t.Fatal("a tagged but unrenamed volume must not be considered committed")
	}
	if !lvm.IsQuarantined(tagged) {
		t.Fatal("a tagged volume must still be skipped by callers that delete volumes")
	}

	r := newTestReaper(t, func(m *lvmMgr.MockManager) {
		expectVolumeGroup(m)
		expectQuarantined(m, tagged)

		// The absence of Sanitize/Remove expectations is the assertion.
	})

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
}

// TestReaperDefersToOpenVolume covers raw block volumes.
//
// The exclusive open in the sanitizer conflicts with a mount, but a raw block
// volume is published as a device node and a pod holding it open takes no
// exclusive claim. LVM's own open count is what catches that, and a volume
// reporting itself open must never be zeroed.
func TestReaperDefersToOpenVolume(t *testing.T) {
	t.Parallel()

	open := quarantinedLV(testWipeName)
	open.DeviceOpen = lvmMgr.BoolString(true)

	r := newTestReaper(t, func(m *lvmMgr.MockManager) {
		expectVolumeGroup(m)
		expectQuarantined(m, open)

		m.EXPECT().UpdateLogicalVolume(gomock.Any(), gomock.Any()).Return(nil)

		// No Sanitize or Remove: gomock fails the test if either is called.
	})

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
}

// TestQuarantineValidation covers input validation.
func TestQuarantineValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		vgName string
		lvName string
	}{
		{name: "empty volume group", vgName: "", lvName: testLogicalVolume},
		{name: "empty logical volume", vgName: testVolumeGroup, lvName: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// No manager expectations: validation must happen before any LVM
			// call, so that invalid input cannot mutate anything.
			l := newQuarantineTestLVM(t, nil)

			if _, err := l.Quarantine(context.Background(), tt.vgName, tt.lvName); err == nil {
				t.Fatal("expected a validation error, got nil")
			}
		})
	}
}

// TestIsQuarantined covers tag matching against the comma-separated list LVM
// reports, including names that merely contain the tag.
func TestIsQuarantined(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tags string
		want bool
	}{
		{name: "no tags", tags: "", want: false},
		{name: "only the wipe tag", tags: lvm.WipePendingTag, want: true},
		{name: "wipe tag first", tags: lvm.WipePendingTag + ",local-csi", want: true},
		{name: "wipe tag last", tags: "local-csi," + lvm.WipePendingTag, want: true},
		{name: "wipe tag in the middle", tags: "a," + lvm.WipePendingTag + ",b", want: true},
		{name: "unrelated tags", tags: "local-csi,other", want: false},
		{name: "tag with a common prefix", tags: "local-csi-wipe-pending-2", want: false},
		{name: "tag with a common suffix", tags: "x-local-csi-wipe-pending", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := lvm.IsQuarantined(lvmMgr.LogicalVolume{Tags: tt.tags})
			if got != tt.want {
				t.Errorf("IsQuarantined(%q) = %v, want %v", tt.tags, got, tt.want)
			}
		})
	}
}

// TestListQuarantinedSelectsCommittedVolumes verifies which volumes the reaper
// is offered.
//
// Discovery must not depend on the tag. A committed volume can legitimately
// have lost its tag, if EnsureVolume's reinstatement landed between the tag
// and the rename, and selecting on the tag would hide it from the reaper
// forever while it still held the tenant's data. Equally, a tagged volume that
// was never renamed is still in service and must not be offered. A name that
// merely starts with the prefix is not a commit marker either.
func TestListQuarantinedSelectsCommittedVolumes(t *testing.T) {
	t.Parallel()

	committed := "local-csi-wipe-" + uuid.NewString()
	untaggedCommitted := "local-csi-wipe-" + uuid.NewString()

	l := newQuarantineTestLVM(t, func(m *lvmMgr.MockManager) {
		m.EXPECT().
			ListLogicalVolumes(gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, opts *lvmMgr.ListLVOptions) ([]lvmMgr.LogicalVolume, error) {
				if opts.Select != "" {
					t.Errorf("selector was %q, want no tag selector", opts.Select)
				}
				return []lvmMgr.LogicalVolume{
					{Name: committed, Tags: lvm.WipePendingTag},
					// Committed but untagged: still a wipe target.
					{Name: untaggedCommitted},
					// Tagged but never renamed: still in service.
					{Name: "live-volume", Tags: lvm.WipePendingTag},
					// Prefix without a generated UUID: not a commit marker.
					{Name: "local-csi-wipe-anything", Tags: lvm.WipePendingTag},
					{Name: "other-volume", Tags: "local-csi"},
				}, nil
			})
	})

	got, err := l.ListQuarantined(context.Background(), testVolumeGroup)
	if err != nil {
		t.Fatalf("ListQuarantined returned error: %v", err)
	}

	names := make(map[string]bool, len(got))
	for _, lv := range got {
		names[lv.Name] = true
	}

	if len(got) != 2 || !names[committed] || !names[untaggedCommitted] {
		t.Fatalf("returned %+v, want exactly the two committed volumes", got)
	}
}

// TestListQuarantinedPropagatesError verifies that a listing failure is not
// reported as an empty list.
//
// Returning no volumes on error would make the reaper believe there is nothing
// to wipe.
func TestListQuarantinedPropagatesError(t *testing.T) {
	t.Parallel()

	l := newQuarantineTestLVM(t, func(m *lvmMgr.MockManager) {
		m.EXPECT().
			ListLogicalVolumes(gomock.Any(), gomock.Any()).
			Return(nil, errors.New("list failed"))
	})

	if _, err := l.ListQuarantined(context.Background(), testVolumeGroup); err == nil {
		t.Fatal("expected the listing error to be propagated, got nil")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestReaperFailsClosedOnUnknownOpenState covers the decode of LVM's open
// state.
//
// lv_device_open decodes to false both when the device is closed and when the
// field is missing or unparsable, so on its own a renamed or dropped field
// would silently degrade into "safe to wipe". An lv_attr too short to carry
// the open indicator must therefore be treated as open.
func TestReaperFailsClosedOnUnknownOpenState(t *testing.T) {
	t.Parallel()

	unknown := quarantinedLV(testWipeName)
	unknown.Attributes = ""
	unknown.DeviceOpen = lvmMgr.BoolString(false)

	r := newTestReaper(t, func(m *lvmMgr.MockManager) {
		expectVolumeGroup(m)
		expectQuarantined(m, unknown)

		m.EXPECT().UpdateLogicalVolume(gomock.Any(), gomock.Any()).Return(nil)

		// No Sanitize or Remove: gomock fails the test if either is called.
	})

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
}

// TestQuarantineNamespaceIsReserved verifies that a volume handle cannot name
// itself into the quarantine namespace.
//
// The generated name is the commit marker the reaper acts on, and the logical
// volume name comes from the CSI volume handle, which is operator-supplied for
// a pre-provisioned PersistentVolume. A handle inside that namespace would be
// treated as awaiting destruction and zeroed.
func TestQuarantineNamespaceIsReserved(t *testing.T) {
	t.Parallel()

	l := newQuarantineTestLVM(t, nil)

	reserved := testVolumeGroup + "#local-csi-wipe-" + uuid.NewString()

	// Delete reports success for an unparsable ID, so the assertion is that
	// nothing is quarantined: the mock has no expectations, so any LVM call
	// fails the test.
	if err := l.Delete(context.Background(), &csi.DeleteVolumeRequest{VolumeId: reserved}); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if _, err := l.EnsureVolume(context.Background(), reserved, 1<<30, 0, true); err == nil {
		t.Error("expected EnsureVolume to reject a volume id in the quarantine namespace")
	}
}

// TestClearQuarantineTagRefusalMatchesCommitSet verifies that the reinstate
// path refuses exactly the volumes the reaper acts on, and no more.
//
// If the refusal set were wider than the commit set, a volume that merely
// resembled a quarantined one could never be reinstated by EnsureVolume nor
// reclaimed by the reaper, leaving it permanently stuck.
func TestClearQuarantineTagRefusalMatchesCommitSet(t *testing.T) {
	t.Parallel()

	committed := "local-csi-wipe-" + uuid.NewString()

	l := newQuarantineTestLVM(t, nil)
	if err := l.ClearQuarantineTag(context.Background(), testVolumeGroup, committed); err == nil {
		t.Error("expected ClearQuarantineTag to refuse a committed volume")
	}

	// Same prefix, but no generated UUID, so it was never committed and is a
	// legal volume name that must remain reinstateable.
	lookalike := "local-csi-wipe-notauuid"

	l = newQuarantineTestLVM(t, func(m *lvmMgr.MockManager) {
		m.EXPECT().
			UpdateLogicalVolume(gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, opts lvmMgr.UpdateLVOptions) error {
				if got, want := opts.Name, testVolumeGroup+"/"+lookalike; got != want {
					t.Errorf("cleared tag on %q, want %q", got, want)
				}
				return nil
			})
	})
	if err := l.ClearQuarantineTag(context.Background(), testVolumeGroup, lookalike); err != nil {
		t.Errorf("ClearQuarantineTag() error = %v", err)
	}
}
