#!/bin/sh
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# Wrapper script to call mkfs.xfs inside /host using chroot, forwarding all arguments
# Avoids incompatibilities with the host environment
if [ ! -d /host ]; then
    echo "Error: /host directory does not exist."
    exit 1
fi

# Pass all arguments to mkfs.xfs, preserving spaces and special characters
chroot /host mkfs.xfs "$@"
exit_code=$?

# Return the exit code from mkfs.xfs
exit $exit_code
