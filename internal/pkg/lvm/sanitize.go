// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"unsafe"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sys/unix"
)

const (
	// zeroChunkSize bounds the range covered by a single BLKZEROOUT ioctl or
	// write call.
	//
	// Both are uninterruptible once issued, so the chunk size determines how
	// long cancellation can take to be observed. It also bounds how long a
	// single operation occupies the device queue ahead of foreground I/O.
	zeroChunkSize = int64(1) << 30 // 1 GiB.

	// zeroBufferSize is the size of the reusable zero-filled buffer used by
	// the write fallback.
	zeroBufferSize = int64(4) << 20 // 4 MiB.
)

// errZeroOutUnsupported signals that the device cannot service BLKZEROOUT and
// that the caller should fall back to writing zeroes explicitly.
//
// It is internal: callers of SanitizeLogicalVolume never see it, because the
// fallback always succeeds where the ioctl is merely unsupported.
var errZeroOutUnsupported = errors.New("BLKZEROOUT not supported")

// SanitizeLogicalVolume overwrites every byte of a logical volume with zeroes
// and flushes the device before returning.
//
// It is the sanitization primitive behind wipe-on-delete: extents must be
// zeroed while they are still attached to a logical volume, because once
// lvremove returns them to the volume group's free pool they may be handed to
// another volume at any time.
//
// The device is held open O_EXCL for the whole operation, so a volume that is
// still mounted or otherwise claimed returns ErrInUse and is left untouched
// rather than being zeroed underneath its consumer.
//
// Zeroing is attempted with the BLKZEROOUT ioctl, which lets the device
// satisfy the request with a write-zeroes command where it supports one. Where
// the ioctl is unsupported the whole remaining range is written explicitly.
// Discard is deliberately never used as a fallback: BLKDISCARD permits, but
// does not require, a device to return zeroes for discarded blocks, so a
// discard that reports success can leave the previous contents readable.
//
// The volume's actual size is read from LVM rather than taken from the caller,
// because LVM rounds allocations up to whole extents and the rounding
// remainder is part of what has to be cleared.
func (c *Client) SanitizeLogicalVolume(ctx context.Context, vgName, lvName string) error {
	ctx, span := c.tracer.Start(ctx, "lvm/SanitizeLogicalVolume", trace.WithAttributes(
		attribute.String("vg.name", vgName),
		attribute.String("lv.name", lvName),
	))
	defer span.End()

	if vgName == "" || lvName == "" {
		return fmt.Errorf("%w: volume group and logical volume names cannot be empty", ErrInvalidInput)
	}

	lv, err := c.GetLogicalVolume(ctx, vgName, lvName)
	if err != nil {
		span.RecordError(err)
		return err
	}
	if lv == nil {
		return fmt.Errorf("%w: logical volume %s/%s", ErrNotFound, vgName, lvName)
	}

	size := int64(lv.Size)
	if size <= 0 {
		return fmt.Errorf("%w: logical volume %s/%s has size %d", ErrInvalidInput, vgName, lvName, size)
	}
	span.SetAttributes(attribute.Int64("lv.size", size))

	path := ConstructLogicalVolumePath(vgName, lvName)

	// O_EXCL on a block device fails if the device is already claimed, which
	// is the in-use guard. The descriptor is held for the whole operation so
	// there is no window between the check and the writes.
	f, err := os.OpenFile(path, os.O_WRONLY|unix.O_EXCL|unix.O_CLOEXEC, 0)
	if err != nil {
		span.RecordError(err)
		if errors.Is(err, unix.EBUSY) {
			return fmt.Errorf("%w: logical volume %s/%s is claimed by another process", ErrInUse, vgName, lvName)
		}
		return fmt.Errorf("failed to open %s for sanitization: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // Close errors are surfaced by the explicit Sync below.

	if err := zeroDevice(ctx, f, size); err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to zero %s: %w", path, err)
	}

	// Flush before the caller removes the logical volume. Without this the
	// zeroes may still be in the device write cache while the extents are
	// already back in the free pool.
	if err := f.Sync(); err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to flush %s: %w", path, err)
	}

	return nil
}

// zeroDevice writes zeroes over the first size bytes of f.
//
// It prefers the BLKZEROOUT ioctl and falls back to writing zeroes explicitly
// from the offset at which the ioctl turned out to be unsupported. Splitting
// the work at that offset means a device that supports the ioctl for part of
// its range is not re-zeroed from the beginning.
func zeroDevice(ctx context.Context, f *os.File, size int64) error {
	offset, err := zeroDeviceIoctl(ctx, f, size)
	if err == nil {
		return nil
	}
	if !errors.Is(err, errZeroOutUnsupported) {
		return err
	}
	return zeroDeviceWrite(ctx, f, offset, size)
}

// zeroDeviceIoctl zeroes f using BLKZEROOUT.
//
// It returns the offset reached, so that a caller falling back to explicit
// writes can resume rather than restart. The returned error wraps
// errZeroOutUnsupported when the device does not implement the ioctl.
func zeroDeviceIoctl(ctx context.Context, f *os.File, size int64) (int64, error) {
	for offset := int64(0); offset < size; {
		if err := ctx.Err(); err != nil {
			return offset, err
		}

		length := min(zeroChunkSize, size-offset)

		// BLKZEROOUT takes a two-element array of uint64: the byte offset and
		// the byte length, both of which must be aligned to the device's
		// logical block size. Logical volumes are extent-aligned and the chunk
		// size is a power of two, so alignment holds for every chunk.
		rng := [2]uint64{uint64(offset), uint64(length)}
		_, _, errno := unix.Syscall(
			unix.SYS_IOCTL,
			f.Fd(),
			unix.BLKZEROOUT,
			uintptr(unsafe.Pointer(&rng[0])),
		)
		if errno != 0 {
			switch errno {
			case unix.EOPNOTSUPP, unix.ENOTTY, unix.EINVAL:
				// Not a block device, or a block device whose driver has no
				// write-zeroes path. Both are expected; fall back.
				return offset, fmt.Errorf("%w: %w", errZeroOutUnsupported, errno)
			default:
				return offset, fmt.Errorf("BLKZEROOUT at offset %d failed: %w", offset, errno)
			}
		}

		offset += length
	}

	return size, nil
}

// zeroDeviceWrite writes zeroes over f from offset up to size.
func zeroDeviceWrite(ctx context.Context, f *os.File, offset, size int64) error {
	if offset >= size {
		return nil
	}

	buf := make([]byte, zeroBufferSize)

	for offset < size {
		if err := ctx.Err(); err != nil {
			return err
		}

		length := min(int64(len(buf)), size-offset)

		n, err := f.WriteAt(buf[:length], offset)
		if err != nil {
			return fmt.Errorf("write at offset %d failed: %w", offset, err)
		}
		if int64(n) != length {
			return fmt.Errorf("short write at offset %d: wrote %d of %d bytes", offset, n, length)
		}

		offset += length
	}

	return nil
}
