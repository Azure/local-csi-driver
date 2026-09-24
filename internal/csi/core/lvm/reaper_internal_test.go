// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveStaleLogicalVolumeDeviceNodes(t *testing.T) {
	t.Parallel()

	devRoot := t.TempDir()
	vgName := "test-vg"
	lvName := "local-csi-wipe-11111111-1111-4111-8111-111111111111"
	vgDir := filepath.Join(devRoot, vgName)
	mapperDir := filepath.Join(devRoot, "mapper")
	if err := os.MkdirAll(vgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(mapperDir, 0o755); err != nil {
		t.Fatal(err)
	}

	lvPath := filepath.Join(vgDir, lvName)
	mapperPath := filepath.Join(mapperDir,
		"test--vg-local--csi--wipe--11111111--1111--4111--8111--111111111111")
	if err := os.WriteFile(lvPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mapperPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := removeStaleLogicalVolumeDeviceNodes(devRoot, vgName, lvName); err != nil {
		t.Fatalf("removeStaleLogicalVolumeDeviceNodes() error = %v", err)
	}
	for _, path := range []string{lvPath, mapperPath} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("stale device node %s still exists, err = %v", path, err)
		}
	}
}

func TestRemoveStaleLogicalVolumeDeviceNodesRejectsTraversal(t *testing.T) {
	t.Parallel()

	err := removeStaleLogicalVolumeDeviceNodes(t.TempDir(), "../escape", "volume")
	if err == nil {
		t.Fatal("expected traversal to be rejected")
	}
}
