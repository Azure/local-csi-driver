// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package block

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"syscall"

	utilexec "k8s.io/utils/exec"
)

const (
	// lsblkCommand is the command to list block devices.
	lsblkCommand = "lsblk"
	blkidCmd     = "blkid"
)

// Interface defines the methods that block should implement
//
//go:generate mockgen -copyright_file ../../../hack/mockgen_copyright.txt -destination=mock_block.go -mock_names=Interface=Mock -package=block -source=block.go Interface
type Interface interface {
	GetDevices(ctx context.Context) (*DeviceList, error)
	IsBlockDevice(path string) (bool, error)
	IsBlkDevUnformatted(device string) (bool, error)
}

// block implements the Interface.
type block struct {
	exec utilexec.Interface
}

var _ Interface = &block{}

// New returns a new block instance.
func New() Interface {
	return &block{
		exec: utilexec.New(),
	}
}

// GetDevices runs the lsblk --json command and parses the output.
func (l *block) GetDevices(ctx context.Context) (*DeviceList, error) {
	_, err := l.exec.LookPath(lsblkCommand)
	if err != nil {
		return nil, fmt.Errorf("unable to find %s in PATH: %w", lsblkCommand, err)
	}

	cmd := l.exec.CommandContext(ctx, lsblkCommand, "--bytes", "--json", "--output-all")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("command failed: %w, output: %s", err, string(output))
	}

	return parseLsblkOutput(output)
}

// IsBlockDevice reports whether the given path is a block device.
func (s *block) IsBlockDevice(path string) (bool, error) {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("failed to stat path %s: %w", path, err)
	}

	stat, ok := fileInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("failed to get raw syscall.Stat_t data for %s", path)
	}

	// Check if the file mode is a block device
	if (stat.Mode & syscall.S_IFMT) == syscall.S_IFBLK {
		return true, nil
	}

	return false, nil
}

// IsBlkDevUnformatted reports whether the given block device is unformatted.
// blockDev is the path to the block device e.g. /dev/nvme0n1.
// Uses system utility blkid to get information about the device.
// blkid exit status is:
//   - 0, the device is present and responds to information i.e. it has a filesystem.
//   - 2, device Not present or does not have a filesystem.
//   - 4, usage or other errors.
//   - 8, ambivalent probing result was detected by low-level probing mode (-p).
//
// The function is mainly centered around the exit status as 2 of blkid.
// returns true if the device is unformatted, false otherwise.
func (s *block) IsBlkDevUnformatted(blockDev string) (bool, error) {
	devPresent, err := s.IsBlockDevice(blockDev)
	if err != nil {
		return false, err
	}
	if devPresent {
		// Run the "blkid" command to get information about the device
		args := []string{"-p", blockDev}
		_, err := s.exec.Command(blkidCmd, args...).CombinedOutput()
		if err != nil {
			if exit, ok := err.(utilexec.ExitError); ok {
				if exit.ExitStatus() == 2 {
					// Disk device is unformatted.
					// For `blkid`, if the specified token (TYPE/PTTYPE, etc) was
					// not found, or no (specified) devices could be identified, an
					// exit code of 2 is returned.
					return true, nil
				}
			}
			return false, fmt.Errorf("could not discover filesystem format for device path (%s): %w", blockDev, err)
		}
	}
	return false, nil
}

// parseLsblkOutput parses the JSON output of the lsblk command.
func parseLsblkOutput(output []byte) (*DeviceList, error) {
	var lsblkOutput DeviceList
	err := json.Unmarshal(output, &lsblkOutput)
	if err != nil {
		return nil, fmt.Errorf("failed to parse lsblk output: %w", err)
	}
	return &lsblkOutput, nil
}
