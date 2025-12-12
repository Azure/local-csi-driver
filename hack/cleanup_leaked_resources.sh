#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.


# WARNING: This script deletes ALL Logical Volumes, Physical Volumes, and Volume Groups on ALL nodes.
# Use with extreme caution - this operation cannot be undone and may cause data loss.

set -e

echo "⚠️  WARNING: This script will delete ALL LVM resources (LVs, PVs, VGs) on ALL nodes ⚠️"
echo "This is a destructive operation that cannot be undone and may result in data loss."
read -rp "Are you absolutely sure you want to continue? [n/Y]: " confirmation
if [ "$confirmation" != "Y" ]; then
    echo "Operation cancelled."
    exit 1
fi

# Get all nodes
NODES=$(kubectl get nodes -o jsonpath='{.items[*].metadata.name}')

for NODE in $NODES; do
    echo "Creating debug pod on node: $NODE"
    IMAGE="mcr.microsoft.com/azurelinux/base/core:3.0"
    POD="lvm-cleanup-$(LC_CTYPE=C tr -dc a-z0-9 < /dev/urandom | head -c 6)"
    NAMESPACE="default"

    # Check the node
    if ! kubectl get node "$NODE" >/dev/null; then
        echo "Node $NODE not found, skipping..."
        continue
    fi

    OVERRIDES="$(cat <<EOT
{
  "spec": {
    "nodeName": "$NODE",
    "hostPID": true,
    "containers": [
      {
        "securityContext": {
          "privileged": true
        },
        "image": "$IMAGE",
        "name": "nsenter",
        "stdin": true,
        "stdinOnce": true,
        "tty": true,
        "command": [ "sh", "-c", "tdnf install util-linux -y --releasever 3.0 &> /dev/null && nsenter --target 1 --mount --uts --ipc --net --pid -- sh -c 'echo \"Removing all LVM resources on $NODE...\" && set -x && lvs && vgs && pvs && lvremove -f \$(lvs -o lv_path --noheadings | grep -v \"^$\") || true && vgremove -f \$(vgs --noheadings -o vg_name | grep -v \"^$\") || true && pvremove -f \$(pvs --noheadings -o pv_name | grep -v \"^$\") || true && echo \"Cleanup complete\"'" ]
      }
    ],
    "restartPolicy": "Never"
  }
}
EOT
)"

    echo "📋 Creating cleanup pod on node $NODE..."
    kubectl run --namespace "$NAMESPACE" --rm --image "$IMAGE" --overrides="$OVERRIDES" -i "$POD"
    echo "✅ Cleanup completed on node $NODE"
    echo "----------------------------------------"
done

echo "🏁 LVM cleanup operation completed on all nodes"
