// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lazyinformer

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), Analyzer, "reads")
}
