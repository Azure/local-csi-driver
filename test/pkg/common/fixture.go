// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package common

import "path/filepath"

var (
	basePath        = filepath.Join("test", "pkg", "common", "fixtures")
	FakeCertFixture = filepath.Join(basePath, "fake_cert.yaml")

	LvmStorageClassFixture          = filepath.Join(basePath, "lvm_storageclass.yaml")
	LvmStorageClassXfsFixture       = filepath.Join(basePath, "lvm_storageclass_xfs.yaml")
	LvmStatefulSetFixture           = filepath.Join(basePath, "lvm_statefulset.yaml")
	LvmAnnotationStatefulSetFixture = filepath.Join(basePath, "lvm_annotation_statefulset.yaml")
	LvmPvcNoAnnotationFixture       = filepath.Join(basePath, "lvm_pvc_no_annotation.yaml")
	LvmPvcAnnotationFixure          = filepath.Join(basePath, "lvm_pvc_annotation.yaml")
	LvmPvcAnnotationXfsFixture      = filepath.Join(basePath, "lvm_pvc_annotation_xfs.yaml")
	LvmPvcAnnotationBlockFixture    = filepath.Join(basePath, "lvm_pvc_annotation_block.yaml")
	LvmPodAnnotationFixture         = filepath.Join(basePath, "lvm_pod_annotation.yaml")
	LvmPodAnnotationXfsFixture      = filepath.Join(basePath, "lvm_pod_annotation_xfs.yaml")
	LvmPodAnnotationBlockFixture    = filepath.Join(basePath, "lvm_pod_annotation_block.yaml")
)
