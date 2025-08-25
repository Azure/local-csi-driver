// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package block

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"syscall"

	utilexec "k8s.io/utils/exec"
)

const (
	// lsblkCommand is the command to list block devices.
	lsblkCommand = "lsblk"
	blkidCmd     = "blkid"
	Lvm2Type     = "LVM2_member"
)

var (
	blkidTypeRe = regexp.MustCompile(`\bTYPE="([^"]+)"`)
)

// Interface defines the methods that block should implement
//
//go:generate mockgen -copyright_file ../../../hack/mockgen_copyright.txt -destination=mock_block.go -mock_names=Interface=Mock -package=block -source=block.go Interface
type Interface interface {
	GetDevices(ctx context.Context) (*DeviceList, error)
	IsBlockDevice(path string) (bool, error)
	IsFormatted(device string) (bool, error)
	IsLVM2(device string) (bool, error)
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

// IsFormatted reports whether the given block device is formatted.
func (s *block) IsFormatted(blockDev string) (bool, error) {
	_, isFormatted, err := s.blkid(blockDev)
	if err != nil {
		return false, err
	}
	return isFormatted, nil
}

// IsLVM2 reports whether the given block device is an LVM2 physical volume.
func (s *block) IsLVM2(blockDev string) (bool, error) {
	fsType, isFormatted, err := s.blkid(blockDev)
	if err != nil {
		return false, err
	}
	if !isFormatted {
		return false, nil
	}
	return fsType == Lvm2Type, nil
}

// blkid reports whether the given block device is formatted and returns its filesystem type if present.
// blockDev is the path to the block device e.g. /dev/nvme0n1.
// Uses system utility blkid to get information about the device.
// blkid exit status is:
//   - 0, the device is present and responds to information i.e. it has a filesystem.
//   - 2, device Not present or does not have a filesystem.
//   - 4, usage or other errors.
//   - 8, ambivalent probing result was detected by low-level probing mode (-p).
//
// Returns: fsType (string), formatted (bool), error.
func (s *block) blkid(blockDev string) (string, bool, error) {
	devPresent, err := s.IsBlockDevice(blockDev)
	if err != nil {
		return "", false, err
	}
	if !devPresent {
		return "", false, fmt.Errorf("device %s not present", blockDev)
	}

	args := []string{"-p", blockDev}
	out, err := s.exec.Command(blkidCmd, args...).CombinedOutput()
	if err == nil {
		// Exit code 0: device is formatted
		fsType := parseFsTypeFromBlkid(string(out))
		return fsType, true, nil
	}
	if exit, ok := err.(utilexec.ExitError); ok {
		if exit.ExitStatus() == 2 {
			// Exit code 2: device is unformatted
			return "", false, nil
		}

	}
	return "", false, fmt.Errorf("could not determine if device %s is formatted: %w", blockDev, err)
}

// parseFsTypeFromBlkid extracts TYPE="<fstype>" from blkid output.
// It ignores PTTYPE and other attributes.
func parseFsTypeFromBlkid(out string) string {
	m := blkidTypeRe.FindStringSubmatch(out)
	if len(m) == 2 {
		return m[1]
	}
	return ""
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
