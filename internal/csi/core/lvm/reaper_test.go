// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"
	kevents "k8s.io/client-go/tools/events"

	"local-csi-driver/internal/csi/core/lvm"
	lvmMgr "local-csi-driver/internal/pkg/lvm"
)

// newTestReaper builds a reaper over an LVM core backed by a mock manager.
func newTestReaper(t *testing.T, expect func(*lvmMgr.MockManager)) *lvm.Reaper {
	t.Helper()

	l := newQuarantineTestLVM(t, expect)

	r, err := lvm.NewReaper(l, kevents.NewFakeRecorder(16), lvm.ReaperConfig{})
	if err != nil {
		t.Fatal(err)
	}

	return r
}

// expectVolumeGroup makes the mock report a single tagged volume group.
func expectVolumeGroup(m *lvmMgr.MockManager) {
	m.EXPECT().
		ListVolumeGroups(gomock.Any(), gomock.Any()).
		Return([]lvmMgr.VolumeGroup{{Name: testVolumeGroup}}, nil).
		AnyTimes()
}

// Quarantined volume names must have the shape Quarantine generates, prefix
// and UUID both, because that generated name is the commit marker the reaper
// keys off. A name that merely starts with the prefix is deliberately not
// recognised.
var (
	testWipeName          = "local-csi-wipe-" + uuid.NewString()
	testWipeNameInherited = "local-csi-wipe-" + uuid.NewString()
)

// quarantinedLV is a logical volume as the reaper sees it after quarantine.
func quarantinedLV(name string) lvmMgr.LogicalVolume {
	return lvmMgr.LogicalVolume{
		Name:   name,
		VGName: testVolumeGroup,
		Tags:   lvm.WipePendingTag,
		// lv_attr for an active linear volume that nothing has open. The
		// reaper treats an absent or too-short attribute string as "open", so
		// a realistic value is required for the volume to be wipeable.
		Attributes: "-wi-a-----",
	}
}

// expectQuarantined makes the mock report the given quarantined volumes, both
// from the sweep listing and from the re-read the reaper performs immediately
// before wiping each one.
func expectQuarantined(m *lvmMgr.MockManager, lvs ...lvmMgr.LogicalVolume) {
	m.EXPECT().
		ListLogicalVolumes(gomock.Any(), gomock.Any()).
		Return(lvs, nil).
		AnyTimes()

	for _, lv := range lvs {
		m.EXPECT().
			GetLogicalVolume(gomock.Any(), lv.VGName, lv.Name).
			Return(&lv, nil).
			AnyTimes()
	}
}

// TestReaperWipesBeforeRemoving pins the ordering that makes the reaper safe.
//
// Removal must come last. Once lvremove has run, the extents are back in the
// volume group's free pool and can be handed to another tenant at any time, so
// there is no longer anywhere to write the zeroes. Activation must come first,
// because a volume can be quarantined while deactivated and a volume with no
// device node cannot be written to at all.
func TestReaperWipesBeforeRemoving(t *testing.T) {
	t.Parallel()

	var calls []string

	r := newTestReaper(t, func(m *lvmMgr.MockManager) {
		expectVolumeGroup(m)
		expectQuarantined(m, quarantinedLV(testWipeName))

		m.EXPECT().
			UpdateLogicalVolume(gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, opts lvmMgr.UpdateLVOptions) error {
				calls = append(calls, "activate")
				if got, want := opts.Name, testVolumeGroup+"/"+testWipeName; got != want {
					t.Errorf("activated %q, want %q", got, want)
				}
				if opts.Activate == nil || !bool(*opts.Activate) {
					t.Error("expected the volume to be activated")
				}
				return nil
			})

		m.EXPECT().
			SanitizeLogicalVolume(gomock.Any(), testVolumeGroup, testWipeName).
			DoAndReturn(func(context.Context, string, string) error {
				calls = append(calls, "sanitize")
				return nil
			})

		m.EXPECT().
			RemoveLogicalVolume(gomock.Any(), lvmMgr.RemoveLVOptions{Name: testVolumeGroup + "/" + testWipeName}).
			DoAndReturn(func(context.Context, lvmMgr.RemoveLVOptions) error {
				calls = append(calls, "remove")
				return nil
			})
	})

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if got, want := strings.Join(calls, ","), "activate,sanitize,remove"; got != want {
		t.Errorf("call order = %q, want %q", got, want)
	}
}

// TestReaperRetainsVolumeOnSanitizeFailure covers the fail-closed contract.
//
// A volume that could not be zeroed keeps its extents and its tag. Removing it
// would return unsanitized extents to the free pool, which is the disclosure
// this whole mechanism exists to prevent. Losing the capacity is the intended
// trade: capacity is recoverable by an operator, disclosed data is not.
func TestReaperRetainsVolumeOnSanitizeFailure(t *testing.T) {
	t.Parallel()

	errSanitize := errors.New("device write error")

	r := newTestReaper(t, func(m *lvmMgr.MockManager) {
		expectVolumeGroup(m)
		expectQuarantined(m, quarantinedLV(testWipeName))

		m.EXPECT().UpdateLogicalVolume(gomock.Any(), gomock.Any()).Return(nil)
		m.EXPECT().
			SanitizeLogicalVolume(gomock.Any(), testVolumeGroup, testWipeName).
			Return(errSanitize)

		// The absence of a RemoveLogicalVolume expectation is the assertion:
		// gomock fails the test if it is called.
	})

	// The sweep itself succeeds. One volume that cannot be wiped must not stop
	// the reaper from clearing the others.
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
}

// TestReaperDoesNotSanitizeUnactivatedVolume checks that a volume which cannot
// be activated is left alone rather than being removed.
//
// Sanitizing writes through /dev/<vg>/<lv>. If activation fails that path does
// not exist, so there is no way to clear the extents and the only safe outcome
// is to keep holding them.
func TestReaperDoesNotSanitizeUnactivatedVolume(t *testing.T) {
	t.Parallel()

	r := newTestReaper(t, func(m *lvmMgr.MockManager) {
		expectVolumeGroup(m)
		expectQuarantined(m, quarantinedLV(testWipeName))

		m.EXPECT().
			UpdateLogicalVolume(gomock.Any(), gomock.Any()).
			Return(errors.New("volume group not found"))

		// Neither sanitize nor remove may be called.
	})

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
}

// TestReaperSkipsUnquarantinedVolumes checks that the reaper only ever acts on
// tagged volumes.
//
// The tag selector is applied by LVM, but the reaper must not depend on that:
// if the selector ever stopped being applied, acting on the unfiltered list
// would destroy live tenant data.
func TestReaperSkipsUnquarantinedVolumes(t *testing.T) {
	t.Parallel()

	r := newTestReaper(t, func(m *lvmMgr.MockManager) {
		expectVolumeGroup(m)
		expectQuarantined(m, lvmMgr.LogicalVolume{
			Name:   "live-volume",
			VGName: testVolumeGroup,
		})

		// No activate, sanitize, or remove may be called.
	})

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
}

// TestReaperBacksOffAfterFailure checks that a volume that failed to wipe is
// not retried on the very next sweep.
//
// Without this, a persistently failing device would make the reaper spin,
// re-reading and re-attempting the same volume on every signal and every tick.
func TestReaperBacksOffAfterFailure(t *testing.T) {
	t.Parallel()

	r := newTestReaper(t, func(m *lvmMgr.MockManager) {
		expectVolumeGroup(m)
		expectQuarantined(m, quarantinedLV(testWipeName))

		// Exactly one attempt, despite two sweeps.
		m.EXPECT().UpdateLogicalVolume(gomock.Any(), gomock.Any()).Return(nil).Times(1)
		m.EXPECT().
			SanitizeLogicalVolume(gomock.Any(), testVolumeGroup, testWipeName).
			Return(errors.New("device write error")).
			Times(1)
	})

	ctx := context.Background()
	for i := range 2 {
		if err := r.Reconcile(ctx); err != nil {
			t.Fatalf("Reconcile() %d error = %v", i, err)
		}
	}
}

// TestReaperTreatsMissingVolumeAsRemoved checks idempotency of the removal
// step.
//
// The volume was sanitized before lvremove was attempted, so a volume that has
// already gone is a success, not a failure to retry.
func TestReaperTreatsMissingVolumeAsRemoved(t *testing.T) {
	t.Parallel()

	r := newTestReaper(t, func(m *lvmMgr.MockManager) {
		expectVolumeGroup(m)
		expectQuarantined(m, quarantinedLV(testWipeName))

		m.EXPECT().UpdateLogicalVolume(gomock.Any(), gomock.Any()).Return(nil)
		m.EXPECT().SanitizeLogicalVolume(gomock.Any(), testVolumeGroup, testWipeName).Return(nil)
		m.EXPECT().
			RemoveLogicalVolume(gomock.Any(), gomock.Any()).
			Return(lvmMgr.ErrNotFound)
	})

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
}

// TestReaperSweepsOnStartup checks that the reaper wipes volumes it was never
// signalled about.
//
// This is what makes quarantine crash-safe. A volume quarantined by a previous
// process, or by a process that died before the wipe finished, has no pending
// signal and must still be found.
func TestReaperSweepsOnStartup(t *testing.T) {
	t.Parallel()

	removed := make(chan string, 1)

	r := newTestReaper(t, func(m *lvmMgr.MockManager) {
		expectVolumeGroup(m)
		expectQuarantined(m, quarantinedLV(testWipeNameInherited))

		m.EXPECT().UpdateLogicalVolume(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		m.EXPECT().SanitizeLogicalVolume(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		m.EXPECT().
			RemoveLogicalVolume(gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, opts lvmMgr.RemoveLVOptions) error {
				select {
				case removed <- opts.Name:
				default:
				}
				return nil
			}).
			AnyTimes()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()

	select {
	case name := <-removed:
		if got, want := name, testVolumeGroup+"/"+testWipeNameInherited; got != want {
			t.Errorf("removed %q, want %q", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reaper did not sweep on startup")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start() error = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reaper did not stop on context cancellation")
	}
}

// TestReaperSignalDoesNotBlock checks that signalling is safe from the
// deletion path.
//
// DeleteVolume signals the reaper while holding a CSI request under a sidecar
// timeout. If the signal could block on a wipe that is already running, a
// deletion would stall behind a multi-minute zeroing operation.
func TestReaperSignalDoesNotBlock(t *testing.T) {
	t.Parallel()

	r := newTestReaper(t, nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		// More signals than the channel can hold. Extra signals are dropped
		// because a pending wakeup already covers them: the next sweep
		// processes everything it finds.
		for range 100 {
			r.Signal()
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Signal() blocked")
	}
}
