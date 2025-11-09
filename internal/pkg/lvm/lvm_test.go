//go:build linux && cgo

// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

/* SPDX-License-Identifier: Apache-1.0
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

package lvm_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os/exec"
	"os/user"
	"strings"
	"testing"

	"local-csi-driver/internal/pkg/lvm"
	testUtils "local-csi-driver/test/pkg/utils/device"
)

var (
	GiB = 1 << 30
	MiB = 1 << 20
)

func TestClient(t *testing.T) { //nolint:gocyclo

	// Can run only as root
	if !isRoot() {
		t.Skip("Skipping TestClient as it requires root permissions.")
	}

	c := lvm.NewClient()

	t.Run("lvm2 is supported", func(t *testing.T) {
		if got := c.IsSupported(); got != true {
			t.Errorf("lvm2 is supported = %v, want %v", got, true)
		}
	})

	t.Run("Physical volumes", func(t *testing.T) {
		ctx := context.Background()

		loopDev, cleanupLoopDev, err := testUtils.CreateLoopDevWithSize(int64(1 * GiB))
		if err != nil {
			t.Fatalf("failed to create loop device: %v", err)
		}
		t.Cleanup(func() { cleanupLoopDev() })

		err = c.CreatePhysicalVolume(ctx, lvm.CreatePVOptions{
			Name: loopDev,
		})
		if err != nil {
			t.Fatalf("failed to create PV: %v", err)
		}

		pv, err := c.GetPhysicalVolume(ctx, loopDev)
		if err != nil {
			t.Fatalf("failed to get PV: %v", err)
		}
		if pv == nil {
			t.Errorf("expected PV to be non-empty")
		}

		pvs, err := c.ListPhysicalVolumes(ctx, &lvm.ListPVOptions{
			Names: []string{loopDev},
		})
		if err != nil {
			t.Fatalf("failed to list PVs: %v", err)
		}
		if len(pvs) != 1 {
			t.Errorf("expected 1 PV, got %d", len(pvs))
		}
		if pvs[0].Name != loopDev {
			t.Errorf("expected PV name %s, got %s", loopDev, pvs[0].Name)
		}

		err = c.RemovePhysicalVolume(ctx, lvm.RemovePVOptions{
			Name: loopDev,
		})
		if err != nil {
			t.Fatalf("failed to remove PV: %v", err)
		}

		pvs, err = c.ListPhysicalVolumes(ctx, &lvm.ListPVOptions{
			Names: []string{loopDev},
		})
		if err == nil || !strings.Contains(err.Error(), "Failed to find physical volume") {
			t.Errorf("expected error containing 'Failed to find physical volume', got %v", err)
		}
		if len(pvs) != 0 {
			t.Errorf("expected 0 PVs, got %d", len(pvs))
		}
	})

	t.Run("Volume groups", func(t *testing.T) {
		vgName := uniqueName(strings.ReplaceAll(t.Name(), "/", "_"))
		vgTag := uniqueName("tag-")
		ctx := context.Background()

		loopDev1, cleanupLoopDev1, err := testUtils.CreateLoopDevWithSize(int64(1 * GiB))
		if err != nil {
			t.Fatalf("failed to create loop device: %v", err)
		}
		loopDev2, cleanupLoopDev2, err := testUtils.CreateLoopDevWithSize(int64(1 * GiB))
		if err != nil {
			t.Fatalf("failed to create loop device: %v", err)
		}
		// Register cleanup functions to remove the PVs after the test
		t.Cleanup(func() {
			cleanupLoopDev1()
			cleanupLoopDev2()
		})

		err = c.CreatePhysicalVolume(ctx, lvm.CreatePVOptions{
			Name: loopDev1,
		})
		if err != nil {
			t.Fatalf("failed to create first PV: %v", err)
		}
		err = c.CreatePhysicalVolume(ctx, lvm.CreatePVOptions{
			Name: loopDev2,
		})
		if err != nil {
			t.Fatalf("failed to create second PV: %v", err)
		}

		err = c.CreateVolumeGroup(ctx, lvm.CreateVGOptions{
			Name:    vgName,
			PVNames: []string{loopDev1, loopDev2},
			Tags:    []string{vgTag},
		})
		if err != nil {
			t.Fatalf("failed to create VG: %v", err)
		}
		// Register cleanup functions to remove the VG after the test
		t.Cleanup(func() {
			_ = c.UpdateVolumeGroup(ctx, lvm.UpdateVGOptions{
				Name:     vgName,
				Activate: lvm.No,
			})

			_ = c.RemoveVolumeGroup(ctx, lvm.RemoveVGOptions{
				Name: vgName,
			})
		})

		vg, err := c.GetVolumeGroup(ctx, vgName)
		if err != nil {
			t.Fatalf("failed to get VG: %v", err)
		}
		if vg.Name != vgName {
			t.Errorf("expected VG name %s, got %s", vgName, vg.Name)
		}
		if int(vg.PVCount) != 2 {
			t.Errorf("expected 2 PVs, got %d", int(vg.PVCount))
		}

		vgs, err := c.ListVolumeGroups(ctx, &lvm.ListVGOptions{
			Names: []string{vgName},
		})
		if err != nil {
			t.Fatalf("failed to list VGs: %v", err)
		}

		if len(vgs) != 1 {
			t.Errorf("expected 1 VG, got %d", len(vgs))
		}
		if vgs[0].Name != vgName {
			t.Errorf("expected VG name %s, got %s", vgName, vgs[0].Name)
		}
		if int(vgs[0].PVCount) != 2 {
			t.Errorf("expected 2 PVs, got %d", int(vgs[0].PVCount))
		}

		err = c.UpdateVolumeGroup(ctx, lvm.UpdateVGOptions{
			Name:     vgName,
			Activate: lvm.Yes,
		})
		if err != nil {
			t.Fatalf("failed to activate VG: %v", err)
		}

		t.Log("Removing Non-existent volume group")

		err = c.RemoveVolumeGroup(ctx, lvm.RemoveVGOptions{
			Name: "NoVolumeGroup",
		})
		if !errors.Is(err, lvm.ErrNotFound) {
			t.Fatalf("failed to remove logical volume: %v", err)
		}

		t.Log("Removing volume group")

		err = c.RemoveVolumeGroup(ctx, lvm.RemoveVGOptions{
			Name: vgName,
		})
		if err != nil {
			t.Fatalf("failed to remove VG: %v", err)
		}

		vgs, err = c.ListVolumeGroups(ctx, &lvm.ListVGOptions{
			Names: []string{vgName},
		})
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected error containing 'not found', got %v", err)
		}
		if len(vgs) != 0 {
			t.Errorf("expected 0 VGs, got %d", len(vgs))
		}
	})

	t.Run("Logical volumes", func(t *testing.T) {

		t.Log("Creating loopback block device Logical Volumes")
		loopDev, cleanupLoopDev1, err := testUtils.CreateLoopDevWithSize(int64(1 * GiB))
		if err != nil {
			t.Fatalf("failed to create loop device: %v", err)
		}

		// Register cleanup functions to remove the PVs after the test
		t.Cleanup(func() {
			cleanupLoopDev1()
		})

		t.Log("Creating physical volumes")

		ctx := context.Background()

		err = c.CreatePhysicalVolume(ctx, lvm.CreatePVOptions{
			Name: loopDev,
		})
		if err != nil {
			t.Fatalf("failed to create PV: %v", err)
		}

		t.Log("Creating volume group")

		vgName := uniqueName(strings.ReplaceAll(t.Name(), "/", "_"))
		vgTag := uniqueName("tag-")

		err = c.CreateVolumeGroup(ctx, lvm.CreateVGOptions{
			Name:    vgName,
			PVNames: []string{loopDev},
			Tags:    []string{vgTag},
		})
		if err != nil {
			t.Fatalf("failed to create VG: %v", err)
		}

		// Register cleanup functions to remove the VG after the test
		t.Cleanup(func() {
			_ = c.UpdateVolumeGroup(ctx, lvm.UpdateVGOptions{
				Name:     vgName,
				Activate: lvm.No,
			})

			_ = c.RemoveVolumeGroup(ctx, lvm.RemoveVGOptions{
				Name: vgName,
			})
		})

		lvName := uniqueName(strings.ReplaceAll(t.Name(), "/", "_"))

		t.Log("Creating logical volume", lvName)

		_, err = c.CreateLogicalVolume(ctx, lvm.CreateLVOptions{
			Name:     lvName,
			VGName:   vgName,
			Size:     "100M",
			Activate: lvm.No,
		})
		if err != nil {
			t.Fatalf("failed to create LV: %v", err)
		}

		t.Log("Activating volume group")
		err = c.UpdateVolumeGroup(ctx, lvm.UpdateVGOptions{
			Name:     vgName,
			Activate: lvm.Yes,
		})
		if err != nil {
			t.Fatalf("failed to activate LV: %v", err)
		}

		lv, err := c.GetLogicalVolume(ctx, vgName, lvName)
		if err != nil {
			t.Fatalf("failed to get LV: %v", err)
		}
		if int64(lv.Size) != int64(100*MiB) {
			t.Errorf("expected LV size %d, got %d", int64(100*MiB), int64(lv.Size))
		}

		lvs, err := c.ListLogicalVolumes(ctx, &lvm.ListLVOptions{
			Names: []string{
				fmt.Sprintf("%s/%s", vgName, lvName),
			},
		})
		if err != nil {
			t.Fatalf("failed to list LVs: %v", err)
		}

		if len(lvs) != 1 {
			t.Errorf("expected 1 LV, got %d", len(lvs))
		}
		if lvs[0].Name != lvName {
			t.Errorf("expected LV name %s, got %s", lvName, lvs[0].Name)
		}
		if int64(lvs[0].Size) != int64(100*MiB) {
			t.Errorf("expected LV size %d, got %d", int64(100*MiB), int64(lvs[0].Size))
		}

		t.Log("Resizing logical volume")
		err = c.ExtendLogicalVolume(ctx, lvm.ExtendLVOptions{
			Name: fmt.Sprintf("%s/%s", vgName, lvName),
			Size: "200M",
		})
		if err != nil {
			t.Fatalf("failed to extend LV: %v", err)
		}

		lvs, err = c.ListLogicalVolumes(ctx, &lvm.ListLVOptions{
			Names: []string{
				fmt.Sprintf("%s/%s", vgName, lvName),
			},
		})
		if err != nil {
			t.Fatalf("failed to list LVs: %v", err)
		}

		if len(lvs) != 1 {
			t.Errorf("expected 1 LV, got %d", len(lvs))
		}
		if int64(lvs[0].Size) != int64(200*MiB) {
			t.Errorf("expected LV size %d, got %d", int64(200*MiB), int64(lvs[0].Size))
		}

		t.Log("Removing logical volume from volume group")

		err = c.RemoveLogicalVolume(ctx, lvm.RemoveLVOptions{
			Name: fmt.Sprintf("%s/%s", vgName, lvName),
		})
		if err != nil {
			t.Fatalf("failed to remove logical volume: %v", err)
		}
	})

	t.Run("Negative Tests For PV VG LV", func(t *testing.T) {
		vgName := uniqueName(strings.ReplaceAll(t.Name(), "/", "_"))
		vgTag := uniqueName("tag-")
		lvName := uniqueName(strings.ReplaceAll(t.Name(), "/", "_"))
		ctx := context.Background()

		// Create loopback device and the cleanup function to destroy it.
		t.Log("Creating loopback block device Logical Volumes")
		loopDev, cleanupLoopDev1, err := testUtils.CreateLoopDevWithSize(int64(1 * GiB))
		if err != nil {
			t.Fatalf("failed to create loop device: %v", err)
		}
		// Register cleanup functions to remove the PVs after the test.
		t.Cleanup(func() {
			cleanupLoopDev1()
		})

		// Create PV, VG and their respective cleanup functions.
		t.Log("Creating physical volumes")
		err = c.CreatePhysicalVolume(ctx, lvm.CreatePVOptions{
			Name: loopDev,
		})
		if err != nil {
			t.Fatalf("failed to create PV: %v", err)
		}
		t.Log("Creating volume group")
		err = c.CreateVolumeGroup(ctx, lvm.CreateVGOptions{
			Name:    vgName,
			PVNames: []string{loopDev},
			Tags:    []string{vgTag},
		})
		if err != nil {
			t.Fatalf("failed to create VG: %v", err)
		}
		// Register cleanup functions to remove the VG, PV after the test.
		t.Cleanup(func() {
			_ = c.UpdateVolumeGroup(ctx, lvm.UpdateVGOptions{
				Name:     vgName,
				Activate: lvm.No,
			})

			_ = c.RemoveVolumeGroup(ctx, lvm.RemoveVGOptions{
				Name: vgName,
			})

			err = c.RemovePhysicalVolume(ctx, lvm.RemovePVOptions{
				Name: loopDev,
			})
		})

		// Create logical volume.
		t.Log("Creating logical volume", lvName)
		_, err = c.CreateLogicalVolume(ctx, lvm.CreateLVOptions{
			Name:     lvName,
			VGName:   vgName,
			Size:     "100M",
			Activate: lvm.No,
		})
		if err != nil {
			t.Fatalf("failed to create LV: %v", err)
		}

		// Check with a non-existent device.
		t.Log("Checking for non-existent block device /dev/null/loop0")
		pv, err := c.GetPhysicalVolume(ctx, "/dev/null/loop0")
		if !errors.Is(err, lvm.ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
		if pv != nil {
			t.Errorf("expected empty PV, got %v", pv)
		}

		// Check with a non-existent Volume Group.
		t.Log("Checking for non-existent volume Group ")
		vg, err := c.GetVolumeGroup(ctx, "NoVolGroup")
		if !errors.Is(err, lvm.ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
		if vg != nil {
			t.Errorf("expected empty VG, got %v", vg)
		}

		// Check with a non-existent Logical Volume on an existing VG.
		t.Log("Checking for non-existent Logical Volume")
		lv, err := c.GetLogicalVolume(ctx, vgName, "NoLogicalVol")
		if !errors.Is(err, lvm.ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
		if lv != nil {
			t.Errorf("expected empty LV, got %v", lv)
		}

		// Check for a Logical Volume on an non-existing VG
		t.Log("Checking for non-existent Logical Volume")
		lv, err = c.GetLogicalVolume(ctx, "NoVolumeGroup", lvName)

		if !errors.Is(err, lvm.ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
		if lv != nil {
			t.Errorf("expected empty LV, got %v", lv)
		}

		t.Log("Removing logical volume without specifying volume group in required format")
		err = c.RemoveLogicalVolume(ctx, lvm.RemoveLVOptions{
			Name: "test",
		})
		if !errors.Is(err, lvm.ErrInvalidInput) {
			t.Fatalf("expected invalid input error: %v", err)
		}

		t.Log("Removing logical volume with non-existent volume group")
		err = c.RemoveLogicalVolume(ctx, lvm.RemoveLVOptions{
			Name: fmt.Sprintf("%s/%s", "NoVolumeGroup", lvName),
		})
		if !errors.Is(err, lvm.ErrNotFound) {
			t.Fatalf("failed to remove logical volume: %v", err)
		}

		t.Log("Removing logical volume with non-existent Logical Volume")
		err = c.RemoveLogicalVolume(ctx, lvm.RemoveLVOptions{
			Name: fmt.Sprintf("%s/%s", vgName, "NoLv"),
		})
		if !errors.Is(err, lvm.ErrNotFound) {
			t.Fatalf("failed to remove logical volume: %v", err)
		}

		t.Log("Removing logical volume from volume group in proper manner")
		err = c.RemoveLogicalVolume(ctx, lvm.RemoveLVOptions{
			Name: fmt.Sprintf("%s/%s", vgName, lvName),
		})
		if err != nil {
			t.Fatalf("failed to remove logical volume: %v", err)
		}
	})
}

func TestIgnoreNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{
			name: "ErrNotFound",
			err:  lvm.ErrNotFound,
			want: nil,
		},
		{
			name: "nil error",
			err:  nil,
			want: nil,
		},
		{
			name: "some error",
			err:  lvm.ErrInvalidInput,
			want: lvm.ErrInvalidInput,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := lvm.IgnoreNotFound(tt.err); got != tt.want {
				t.Errorf("IgnoreNotFound() = %v, want %v", got, tt.want)
			}
		})
	}
}

func isRoot() bool {
	if u, err := user.Current(); err != nil || u.Uid != "0" {
		return false
	}
	return true
}

func uniqueName(prefix string) string {
	return prefix + "_" + randString(8)
}

func randString(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		bigIndex, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			panic(err)
		}

		b[i] = alphabet[bigIndex.Int64()]
	}
	return string(b)
}

func TestIsLogicalVolumeCorrupted(t *testing.T) {
	// Can run only as root
	if !isRoot() {
		t.Skip("Skipping TestIsLogicalVolumeCorrupted as it requires root permissions.")
	}

	c := lvm.NewClient()
	ctx := context.Background()

	// Test with non-existent VG/LV
	t.Run("NonExistentLogicalVolume", func(t *testing.T) {
		corrupted, err := c.IsLogicalVolumeCorrupted(ctx, "nonexistent-vg", "nonexistent-lv")
		if err != nil {
			t.Fatalf("Expected no error for non-existent LV, got: %v", err)
		}
		if corrupted {
			t.Fatalf("Expected non-existent LV to not be corrupted")
		}
	})

	// Test with empty VG/LV names
	t.Run("EmptyNames", func(t *testing.T) {
		_, err := c.IsLogicalVolumeCorrupted(ctx, "", "")
		if err == nil {
			t.Fatalf("Expected error for empty VG/LV names")
		}
	})

	// Create test devices and VG for actual corruption testing
	device1, cleanup1, err := testUtils.CreateLoopDevWithSize(int64(GiB))
	if err != nil {
		t.Fatalf("Failed to create first test device: %v", err)
	}
	defer cleanup1()

	device2, cleanup2, err := testUtils.CreateLoopDevWithSize(int64(GiB))
	if err != nil {
		t.Fatalf("Failed to create second test device: %v", err)
	}
	defer cleanup2()

	devices := []string{device1, device2}

	vgName := fmt.Sprintf("test-vg-%d", mustRandomInt(1000))
	lvName := fmt.Sprintf("test-lv-%d", mustRandomInt(1000))

	// Create PVs
	for _, device := range devices {
		err := c.CreatePhysicalVolume(ctx, lvm.CreatePVOptions{
			Name: device,
		})
		if err != nil {
			t.Fatalf("Failed to create PV on %s: %v", device, err)
		}
		defer func(device string) {
			_ = c.RemovePhysicalVolume(ctx, lvm.RemovePVOptions{Name: device})
		}(device)
	}

	// Create VG
	err = c.CreateVolumeGroup(ctx, lvm.CreateVGOptions{
		Name:    vgName,
		PVNames: devices,
	})
	if err != nil {
		t.Fatalf("Failed to create VG: %v", err)
	}
	defer func() {
		_ = c.RemoveVolumeGroup(ctx, lvm.RemoveVGOptions{Name: vgName})
	}()

	// Create LV
	_, err = c.CreateLogicalVolume(ctx, lvm.CreateLVOptions{
		Name:   lvName,
		VGName: vgName,
		Size:   "100M",
	})
	if err != nil {
		t.Fatalf("Failed to create LV: %v", err)
	}
	defer func() {
		_ = c.RemoveLogicalVolume(ctx, lvm.RemoveLVOptions{
			Name: fmt.Sprintf("%s/%s", vgName, lvName),
		})
	}()

	// Test with healthy LV
	t.Run("HealthyLogicalVolume", func(t *testing.T) {
		corrupted, err := c.IsLogicalVolumeCorrupted(ctx, vgName, lvName)
		if err != nil {
			t.Fatalf("Failed to check LV corruption: %v", err)
		}
		if corrupted {
			t.Fatalf("Expected healthy LV to not be corrupted")
		}
	})

	// Simulate corruption by removing device symlink
	t.Run("CorruptedLogicalVolume", func(t *testing.T) {
		devicePath := lvm.ConstructLogicalVolumePath(vgName, lvName)
		// Remove the device symlink to simulate corruption
		err := exec.Command("rm", devicePath).Run()
		if err != nil {
			t.Fatalf("Failed to remove devicePath: %v", err)
		}

		// Now check if it's detected as corrupted
		corrupted, err := c.IsLogicalVolumeCorrupted(ctx, vgName, lvName)
		if err != nil {
			t.Fatalf("Failed to check LV corruption: %v", err)
		}
		if !corrupted {
			t.Fatalf("Expected LV with missing device symlink to be corrupted")
		}
	})
}

func mustRandomInt(max int) int {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		panic(err)
	}
	return int(n.Int64())
}
