// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package server

import (
	"errors"
	"os"
	"testing"
)

func Test_parseEndpoint(t *testing.T) {
	tests := []struct {
		ep        string
		wantProto string
		wantAddr  string
		wantErr   bool
	}{
		{"unix:///var/lib/lcd/csi.sock", "unix", "/var/lib/lcd/csi.sock", false},
		{"tcp://10.0.0.1:1000", "tcp", "10.0.0.1:1000", false},
		{"/var/lib/lcd/csi.sock", "", "", true},
		{"10.0.0.1:1000", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.ep, func(t *testing.T) {
			gotProto, gotAddr, err := parseEndpoint(tt.ep)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseEndpoint() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotProto != tt.wantProto {
				t.Errorf("parseEndpoint() gotProto = %v, want %v", gotProto, tt.wantProto)
			}
			if gotAddr != tt.wantAddr {
				t.Errorf("parseEndpoint() gotAddr = %v, want %v", gotAddr, tt.wantAddr)
			}
		})
	}
}

//nolint:errcheck
func Test_prepareEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		proto   string
		addr    string
		setup   func()
		cleanup func()
		wantErr bool
	}{
		{
			name:    "valid unix endpoint",
			proto:   "unix",
			addr:    "/tmp/csi.sock",
			setup:   func() {},
			cleanup: func() { os.Remove("/tmp/csi.sock") },
			wantErr: false,
		},
		{
			name:    "valid tcp endpoint",
			proto:   "tcp",
			addr:    "10.0.0.1:1000",
			setup:   func() {},
			cleanup: func() {},
			wantErr: false,
		},
		{
			name:    "unix endpoint with existing file",
			proto:   "unix",
			addr:    "/tmp/existing.sock",
			setup:   func() { os.Create("/tmp/existing.sock") },
			cleanup: func() { os.Remove("/tmp/existing.sock") },
			wantErr: false,
		},
		{
			name:  "unix endpoint with error removing file",
			proto: "unix",
			addr:  "/tmp/protected.sock",
			setup: func() {
				// Override the remove function to return an error
				remove = func(name string) error {
					return errors.New("mock remove error")
				}
			},
			cleanup: func() {
				// Restore the original remove function
				remove = os.Remove
			},
			wantErr: true,
		},
		{
			name:    "invalid protocol",
			proto:   "http",
			addr:    "localhost:8080",
			setup:   func() {},
			cleanup: func() {},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setup != nil {
				tt.setup()
			}
			if tt.cleanup != nil {
				defer tt.cleanup()
			}
			err := prepareEndpoint(tt.proto, tt.addr)
			if (err != nil) != tt.wantErr {
				t.Errorf("prepareEndpoint() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
