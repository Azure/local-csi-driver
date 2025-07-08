// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sanity

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/kubernetes-csi/csi-test/v5/pkg/sanity"
)

const (
	// volumeGroupName is the name of the volume group used in the sanity tests.
	volumeGroupName = "containerstorage"
)

// Verify VGIDGenerator implements IDGenerator interface.
var _ sanity.IDGenerator = &LvmIDGenerator{}

// LvmIDGenerator implements IDGenerator for LVM volumes
// It generates volume IDs in the format "vgName#volumeName".
type LvmIDGenerator struct {
	sanity.DefaultIDGenerator
}

// NewLVMGenerator creates a new VGIDGenerator with the specified volume group name.
func NewLVMGenerator() (*LvmIDGenerator, error) {

	return &LvmIDGenerator{}, nil
}

// GenerateUniqueValidVolumeID generates a unique volume ID in the format "vgName#volumeName".
func (g LvmIDGenerator) GenerateUniqueValidVolumeID() string {
	return fmt.Sprintf("%s#%s", volumeGroupName, uuid.New().String()[:10])
}
