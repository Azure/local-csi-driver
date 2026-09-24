// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package gc

import (
	"context"
	"testing"

	"go.uber.org/mock/gomock"

	"local-csi-driver/internal/csi/core/lvm"
	lvmMgr "local-csi-driver/internal/pkg/lvm"
	"local-csi-driver/internal/pkg/probe"
	"local-csi-driver/internal/pkg/telemetry"
)

func TestLVMVolumeManagerAdapterRemovesImmediatelyWhenWipeDisabled(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	manager := lvmMgr.NewMockManager(ctrl)
	manager.EXPECT().
		RemoveLogicalVolume(gomock.Any(), lvmMgr.RemoveLVOptions{
			Name: "containerstorage/test-volume",
		}).
		Return(nil)

	core, err := lvm.New(
		"test-pod",
		"test-node",
		"test-namespace",
		false,
		probe.NewFake(nil, nil),
		manager,
		telemetry.NewNoopTracerProvider(),
	)
	if err != nil {
		t.Fatal(err)
	}
	core.SetVolumeWipeEnabled(false)

	adapter := &lvmVolumeManagerAdapter{
		lvmCore:    core,
		lvmManager: manager,
	}
	if err := adapter.DeleteVolume(context.Background(), "containerstorage#test-volume"); err != nil {
		t.Fatalf("DeleteVolume() error = %v", err)
	}
}
