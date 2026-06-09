// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package kwok

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"sigs.k8s.io/yaml"

	"local-csi-driver/internal/csi/core/lvm"
	"local-csi-driver/test/pkg/common"
	"local-csi-driver/test/pkg/utils"
)

// Install installs Kwok into the current cluster.
func Install(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "make", "kwok")
	if _, err := utils.Run(cmd); err != nil {
		return fmt.Errorf("installing kwok: %w", err)
	}
	return nil
}

// Uninstall uninstalls Kwok from the current cluster.
func Uninstall(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "make", "kwok-uninstall")
	if _, err := utils.Run(cmd); err != nil {
		return fmt.Errorf("uninstalling kwok: %w", err)
	}
	return nil
}

func loadFixture(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, out); err != nil {
		return fmt.Errorf("unmarshalling %s: %w", path, err)
	}
	return nil
}

// NewNode returns a *corev1.Node populated from the kwok node fixture with the
// given name (also applied to the kubernetes.io/hostname label).
func NewNode(name string) (*corev1.Node, error) {
	var node corev1.Node
	if err := loadFixture(KwokNode, &node); err != nil {
		return nil, err
	}
	node.Name = name
	if node.Labels == nil {
		node.Labels = make(map[string]string)
	}
	node.Labels[corev1.LabelHostname] = name
	node.Labels[lvm.TopologyKey] = name
	return &node, nil
}

// NewPV returns a *corev1.PersistentVolume populated from the kwok pv fixture.
func NewPV(name string) (*corev1.PersistentVolume, error) {
	var pv corev1.PersistentVolume
	if err := loadFixture(KwokPV, &pv); err != nil {
		return nil, err
	}
	pv.Name = name
	return &pv, nil
}

// NewPVC returns a *corev1.PersistentVolumeClaim populated from the kwok pvc
// fixture.
func NewPVC(name, ns string) (*corev1.PersistentVolumeClaim, error) {
	var pvc corev1.PersistentVolumeClaim
	if err := loadFixture(KwokPVC, &pvc); err != nil {
		return nil, err
	}
	pvc.Name = name
	pvc.Namespace = ns
	return &pvc, nil
}

// NewPod returns a *corev1.Pod populated from the kwok pod fixture.
func NewPod(name, ns string) (*corev1.Pod, error) {
	var pod corev1.Pod
	if err := loadFixture(KwokPod, &pod); err != nil {
		return nil, err
	}
	pod.Name = name
	pod.Namespace = ns
	return &pod, nil
}

// NewStorageClass returns the shared LVM storageclass fixture used by e2e and
// scale tests.
func NewStorageClass() (*storagev1.StorageClass, error) {
	var sc storagev1.StorageClass
	if err := loadFixture(common.LvmStorageClassFixture, &sc); err != nil {
		return nil, err
	}
	return &sc, nil
}

// NewNoopStorageClass returns a no-op StorageClass referenced by the kwok PVC
// fixture. The SC uses kubernetes.io/no-provisioner with
// volumeBindingMode=WaitForFirstConsumer so kube-controller-manager's
// pv_controller does not flood logs with "storageclass not found" or attempt
// to provision the kwok fake PVCs.
func NewNoopStorageClass() (*storagev1.StorageClass, error) {
	var sc storagev1.StorageClass
	if err := loadFixture(KwokStorageClass, &sc); err != nil {
		return nil, err
	}
	return &sc, nil
}

// NewWarmupStatefulSet returns an *appsv1.StatefulSet built from the shared
// LVM statefulset fixture, scaled down to a single replica, so the local-csi
// driver's informers and gRPC paths are exercised before kwok scale load.
func NewWarmupStatefulSet(ns string) (*appsv1.StatefulSet, error) {
	var ss appsv1.StatefulSet
	if err := loadFixture(common.LvmStatefulSetFixture, &ss); err != nil {
		return nil, err
	}
	ss.Namespace = ns
	one := int32(10)
	ss.Spec.Replicas = &one
	return &ss, nil
}
