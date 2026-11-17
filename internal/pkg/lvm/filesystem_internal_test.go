// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
	utilexec "k8s.io/utils/exec"
)

func TestResizeFilesystem(t *testing.T) {
	tests := []struct {
		name        string
		format      string
		exitCode    string
		canceled    bool
		deviceMode  uint32
		deviceError error
		mountError  error
		wrongDevice bool
		volumePath  string
		wantCommand string
		wantError   bool
	}{
		{name: "ext4 grows device", format: "ext4", wantCommand: "resize2fs\n/dev/vg/lv\n"},
		{name: "xfs grows mounted filesystem", format: "xfs", wantCommand: "xfs_growfs\n-d\n/staging/lv\n"},
		{name: "xfs bind mount uses backing device identity", format: "xfs", volumePath: "/published/bind", wantCommand: "xfs_growfs\n-d\n/published/bind\n"},
		{name: "unidentified mounted filesystem is not success", wantError: true},
		{name: "filesystem helper fails", format: "ext4", exitCode: "1", wantCommand: "resize2fs\n/dev/vg/lv\n", wantError: true},
		{name: "unsupported filesystem", format: "unknown", wantError: true},
		{name: "ext2 remains unsupported", format: "ext2", wantError: true},
		{name: "canceled request never invokes resize", format: "ext4", canceled: true, wantError: true},
		{name: "missing device never invokes resize", format: "xfs", deviceError: os.ErrNotExist, wantError: true},
		{name: "nonblock device never invokes resize", format: "xfs", deviceMode: unix.S_IFREG, wantError: true},
		{name: "missing mount never invokes resize", format: "xfs", mountError: os.ErrNotExist, wantError: true},
		{name: "mount probe failure never invokes resize", format: "xfs", mountError: os.ErrPermission, wantError: true},
		{name: "different xfs mount never invokes resize", format: "xfs", wrongDevice: true, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "resize.log")
			t.Setenv("PATH", dir)
			t.Setenv("RESIZE_LOG", logPath)
			t.Setenv("TEST_FS", tt.format)
			t.Setenv("TEST_EXIT", tt.exitCode)
			for name, script := range map[string]string{
				"blkid":      "[ -n \"$TEST_FS\" ] || exit 2\nprintf 'TYPE=%s\\n' \"$TEST_FS\"\n",
				"resize2fs":  "printf '%s\\n' resize2fs \"$@\" > \"$RESIZE_LOG\"\nexit \"${TEST_EXIT:-0}\"\n",
				"xfs_growfs": "printf '%s\\n' xfs_growfs \"$@\" > \"$RESIZE_LOG\"\nexit \"${TEST_EXIT:-0}\"\n",
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			stat := func(path string, result *unix.Stat_t) error {
				if path == "/dev/vg/lv" {
					result.Rdev = 253
					result.Mode = unix.S_IFBLK
					if tt.deviceMode != 0 {
						result.Mode = tt.deviceMode
					}
					return tt.deviceError
				}
				result.Dev = 253
				if tt.wrongDevice {
					result.Dev++
				}
				return tt.mountError
			}
			volumePath := tt.volumePath
			if volumePath == "" {
				volumePath = "/staging/lv"
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tt.canceled {
				cancel()
			}
			err := resizeFilesystem("/dev/vg/lv", volumePath, contextExec{Interface: utilexec.New(), ctx: ctx}, stat)
			if (err != nil) != tt.wantError {
				t.Fatalf("resizeFilesystem() error = %v, wantError %v", err, tt.wantError)
			}
			output, readErr := os.ReadFile(logPath)
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				t.Fatal(readErr)
			}
			if string(output) != tt.wantCommand {
				t.Errorf("resize command = %q, want %q", output, tt.wantCommand)
			}
		})
	}
}
