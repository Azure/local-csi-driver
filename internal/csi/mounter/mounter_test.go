// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package mounter

import (
	"os"
	"testing"

	"local-csi-driver/internal/csi/testutil"
	"local-csi-driver/internal/pkg/telemetry"
)

func MakeFile(t *testing.T) {
	testTarget, err := testutil.GetWorkDirPath("test")
	mounter := New(telemetry.NewNoopTracerProvider())
	if err != nil {
		t.Errorf("Failed to get work dir path: %v", err)
		os.Exit(1)
	}

	existingFile, err := testutil.GetWorkDirPath("existingFile")
	if err != nil {
		t.Errorf("Failed to get work dir path: %v", err)
		os.Exit(1)
	}
	file, err := os.Create(existingFile)
	if err != nil {
		t.Errorf("Failed to create file: %v", err)
		os.Exit(1)
	}
	_ = file.Close()

	tests := []struct {
		desc         string
		targetPath   string
		expectedErr  bool
		expectedFile bool
	}{
		{
			desc:         "Valid target path",
			targetPath:   testTarget,
			expectedErr:  false,
			expectedFile: true,
		},
		{
			desc:         "existing file should be removed",
			targetPath:   existingFile,
			expectedErr:  false,
			expectedFile: true,
		},
		{
			desc:         "Invalid target path",
			targetPath:   testTarget + "/testFile", // testTarget is a file
			expectedErr:  true,
			expectedFile: false,
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			err := mounter.MakeFile(test.targetPath)
			if err != nil && !test.expectedErr {
				t.Errorf("MakeFile error = %v but error was not expected", err)
				return
			}

			fileExists := false
			if _, err := os.Lstat(test.targetPath); err == nil {
				fileExists = true
			}

			if fileExists != test.expectedFile {
				t.Errorf("MakeFiles result:\n fileExists = %v\n expectedFile = %v", fileExists, test.expectedFile)
			}
		})
	}

	// testTarget will be removed since it is created by CreateFile on success
	_ = os.RemoveAll(testTarget)
	// existingFile is created for the test and will be removed
	_ = os.RemoveAll(existingFile)
}
