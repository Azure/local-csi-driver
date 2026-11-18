// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package node

import (
	"context"
	"errors"
	"fmt"

	"github.com/container-storage-interface/spec/lib/go/csi"
)

type server struct{}

func backend(context.Context) (int64, error) { return 0, nil }

func fromVolumeError(error) error { return nil }

func (*server) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	defer cleanup()
	size, err := backend(ctx)
	switch size {
	case 0:
		return nil, err // want "translate backend error"
	case 1:
		return nil, fmt.Errorf("expand: %w", err) // want "translate backend error"
	case 2:
		return nil, errors.New("bad size") // want "translate backend error"
	case 3:
		return nil, fromVolumeError(err)
	case 4:
		return forward(ctx, req)
	default:
		return &csi.NodeExpandVolumeResponse{}, nil
	}
}

func cleanup() {}

func forward(context.Context, *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	return nil, errors.New("not an RPC method")
}
