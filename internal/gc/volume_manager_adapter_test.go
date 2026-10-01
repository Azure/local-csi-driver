// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package gc

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/mock/gomock"

	"local-csi-driver/internal/csi/core/lvm"
	lvmMgr "local-csi-driver/internal/pkg/lvm"
	"local-csi-driver/internal/pkg/probe"
	"local-csi-driver/internal/pkg/telemetry"
)

func TestLVMVolumeManagerAdapterRemovesImmediatelyWhenWipeDisabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		removeErr error
		wantErr   error
	}{
		{
			name: "removed",
		},
		{
			name:      "logical volume already absent",
			removeErr: lvmMgr.ErrNotFound,
		},
		{
			name:      "volume group temporarily absent",
			removeErr: lvmMgr.ErrVolumeGroupNotFound,
			wantErr:   lvmMgr.ErrVolumeGroupNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			manager := lvmMgr.NewMockManager(ctrl)
			manager.EXPECT().
				RemoveLogicalVolume(gomock.Any(), lvmMgr.RemoveLVOptions{
					Name: "containerstorage/test-volume",
				}).
				Return(tt.removeErr)

			core, err := lvm.New(
				"test-pod",
				"test-node",
				"test-namespace",
				false,
				probe.NewFake(nil, nil),
				manager,
				telemetry.NewNoopTracerProvider(),
				lvm.WithVolumeWipeEnabled(false),
			)
			if err != nil {
				t.Fatal(err)
			}

			adapter := &lvmVolumeManagerAdapter{
				lvmCore:    core,
				lvmManager: manager,
			}
			err = adapter.DeleteVolume(context.Background(), "containerstorage#test-volume")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("DeleteVolume() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
