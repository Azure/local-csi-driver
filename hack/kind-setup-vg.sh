#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
#
# Pre-creates an LVM volume group on each Kind node so the local-csi-driver
# can use it without real NVMe devices.
#
# The driver's CreateVolume path calls GetVolumeGroup() first and short-circuits
# device discovery if the VG already exists, so this is enough to make the
# e2e / external-e2e suites runnable on Kind.
#
# Each node gets a sparse file (default 500G) attached to a loop device, with a
# physical volume on top and the expected VG (default "containerstorage")
# created with the "local-csi" tag. Because the backing file is sparse, only
# data actually written to logical volumes consumes real host disk space.
#
# Usage:
#   ./hack/kind-setup-vg.sh                       # cluster "kind", VG "containerstorage", 500G
#   KIND_CLUSTER=mycluster VG_SIZE=8G ./hack/kind-setup-vg.sh
#   ./hack/kind-setup-vg.sh --teardown            # remove VG + loop + backing file
#
# Requires: docker, kind (or $KIND set to a kind binary path).

set -euo pipefail

CLUSTER="${KIND_CLUSTER:-kind}"
VG_NAME="${VG_NAME:-containerstorage}"
VG_TAG="${VG_TAG:-local-csi}"
VG_SIZE="${VG_SIZE:-500G}"
IMG_PATH_TEMPLATE="${IMG_PATH:-/csi-local-vg/__HOSTNAME__.img}"
TMPFS_DIR="${TMPFS_DIR:-/csi-local-vg}"
KIND_BIN="${KIND:-kind}"

ACTION="setup"
if [[ "${1:-}" == "--teardown" || "${1:-}" == "-d" ]]; then
    ACTION="teardown"
fi

nodes=$("${KIND_BIN}" get nodes --name "${CLUSTER}")
if [[ -z "${nodes}" ]]; then
    echo "No kind nodes found for cluster '${CLUSTER}'" >&2
    exit 1
fi

# Verify every node container is actually running before touching it. A stopped
# kind cluster (e.g. after a host reboot) returns node names from `kind get
# nodes` but `docker exec` will fail with "container is not running".
not_running=()
for node in ${nodes}; do
    state=$(docker inspect -f '{{.State.Running}}' "${node}" 2>/dev/null || echo "missing")
    if [[ "${state}" != "true" ]]; then
        not_running+=("${node} (${state})")
    fi
done
if (( ${#not_running[@]} > 0 )); then
    echo "The following kind node containers are not running:" >&2
    printf '  - %s\n' "${not_running[@]}" >&2
    echo >&2
    echo "Start them with:    docker start ${nodes//$'\n'/ }" >&2
    echo "Or recreate with:   make clean && make kind-e2e-bootstrap" >&2
    exit 1
fi

setup_node() {
    local node="$1"
    echo ">>> [${node}] ensuring VG ${VG_NAME} (${VG_SIZE})"
    docker exec -i \
        -e VG_NAME="${VG_NAME}" \
        -e VG_TAG="${VG_TAG}" \
        -e VG_SIZE="${VG_SIZE}" \
        -e IMG_PATH_TEMPLATE="${IMG_PATH_TEMPLATE}" \
        -e TMPFS_DIR="${TMPFS_DIR}" \
        "${node}" bash -euo pipefail <<'INNER'
if ! command -v vgcreate >/dev/null 2>&1; then
    echo "installing lvm2..."
    apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq lvm2 >/dev/null
fi

# A kind node's /dev is a small tmpfs, populated at boot by systemd via
# `kmod static-nodes` (see /lib/modules/$(uname -r)/modules.devname). The
# loop driver statically declares only loop0..loop7 there, so that's all
# the device nodes the container starts with. The kernel's loop pool is
# not capped at 8 - `losetup -f` will happily allocate loop8, loop9, ...
# but it returns ENOENT unless the device node exists. Once our VG backing
# file and any other loop users have consumed those 8 slots, kubelet's
# MapBlockVolume (which losetup's the published file for each block-mode
# PVC) fails with "No such file or directory". Pre-create extra device
# nodes so the kernel can hand them out on demand.
# See: https://github.com/kubernetes-sigs/kind/issues/1452
#      man 4 loop (max_loop / dynamic allocation)
for i in $(seq 8 31); do
    if [ ! -e "/dev/loop${i}" ]; then
        mknod "/dev/loop${i}" b 7 "${i}" 2>/dev/null || true
        chmod 660 "/dev/loop${i}" 2>/dev/null || true
    fi
done

# Loop devices are kernel-global across all kind containers on the same host
# kernel. The backing file path MUST be unique per node so `losetup -j` on one
# node does not match (and accidentally detach) another node's loop device.
IMG_PATH="${IMG_PATH_TEMPLATE//__HOSTNAME__/$(hostname)}"

# Loop devices backed by files on overlayfs do NOT round-trip writes through
# the file. Use a tmpfs mount so the backing file lives on a real fs. A sparse
# file in tmpfs consumes no RAM until LVs actually write data.
mkdir -p "${TMPFS_DIR}"
if ! mountpoint -q "${TMPFS_DIR}"; then
    mount -t tmpfs -o "size=${VG_SIZE}" tmpfs "${TMPFS_DIR}"
fi

if [ ! -f "${IMG_PATH}" ]; then
    if vgs --noheadings -o vg_name 2>/dev/null | tr -d ' ' | grep -qx "${VG_NAME}"; then
        vgchange -an "${VG_NAME}" 2>/dev/null || true
        vgremove -ff -y "${VG_NAME}" 2>/dev/null || true
    fi
    for L in $(losetup -j "${IMG_PATH}" 2>/dev/null | awk -F: '{print $1}'); do
        losetup -d "${L}" 2>/dev/null || true
    done
    truncate -s "${VG_SIZE}" "${IMG_PATH}"
fi

if vgs --noheadings -o vg_name 2>/dev/null | tr -d ' ' | grep -qx "${VG_NAME}"; then
    echo "VG ${VG_NAME} already exists, skipping."
    vgs "${VG_NAME}"
    exit 0
fi

LOOP=$(losetup -j "${IMG_PATH}" | awk -F: 'NR==1{print $1}')
if [ -z "${LOOP}" ]; then
    LOOP=$(losetup -f --show "${IMG_PATH}")
fi
echo "using loop device ${LOOP}"

if ! pvs --noheadings -o pv_name "${LOOP}" >/dev/null 2>&1; then
    pvcreate -ff -y "${LOOP}"
fi

vgcreate --addtag "${VG_TAG}" "${VG_NAME}" "${LOOP}"
vgs "${VG_NAME}"
INNER
}

teardown_node() {
    local node="$1"
    echo ">>> [${node}] removing VG ${VG_NAME}"
    docker exec -i \
        -e VG_NAME="${VG_NAME}" \
        -e IMG_PATH_TEMPLATE="${IMG_PATH_TEMPLATE}" \
        -e TMPFS_DIR="${TMPFS_DIR}" \
        "${node}" bash -euo pipefail <<'INNER' || true
IMG_PATH="${IMG_PATH_TEMPLATE//__HOSTNAME__/$(hostname)}"
if vgs --noheadings -o vg_name 2>/dev/null | tr -d ' ' | grep -qx "${VG_NAME}"; then
    vgchange -an "${VG_NAME}" || true
    vgremove -ff -y "${VG_NAME}" || true
fi
LOOP=$(losetup -j "${IMG_PATH}" 2>/dev/null | awk -F: 'NR==1{print $1}')
if [ -n "${LOOP}" ]; then
    pvremove -ff -y "${LOOP}" 2>/dev/null || true
    losetup -d "${LOOP}" || true
fi
rm -f "${IMG_PATH}"
if mountpoint -q "${TMPFS_DIR}"; then
    umount "${TMPFS_DIR}" || true
fi
INNER
}

for node in ${nodes}; do
    if [[ "${ACTION}" == "teardown" ]]; then
        teardown_node "${node}"
    else
        setup_node "${node}"
    fi
done

echo "Done."
