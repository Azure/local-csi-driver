#!/bin/bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -x
set -e
set -o pipefail

RAID_NAME="local-csi"
VOLUME_GROUP="${VOLUME_GROUP:-containerstorage}"

if vgdisplay "${VOLUME_GROUP}" &> /dev/null; then
        echo "Volume group ${VOLUME_GROUP} already exists. Nothing to do."
        exit 0
fi

if ! command -v mdadm &> /dev/null; then
        echo "mdadm not found, installing..."
        if command -v tdnf &> /dev/null; then
                tdnf install -y mdadm
        elif command -v apt-get &> /dev/null; then
                apt-get update >/dev/null && apt-get install -y mdadm
        else
                echo "Error: Neither tdnf nor apt-get found. Cannot install mdadm."
                exit 1
        fi
fi

# Try to assemble any existing RAID arrays (needed after reboot)
mdadm --assemble --scan 2>/dev/null || true

# Look for existing RAID device with our name
# Check all /dev/md* devices using mdadm --detail
RAID_DEVICE=""
for md in /dev/md*; do
        if [ -b "$md" ] && mdadm --detail "$md" 2>/dev/null | grep -q "Name.*${RAID_NAME}"; then
                RAID_DEVICE="$md"
                break
        fi
done

if [ -n "${RAID_DEVICE}" ]; then
        echo "Found existing RAID device ${RAID_DEVICE} with name ${RAID_NAME}"
fi

ALL_NVME_DEVICES=$(ls /dev/nvme*n* 2>/dev/null)
UNUSED_DEVICES=()

for device in $ALL_NVME_DEVICES; do
        if mdadm --examine "$device" | grep -q 'RAID superblock'; then
                continue
        fi
        if findmnt -S "$device" -n &> /dev/null; then
                continue
        fi
        if blkid "$device" &> /dev/null; then
                continue
        fi
        UNUSED_DEVICES+=("$device")
done

if [ "${#UNUSED_DEVICES[@]}" -eq 1 ]; then
        echo "Only one unused NVMe device found: ${UNUSED_DEVICES[0]}"
        DEVICE="${UNUSED_DEVICES[0]}"
        echo "Creating LVM volume group ${VOLUME_GROUP} on ${DEVICE}"
        pvcreate "${DEVICE}"
        vgcreate --addtag "${RAID_NAME}" "${VOLUME_GROUP}" "${DEVICE}"
        echo "LVM volume group ${VOLUME_GROUP} created successfully."
        exit 0
fi

if [ -n "${RAID_DEVICE}" ]; then
        echo "RAID device ${RAID_DEVICE} already exists"
else
        if [ "${#UNUSED_DEVICES[@]}" -lt 2 ]; then
                # We shouldn't reach here because of the earlier
                # checks. The device should have been added to LVM
                # directly.
                echo "Error: Found fewer than 2 unused NVMe devices.  Cannot create a RAID0 array."
                exit 1
        fi
        echo ""
        echo "${#UNUSED_DEVICES[@]} devices will be combined into a RAID0 array:"
        for device in "${UNUSED_DEVICES[@]}"; do
                echo "  - $device"
        done
        echo ""

        RAID_DEVICE="/dev/md0"
        if ! mdadm --create "${RAID_DEVICE}" \
                --name="${RAID_NAME}" \
                --level=0 \
                --raid-devices="${#UNUSED_DEVICES[@]}" \
                "${UNUSED_DEVICES[@]}" \
                --run \
                --force; then
                echo "Error: Failed to create RAID array."
                exit 1
        fi

        echo "RAID array created successfully."
        echo "Waiting a few seconds for the device to be ready"
        sleep 5

        mkdir -p /etc/mdadm
        mdadm --detail --scan | tee -a /etc/mdadm/mdadm.conf

        # Update initramfs on Ubuntu to persist RAID config across reboots
        if command -v update-initramfs &> /dev/null; then
                update-initramfs -u
        fi
fi

if [ ! -e "${RAID_DEVICE}" ]; then
        echo "Error: RAID device ${RAID_DEVICE} not found after creation."
        exit 1
fi
if ! mdadm -D "${RAID_DEVICE}" | grep -q "Name.*${RAID_NAME}"; then
        echo "Error: RAID device ${RAID_DEVICE} was not created by us, aborting."
        exit 1
fi

if vgdisplay "${VOLUME_GROUP}" &> /dev/null; then
        echo "Volume group ${VOLUME_GROUP} already exists."
        exit 0
fi

echo "Creating LVM volume group ${VOLUME_GROUP} on ${RAID_DEVICE}"
if ! pvdisplay "${RAID_DEVICE}" &> /dev/null; then
        pvcreate "${RAID_DEVICE}"
fi
vgcreate --addtag "${RAID_NAME}" "${VOLUME_GROUP}" "${RAID_DEVICE}"
echo "LVM volume group ${VOLUME_GROUP} created successfully."
exit 0
