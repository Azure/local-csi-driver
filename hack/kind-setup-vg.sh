#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
#
# Pre-create an LVM volume group on each Kind node so local-csi-driver can
# run without real NVMe devices.
#
# Each node gets a sparse backing file under /var (kind's real-fs Docker
# volume, NOT overlayfs - loop devices on overlayfs don't round-trip
# writes), attached to a loop device with a PV/VG on top.
#
#   ./hack/kind-setup-vg.sh              # setup
#   ./hack/kind-setup-vg.sh --teardown   # tear down
#
# Env: KIND_CLUSTER (kind), VG_NAME (containerstorage), VG_TAG (local-csi),
#      VG_SIZE (500G), KIND (kind binary).

set -euo pipefail

CLUSTER="${KIND_CLUSTER:-kind}"
VG_NAME="${VG_NAME:-containerstorage}"
VG_TAG="${VG_TAG:-local-csi}"
VG_SIZE="${VG_SIZE:-500G}"
KIND_BIN="${KIND:-kind}"

action="setup"
[[ "${1:-}" == "--teardown" || "${1:-}" == "-d" ]] && action="teardown"

nodes=$("${KIND_BIN}" get nodes --name "${CLUSTER}")
[[ -n "${nodes}" ]] || { echo "no kind nodes for cluster '${CLUSTER}'" >&2; exit 1; }

run_on_node() {
    docker exec -i \
        -e NODE_NAME="$1" \
        -e VG_NAME="${VG_NAME}" \
        -e VG_TAG="${VG_TAG}" \
        -e VG_SIZE="${VG_SIZE}" \
        "$1" bash -euo pipefail
}

# shellcheck disable=SC2016  # vars expand inside container, not here
setup_script='
command -v vgcreate >/dev/null || {
    apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq lvm2 >/dev/null
}

# Kind /dev only ships loop0..loop7 device nodes; the kernel can hand out
# more but only if the nodes exist. Pre-create extras so kubelet block-mode
# losetup calls do not ENOENT. See kubernetes-sigs/kind#1248.
for i in $(seq 8 31); do
    [ -e "/dev/loop${i}" ] || { mknod "/dev/loop${i}" b 7 "${i}" && chmod 660 "/dev/loop${i}"; } 2>/dev/null || true
done

# LVM is host-kernel-global across kind nodes (same /dev). Without scoping,
# every node sees every other node`s PV/VG. Use the lvm devices file so this
# container`s lvm tools only operate on this node`s loop. Each kind node
# has its own /etc, so the devices file is per-node.
mkdir -p /etc/lvm/devices
: > /etc/lvm/devices/system.devices

img=/var/lib/csi-local-vg/${NODE_NAME}.img
mkdir -p "$(dirname "$img")"

[ -f "$img" ] || truncate -s "${VG_SIZE}" "$img"
loop=$(losetup -j "$img" | awk -F: "NR==1{print \$1}")
[ -n "$loop" ] || loop=$(losetup -f --show "$img")
echo "using loop device $loop"

# Scope lvm to just this loop. lvmdevices --adddev re-creates the file with
# only this entry; subsequent vgs/pvs/lvs ignore everything else.
lvmdevices --adddev "$loop" 2>/dev/null || true

if vgs --noheadings -o vg_name 2>/dev/null | grep -qw "${VG_NAME}"; then
    echo "VG ${VG_NAME} already exists on this node, skipping."
    vgs "${VG_NAME}"
    exit 0
fi

pvs --noheadings -o pv_name "$loop" >/dev/null 2>&1 || pvcreate -ff -y "$loop"
vgcreate --addtag "${VG_TAG}" "${VG_NAME}" "$loop"
vgs "${VG_NAME}"
'

# shellcheck disable=SC2016
teardown_script='
img=/var/lib/csi-local-vg/${NODE_NAME}.img
if vgs --noheadings -o vg_name 2>/dev/null | grep -qw "${VG_NAME}"; then
    vgchange -an "${VG_NAME}" || true
    vgremove -ff -y "${VG_NAME}" || true
fi
loop=$(losetup -j "$img" 2>/dev/null | awk -F: "NR==1{print \$1}")
[ -z "$loop" ] || { pvremove -ff -y "$loop" 2>/dev/null || true; losetup -d "$loop" || true; }
rm -f "$img"
rm -f /etc/lvm/devices/system.devices
'

for node in ${nodes}; do
    echo ">>> [${node}] ${action} VG ${VG_NAME} (${VG_SIZE})"
    if [[ "${action}" == "teardown" ]]; then
        run_on_node "${node}" <<<"${teardown_script}" || true
    else
        run_on_node "${node}" <<<"${setup_script}"
    fi
done

echo "Done."
