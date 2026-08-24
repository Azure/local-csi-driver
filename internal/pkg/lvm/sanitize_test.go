// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/unix"

	"local-csi-driver/internal/pkg/block"
	"local-csi-driver/internal/pkg/lvm"
	testUtils "local-csi-driver/test/pkg/utils/device"
)

// lvSize is the size of the logical volumes used by the sanitization tests.
//
// It is large enough to span multiple extents and multiple write-fallback
// buffers, and small enough to keep the tests quick.
const lvSize = 64 * (1 << 20) // 64 MiB.

// writePattern fills the first n bytes of the device at path with a
// recognisable non-zero pattern and returns it.
func writePattern(t *testing.T, path string, n int64) []byte {
	t.Helper()

	pattern := make([]byte, n)
	for i := range pattern {
		pattern[i] = byte(i%251 + 1) // Never zero: 251 is prime, offset by 1.
	}

	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("failed to open %s for writing: %v", path, err)
	}
	defer f.Close() //nolint:errcheck // Sync below reports any deferred error.

	if _, err := f.WriteAt(pattern, 0); err != nil {
		t.Fatalf("failed to write pattern to %s: %v", path, err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("failed to flush pattern to %s: %v", path, err)
	}

	return pattern
}

// readDevice reads the first n bytes of the device at path, bypassing the page
// cache where possible so that what is read reflects the device rather than a
// stale cached copy of it.
func readDevice(t *testing.T, path string, n int64) []byte {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("failed to open %s for reading: %v", path, err)
	}
	defer f.Close() //nolint:errcheck // Read-only.

	// Drop any cached pages for this device so the read goes to the backing
	// store. Without this a test could pass on cache contents alone.
	if err := unix.IoctlSetInt(int(f.Fd()), unix.BLKFLSBUF, 0); err != nil {
		t.Logf("BLKFLSBUF on %s failed, continuing: %v", path, err)
	}

	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, 0); err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}

	return buf
}

// firstNonZero returns the offset of the first non-zero byte, or -1.
func firstNonZero(b []byte) int {
	return bytes.IndexFunc(b, func(r rune) bool { return r != 0 })
}

// newTestVolumeGroup creates a loop-backed volume group and registers cleanup.
func newTestVolumeGroup(t *testing.T, c *lvm.Client) string {
	t.Helper()

	ctx := context.Background()

	device, cleanupLoopDev, err := testUtils.CreateLoopDevWithSize(int64(GiB))
	if err != nil {
		t.Fatalf("failed to create loop device: %v", err)
	}
	t.Cleanup(cleanupLoopDev)

	if err := c.CreatePhysicalVolume(ctx, lvm.CreatePVOptions{Name: device}); err != nil {
		t.Fatalf("failed to create PV: %v", err)
	}
	t.Cleanup(func() { _ = c.RemovePhysicalVolume(ctx, lvm.RemovePVOptions{Name: device}) })

	vgName := fmt.Sprintf("sanvg%d", mustRandomInt(100000))
	if err := c.CreateVolumeGroup(ctx, lvm.CreateVGOptions{Name: vgName, PVNames: []string{device}}); err != nil {
		t.Fatalf("failed to create VG: %v", err)
	}
	t.Cleanup(func() { _ = c.RemoveVolumeGroup(ctx, lvm.RemoveVGOptions{Name: vgName}) })

	return vgName
}

// TestSanitizeLogicalVolume exercises the sanitization primitive against real
// LVM logical volumes on a loop-backed volume group.
func TestSanitizeLogicalVolume(t *testing.T) {
	if !isRoot() {
		t.Skip("Skipping TestSanitizeLogicalVolume as it requires root permissions.")
	}
	if _, err := os.Stat("/sbin/lvm"); err != nil {
		t.Skip("Skipping TestSanitizeLogicalVolume as /sbin/lvm is not available.")
	}

	c := lvm.NewClient(lvm.WithBlockDeviceUtilities(block.New()))
	ctx := context.Background()

	// The whole volume is zeroed, not just the first extent or the first few
	// kilobytes that lvcreate clears by default.
	t.Run("ZeroesEntireVolume", func(t *testing.T) {
		vgName := newTestVolumeGroup(t, c)
		lvName := uniqueName("lv")

		size, err := c.CreateLogicalVolume(ctx, lvm.CreateLVOptions{
			Name:   lvName,
			VGName: vgName,
			Size:   fmt.Sprintf("%db", lvSize),
		})
		if err != nil {
			t.Fatalf("failed to create LV: %v", err)
		}
		defer func() {
			_ = c.RemoveLogicalVolume(ctx, lvm.RemoveLVOptions{Name: vgName + "/" + lvName})
		}()

		path := lvm.ConstructLogicalVolumePath(vgName, lvName)
		pattern := writePattern(t, path, size)

		// Confirm the pattern really is on the device, so that an all-zero
		// result after sanitization cannot be a false pass caused by the write
		// never landing.
		if got := readDevice(t, path, size); !bytes.Equal(got, pattern) {
			t.Fatal("pattern was not written to the volume; test cannot verify sanitization")
		}

		if err := c.SanitizeLogicalVolume(ctx, vgName, lvName); err != nil {
			t.Fatalf("SanitizeLogicalVolume returned error: %v", err)
		}

		if idx := firstNonZero(readDevice(t, path, size)); idx != -1 {
			t.Fatalf("found non-zero byte at offset %d after sanitization", idx)
		}
	})

	// A volume that is claimed by another process must be reported as in use
	// and left alone, rather than being zeroed underneath its consumer.
	t.Run("RefusesVolumeInUse", func(t *testing.T) {
		vgName := newTestVolumeGroup(t, c)
		lvName := uniqueName("lv")

		size, err := c.CreateLogicalVolume(ctx, lvm.CreateLVOptions{
			Name:   lvName,
			VGName: vgName,
			Size:   fmt.Sprintf("%db", lvSize),
		})
		if err != nil {
			t.Fatalf("failed to create LV: %v", err)
		}
		defer func() {
			_ = c.RemoveLogicalVolume(ctx, lvm.RemoveLVOptions{Name: vgName + "/" + lvName})
		}()

		path := lvm.ConstructLogicalVolumePath(vgName, lvName)
		pattern := writePattern(t, path, size)

		// Claim the device exclusively, as a mount or an open raw block
		// consumer would.
		holder, err := os.OpenFile(path, os.O_RDONLY|unix.O_EXCL, 0)
		if err != nil {
			t.Fatalf("failed to claim device exclusively: %v", err)
		}
		defer holder.Close() //nolint:errcheck // Test cleanup.

		err = c.SanitizeLogicalVolume(ctx, vgName, lvName)
		if !errors.Is(err, lvm.ErrInUse) {
			t.Fatalf("expected ErrInUse for a claimed device, got %v", err)
		}

		// The data must be untouched, since a refused wipe must not be a
		// partial wipe.
		if got := readDevice(t, path, size); !bytes.Equal(got, pattern) {
			t.Fatal("volume was modified despite being in use")
		}
	})

	// Sanitizing a volume that does not exist must report an error rather than
	// silently succeeding, so a caller cannot mistake it for a completed wipe.
	//
	// The assertion is deliberately "an error" rather than ErrNotFound.
	// GetLogicalVolume documents ErrNotFound for a missing volume, but on LVM
	// builds that emit the report as JSON the "Failed to find logical volume"
	// message is written to the log array on stdout and stderr is left empty,
	// so getErrorType has nothing to match on and the raw exit status is
	// returned instead. That is a pre-existing classification gap, tracked
	// separately; what matters here is that the error is not swallowed.
	t.Run("MissingVolume", func(t *testing.T) {
		vgName := newTestVolumeGroup(t, c)

		err := c.SanitizeLogicalVolume(ctx, vgName, uniqueName("missing"))
		if err == nil {
			t.Fatal("expected an error for a missing logical volume, got nil")
		}
	})
}

// TestSanitizePreventsDataRemanence is the regression test for extent reuse.
//
// It reproduces the sequence that exposes residual data: a volume is written
// to, deleted, and a new volume of the same size is created in the same volume
// group so that it is allocated the same extents. Without sanitization the new
// volume returns the previous volume's contents from offset 4096 onward,
// because lvcreate only clears the first 4 KiB.
//
// The new volume is read as a raw block device, which is how a volumeMode:
// Block consumer sees it. No filesystem is created, so nothing else can mask
// the residual data.
func TestSanitizePreventsDataRemanence(t *testing.T) {
	if !isRoot() {
		t.Skip("Skipping TestSanitizePreventsDataRemanence as it requires root permissions.")
	}
	if _, err := os.Stat("/sbin/lvm"); err != nil {
		t.Skip("Skipping TestSanitizePreventsDataRemanence as /sbin/lvm is not available.")
	}

	c := lvm.NewClient(lvm.WithBlockDeviceUtilities(block.New()))
	ctx := context.Background()

	vgName := newTestVolumeGroup(t, c)

	// First tenant: create a volume and write identifiable data to it.
	firstLV := uniqueName("first")
	size, err := c.CreateLogicalVolume(ctx, lvm.CreateLVOptions{
		Name:   firstLV,
		VGName: vgName,
		Size:   fmt.Sprintf("%db", lvSize),
	})
	if err != nil {
		t.Fatalf("failed to create first LV: %v", err)
	}

	firstPath := lvm.ConstructLogicalVolumePath(vgName, firstLV)
	pattern := writePattern(t, firstPath, size)

	if got := readDevice(t, firstPath, size); !bytes.Equal(got, pattern) {
		t.Fatal("pattern was not written to the first volume; test cannot detect remanence")
	}

	// Deletion sanitizes before removing, which is the behaviour under test.
	if err := c.SanitizeLogicalVolume(ctx, vgName, firstLV); err != nil {
		t.Fatalf("SanitizeLogicalVolume returned error: %v", err)
	}
	if err := c.RemoveLogicalVolume(ctx, lvm.RemoveLVOptions{Name: vgName + "/" + firstLV}); err != nil {
		t.Fatalf("failed to remove first LV: %v", err)
	}

	// Second tenant: same volume group, same size, so the allocator hands back
	// the extents the first volume was using.
	secondLV := uniqueName("second")
	secondSize, err := c.CreateLogicalVolume(ctx, lvm.CreateLVOptions{
		Name:   secondLV,
		VGName: vgName,
		Size:   fmt.Sprintf("%db", lvSize),
	})
	if err != nil {
		t.Fatalf("failed to create second LV: %v", err)
	}
	defer func() {
		_ = c.RemoveLogicalVolume(ctx, lvm.RemoveLVOptions{Name: vgName + "/" + secondLV})
	}()

	secondPath := lvm.ConstructLogicalVolumePath(vgName, secondLV)
	got := readDevice(t, secondPath, secondSize)

	// The specific failure being guarded against: the previous tenant's data
	// readable from offset 4096, past the 4 KiB that lvcreate zeroes.
	if bytes.Contains(got, pattern[:4096]) {
		t.Fatal("previous volume's data is readable from the newly created volume")
	}
	if idx := firstNonZero(got); idx != -1 {
		t.Fatalf("newly created volume is not zeroed at offset %d", idx)
	}
}
