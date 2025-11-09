//go:build linux && cgo

// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package device

import (
	"os/user"
	"strings"
	"testing"
)

func TestCreate(t *testing.T) {
	tests := []struct {
		name       string
		wantPrefix string
		wantErr    bool
	}{
		{
			name:       "create loopback device",
			wantPrefix: "/dev/loop",
		},
	}
	for _, tt := range tests {
		if u, err := user.Current(); err != nil || u.Uid != "0" {
			t.Skip("skipping test; must be root to create loopback device")
		}
		t.Run(tt.name, func(t *testing.T) {
			got, cleanup, err := CreateLoopDev()
			if (err != nil) != tt.wantErr {
				t.Errorf("Create() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			defer cleanup()

			if !strings.HasPrefix(got, tt.wantPrefix) {
				t.Errorf("Create() got = %v, want prefix %v", got, tt.wantPrefix)
			}
		})
	}
}
