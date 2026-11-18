// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"golang.org/x/tools/go/analysis/multichecker"

	"local-csi-driver/internal/analysis/driverlint"
	"local-csi-driver/internal/analysis/lazyinformer"
)

func main() {
	multichecker.Main(driverlint.LVMSize, driverlint.CSIStatus, lazyinformer.Analyzer)
}
