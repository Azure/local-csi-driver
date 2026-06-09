// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package kwok

import "path/filepath"

var (
	basePath = filepath.Join("test", "pkg", "kwok", "fixtures")

	KwokNode         = filepath.Join(basePath, "node.yaml")
	KwokPod          = filepath.Join(basePath, "pod.yaml")
	KwokPV           = filepath.Join(basePath, "pv.yaml")
	KwokPVC          = filepath.Join(basePath, "pvc.yaml")
	KwokStorageClass = filepath.Join(basePath, "storageclass.yaml")
)
