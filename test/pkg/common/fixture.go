// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package common

import "path/filepath"

var (
	basePath        = filepath.Join("test", "pkg", "common", "fixtures")
	FakeCertFixture = filepath.Join(basePath, "fake_cert.yaml")

	LvmStorageClassFixture          = filepath.Join(basePath, "lvm_storageclass.yaml")
	LvmStatefulSetFixture           = filepath.Join(basePath, "lvm_statefulset.yaml")
	LvmChaosStatefulSetFixture      = filepath.Join(basePath, "lvm_chaos_statefulset.yaml")
	LvmAnnotationStatefulSetFixture = filepath.Join(basePath, "lvm_annotation_statefulset.yaml")
	LvmPvcNoAnnotationFixture       = filepath.Join(basePath, "lvm_pvc_no_annotation.yaml")
	LvmPvcAnnotationFixure          = filepath.Join(basePath, "lvm_pvc_annotation.yaml")
	LvmPodAnnotationFixture         = filepath.Join(basePath, "lvm_pod_annotation.yaml")
)
