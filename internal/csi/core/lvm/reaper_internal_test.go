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
	sysBlockRoot := t.TempDir()
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

	if err := removeStaleLogicalVolumeDeviceNodes(devRoot, sysBlockRoot, vgName, lvName); err != nil {
		t.Fatalf("removeStaleLogicalVolumeDeviceNodes() error = %v", err)
	}
	for _, path := range []string{lvPath, mapperPath} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("stale device node %s still exists, err = %v", path, err)
		}
	}
}

func TestRemoveStaleLogicalVolumeDeviceNodesRefusesActiveMapping(t *testing.T) {
	t.Parallel()

	devRoot := t.TempDir()
	sysBlockRoot := t.TempDir()
	vgName := "test-vg"
	lvName := "local-csi-wipe-11111111-1111-4111-8111-111111111111"
	mapperName := "test--vg-local--csi--wipe--11111111--1111--4111--8111--111111111111"

	vgDir := filepath.Join(devRoot, vgName)
	mapperDir := filepath.Join(devRoot, "mapper")
	dmDir := filepath.Join(sysBlockRoot, "dm-7", "dm")
	for _, dir := range []string{vgDir, mapperDir, dmDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	lvPath := filepath.Join(vgDir, lvName)
	mapperPath := filepath.Join(mapperDir, mapperName)
	for _, path := range []string{lvPath, mapperPath} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dmDir, "name"), []byte(mapperName+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := removeStaleLogicalVolumeDeviceNodes(devRoot, sysBlockRoot, vgName, lvName); err == nil {
		t.Fatal("expected active device-mapper mapping to prevent cleanup")
	}
	for _, path := range []string{lvPath, mapperPath} {
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("device node %s was removed despite active mapping: %v", path, err)
		}
	}
}

func TestRemoveStaleLogicalVolumeDeviceNodesRejectsTraversal(t *testing.T) {
	t.Parallel()

	err := removeStaleLogicalVolumeDeviceNodes(t.TempDir(), t.TempDir(), "../escape", "volume")
	if err == nil {
		t.Fatal("expected traversal to be rejected")
	}
}
