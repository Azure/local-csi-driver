// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fillPattern writes a recognisable non-zero pattern of length n to path and
// returns it, so a test can distinguish "zeroed" from "never written".
//
// A repeating byte sequence is used rather than a constant so that a partial
// or misaligned wipe shifts the pattern visibly instead of leaving a plausible
// looking result.
func fillPattern(t *testing.T, path string, n int64) []byte {
	t.Helper()

	pattern := make([]byte, n)
	for i := range pattern {
		pattern[i] = byte(i%251 + 1) // Never zero: 251 is prime, offset by 1.
	}

	if err := os.WriteFile(path, pattern, 0o600); err != nil {
		t.Fatalf("failed to write pattern file: %v", err)
	}

	return pattern
}

// openPatternFile creates a file of size n filled with a non-zero pattern and
// returns the open file plus the pattern that was written.
func openPatternFile(t *testing.T, n int64) (*os.File, []byte) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "device.img")
	pattern := fillPattern(t, path, n)

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("failed to open pattern file: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })

	return f, pattern
}

// readAll reads the whole contents of f from offset zero.
func readAll(t *testing.T, f *os.File) []byte {
	t.Helper()

	info, err := f.Stat()
	if err != nil {
		t.Fatalf("failed to stat file: %v", err)
	}

	buf := make([]byte, info.Size())
	if _, err := f.ReadAt(buf, 0); err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	return buf
}

// TestZeroDeviceIoctlUnsupported pins the condition that triggers the write
// fallback.
//
// A regular file cannot service BLKZEROOUT, so the ioctl must report
// errZeroOutUnsupported rather than a generic failure. Without this the
// fallback would be reached by accident on real devices and the difference
// between "unsupported" and "broken" would be invisible.
func TestZeroDeviceIoctlUnsupported(t *testing.T) {
	f, _ := openPatternFile(t, 8192)

	offset, err := zeroDeviceIoctl(context.Background(), f, 8192)
	if !errors.Is(err, errZeroOutUnsupported) {
		t.Fatalf("expected errZeroOutUnsupported for a regular file, got %v", err)
	}
	if offset != 0 {
		t.Fatalf("expected fallback to resume at offset 0, got %d", offset)
	}
}

// TestZeroDevice covers the sizes at which the chunking arithmetic is most
// likely to be wrong.
func TestZeroDevice(t *testing.T) {
	tests := []struct {
		name string
		size int64
	}{
		{name: "single byte", size: 1},
		{name: "smaller than buffer", size: 1024},
		{name: "exactly one buffer", size: zeroBufferSize},
		{name: "one buffer plus a partial tail", size: zeroBufferSize + 512},
		{name: "several buffers", size: zeroBufferSize * 3},
		{name: "several buffers plus a partial tail", size: zeroBufferSize*3 + 17},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _ := openPatternFile(t, tt.size)

			if err := zeroDevice(context.Background(), f, tt.size); err != nil {
				t.Fatalf("zeroDevice returned error: %v", err)
			}

			got := readAll(t, f)
			if len(got) != int(tt.size) {
				t.Fatalf("expected file to remain %d bytes, got %d", tt.size, len(got))
			}
			if idx := bytes.IndexFunc(got, func(r rune) bool { return r != 0 }); idx != -1 {
				t.Fatalf("found non-zero byte at offset %d after zeroDevice", idx)
			}
		})
	}
}

// TestZeroDeviceDoesNotOverrun verifies that zeroing stops at the requested
// size.
//
// The size passed in is the logical volume's size as reported by LVM. Writing
// past it would run into whatever follows on the underlying device, which for
// a logical volume means another tenant's extents.
func TestZeroDeviceDoesNotOverrun(t *testing.T) {
	const total = zeroBufferSize * 2
	const zeroUpTo = zeroBufferSize

	f, pattern := openPatternFile(t, total)

	if err := zeroDevice(context.Background(), f, zeroUpTo); err != nil {
		t.Fatalf("zeroDevice returned error: %v", err)
	}

	got := readAll(t, f)

	if !bytes.Equal(got[:zeroUpTo], make([]byte, zeroUpTo)) {
		t.Fatal("expected the requested range to be zeroed")
	}
	if !bytes.Equal(got[zeroUpTo:], pattern[zeroUpTo:]) {
		t.Fatal("zeroDevice wrote past the requested size")
	}
}

// TestZeroDeviceWriteResumesFromOffset verifies that the write fallback starts
// where the ioctl path stopped.
//
// A device that services BLKZEROOUT for part of its range must not be
// re-zeroed from the beginning, and more importantly the range before the
// offset must not be assumed clean by accident.
func TestZeroDeviceWriteResumesFromOffset(t *testing.T) {
	const total = zeroBufferSize * 2
	const resumeAt = zeroBufferSize

	f, pattern := openPatternFile(t, total)

	if err := zeroDeviceWrite(context.Background(), f, resumeAt, total); err != nil {
		t.Fatalf("zeroDeviceWrite returned error: %v", err)
	}

	got := readAll(t, f)

	if !bytes.Equal(got[:resumeAt], pattern[:resumeAt]) {
		t.Fatal("zeroDeviceWrite wrote before the resume offset")
	}
	if !bytes.Equal(got[resumeAt:], make([]byte, total-resumeAt)) {
		t.Fatal("expected the range from the resume offset to be zeroed")
	}
}

// TestZeroDeviceWriteNoOpAtOrPastSize covers the case where the ioctl path
// completed the whole range, so the fallback has nothing left to do.
func TestZeroDeviceWriteNoOpAtOrPastSize(t *testing.T) {
	const total = 4096

	f, pattern := openPatternFile(t, total)

	if err := zeroDeviceWrite(context.Background(), f, total, total); err != nil {
		t.Fatalf("zeroDeviceWrite returned error: %v", err)
	}

	if !bytes.Equal(readAll(t, f), pattern) {
		t.Fatal("expected a no-op when the offset is already at the size")
	}
}

// TestZeroDeviceCancelled verifies that cancellation is observed and reported.
//
// Sanitization must fail closed: a cancelled wipe has to surface an error so
// the caller leaves the volume in quarantine rather than removing it and
// releasing partially zeroed extents.
func TestZeroDeviceCancelled(t *testing.T) {
	f, pattern := openPatternFile(t, zeroBufferSize*2)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := zeroDevice(ctx, f, zeroBufferSize*2)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	// Nothing should have been written, since cancellation is checked before
	// the first chunk.
	if !bytes.Equal(readAll(t, f), pattern) {
		t.Fatal("expected no writes after cancellation before the first chunk")
	}
}

// TestSanitizeLogicalVolumeValidation covers the input validation that runs
// before any device is opened, so it needs neither root nor LVM.
func TestSanitizeLogicalVolumeValidation(t *testing.T) {
	c := NewClient()
	ctx := context.Background()

	tests := []struct {
		name   string
		vgName string
		lvName string
	}{
		{name: "empty volume group", vgName: "", lvName: "lv0"},
		{name: "empty logical volume", vgName: "vg0", lvName: ""},
		{name: "both empty", vgName: "", lvName: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := c.SanitizeLogicalVolume(ctx, tt.vgName, tt.lvName)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
		})
	}
}
