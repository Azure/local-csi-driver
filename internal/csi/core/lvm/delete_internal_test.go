// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm

import (
	"context"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"go.uber.org/mock/gomock"

	lvmMgr "local-csi-driver/internal/pkg/lvm"
	"local-csi-driver/internal/pkg/probe"
	"local-csi-driver/internal/pkg/telemetry"
)

// TestDeleteSignalsWipeReaper checks that a deletion wakes the reaper.
//
// Without the signal, a deleted volume's capacity would not be reclaimed until
// the next backstop sweep, which is far longer than a workload that deletes
// and immediately recreates a volume would tolerate.
//
// This test is internal because the signal is deliberately not exposed: the
// deletion path signals unconditionally and nothing in the driver needs to
// inspect it.
func TestDeleteSignalsWipeReaper(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	m := lvmMgr.NewMockManager(ctrl)
	m.EXPECT().UpdateLogicalVolume(gomock.Any(), gomock.Any()).Return(nil)
	m.EXPECT().RenameLogicalVolume(gomock.Any(), gomock.Any()).Return(nil)

	l, err := New(
		"test-pod", "test-node", "test-namespace", false,
		probe.NewFake([]string{"device1"}, nil), m,
		telemetry.NewNoopTracerProvider(),
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := l.Delete(context.Background(), &csi.DeleteVolumeRequest{
		VolumeId: "test-vg#test-lv",
	}); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	select {
	case <-l.wipeSignal:
	default:
		t.Error("Delete() did not signal the wipe reaper")
	}
}
