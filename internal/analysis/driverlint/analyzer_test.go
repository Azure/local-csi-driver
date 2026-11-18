// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package driverlint

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

func TestLVMSize(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), LVMSize, "local-csi-driver/internal/analysis/sizes")
}

func TestCSIStatus(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), CSIStatus, "local-csi-driver/internal/csi/node")
}
