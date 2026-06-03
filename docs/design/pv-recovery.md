# local-csi-driver PV Recovery

## Scenario Definition

When local-csi-driver provisions a PersistentVolume (PV), the underlying LVM
logical volume exists only on the node where it was created. By default,
Kubernetes sets node affinity on the PV so that workloads can only be scheduled
on that specific node. If the node becomes unavailable (e.g., due to hardware
failure or maintenance), the PV becomes inaccessible and the workload cannot
recover without manual intervention.

PV Recovery enables workloads using local storage to automatically recover on
another node by:

1. Removing node affinity from the PV at creation time, allowing the Pod to be
   rescheduled to any node.
2. Using a webhook to enforce hyperconverged scheduling (pod-to-volume
   co-location) without relying on immutable PV node affinity.
3. Recreating an empty LVM volume on the new node during `NodeStageVolume`.
4. Cleaning up orphaned LVM volumes left behind on the original node.

## Goals

- Allow workloads to recover on a different node without manual intervention
  when the original node is lost.
- Garbage collect orphaned LVM logical volumes from nodes that no longer own the
  PV.

## Non-goals

- Data persistence across node failures. The assumption is that the application
  can re-hydrate any required data from external sources. PV Recovery recreates
  an **empty** volume on the new node.
- Support for volumes without the hyperconverged webhook. When the webhook is
  not enabled, PV node affinity is set as normal and manual cleanup is required
  for node failures (see [pv-cleanup.md](pv-cleanup.md)).

## Applicable PVCs and PVs

PV Recovery applies when **all** of the following conditions are met:

- **CSI Driver**: PV must be provisioned by `localdisk.csi.acstor.io`.
- **Hyperconverged webhook enabled**: The mutation webhook must be active
  to inject node affinity into Pods.
- **`--run-alongside-webhook=true`**: The driver flag must be set so the
  driver removes PV node affinity.
- **Non-ephemeral PVC**: PVC must use standard (non-generic-ephemeral)
  provisioning with the
  `localdisk.csi.acstor.io/accept-ephemeral-storage: "true"` annotation.
- **Volume binding mode**: StorageClass must use `WaitForFirstConsumer`.

Generic ephemeral volumes are excluded because their lifecycle is tied to the
Pod, so recovery does not apply.

## Design

### Overview

PV Recovery is implemented across three components that work together:

```text
+-----------------------+      +---------------------+      +----------------------+
| CSI ControllerServer  |      | CSI NodeServer      |      | GC Controllers       |
| (CreateVolume)        |      | (NodeStageVolume)   |      | (DaemonSet)          |
+-----------+-----------+      +----------+----------+      +-----------+----------+
            |                             |                              |
            v                             v                              v
  Remove node affinity         Annotate PV with              Clean up orphaned LVM
  from PV; store initial       selected-node; recreate       volumes on old node
  node in VolumeContext        empty LVM volume
```

### 1. Volume Creation (ControllerServer)

When `--run-alongside-webhook=true`, the `CreateVolume` handler:

- Removes accessible topology from the CSI volume response
  (`vol.AccessibleTopology = nil`), which prevents Kubernetes from setting node
  affinity on the PV.
- Stores the creating node in `VolumeContext` under the key
  `localdisk.csi.acstor.io/selected-initial-node` so the system knows where the
  volume was originally created.

### 2. Volume Staging (NodeServer)

During `NodeStageVolume`, when `removePvNodeAffinity` is enabled:

- The node reads the PV and checks the `selected-node` annotation
  (`localdisk.csi.acstor.io/selected-node`) and the `selected-initial-node`
  volume attribute.
- If the current node differs from the recorded node, it patches the PV
  annotation to point to the current node. This signals that recovery has
  occurred.
- The node calls `NodeEnsureVolume` which creates a new empty LVM logical
  volume with the original capacity if one does not already exist locally.
- The volume is then formatted and mounted normally.

### 3. Volume Deletion (ControllerServer)

When a PV without hostname topology receives a `DeleteVolume` request:

- All driver instances receive the request (because there is no node affinity
  restricting delivery via the external-provisioner's `--node-deployment` mode).
- Each instance checks the `selected-node` annotation (or
  `selected-initial-node` fallback) to determine if it owns the volume.
- Instances that do **not** own the volume return a gRPC `FailedPrecondition`
  error. The external-provisioner sidecar records this error as a Warning event
  on the PV and retries. The external-provisioner only removes its finalizer
  (`external-provisioner.volume.kubernetes.io/finalizer`) when it receives a
  successful (non-error) response, ensuring only the owning node's deletion
  actually takes effect.
- If the selected node no longer exists in the cluster, the instance returns
  success without deleting, deferring cleanup to the PV cleanup controller in
  the manager.
- Only the instance on the selected node performs the actual LVM deletion.

> **Note:** In large clusters with many PVs, the Warning events from
> non-owning nodes can be excessive. There is currently no way to suppress
> these events, as they are generated by the external-provisioner sidecar in
> response to the `FailedPrecondition` error.

### 4. Garbage Collection (GC Controllers)

Two complementary controllers in the `internal/gc` package clean up orphaned
LVM volumes on nodes that no longer own the PV:

#### PV Failover Reconciler (event-driven)

- Watches for PV Update events where the `selected-node` annotation changes.
- When a PV's annotation is updated to a different node, the reconciler on the
  **old** node detects the mismatch.
- It verifies the LVM logical volume exists locally, unmounts it, and deletes
  it.
- Controlled by `--enable-lv-garbage-collection` (default: `true`).

#### LVM Orphan Scanner (periodic)

- Runs at a configurable interval (default: 30 minutes).
- Scans all LVM logical volumes in managed volume groups.
- Uses a field index on `spec.csi.volumeHandle` for O(1) PV lookups.
- Deletes volumes that either have no corresponding PV or have a node
  annotation mismatch.
- Controlled by `--enable-lvm-orphan-cleanup` (default: `true`).
- Interval configured by `--lvm-orphan-cleanup-interval` (default: `30m`).

### Node Annotations

- `localdisk.csi.acstor.io/selected-initial-node` (PV VolumeAttributes):
  records the node that originally created the volume.
- `localdisk.csi.acstor.io/selected-node` (PV Annotations): records the node
  currently owning the volume (updated on recovery).

## How to Enable

PV Recovery is **enabled by default** when deploying via the Helm chart. The
chart sets `--run-alongside-webhook={{ .Values.webhook.hyperconverged.enabled }}`
on the driver DaemonSet, and `webhook.hyperconverged.enabled` defaults to
`true`.

### Default Configuration (enabled)

With the default Helm values, the following are all active:

- **Hyperconverged webhook** (`webhook.hyperconverged.enabled: true`) - injects
  node affinity into Pods based on PV annotations.
- **`--run-alongside-webhook=true`** - automatically set by the chart when the
  hyperconverged webhook is enabled. Causes the driver to remove PV node
  affinity, track ownership via annotations, and enable volume recreation
  during `NodeStageVolume`.
- **GC controllers** - enabled by default to clean up orphaned LVM volumes.

### Disabling PV Recovery

To disable PV recovery, set the hyperconverged webhook to `false` in Helm
values:

```yaml
webhook:
  hyperconverged:
    enabled: false
```

This disables both the webhook and the `--run-alongside-webhook` flag, reverting
to standard PV node affinity behavior (manual recovery required on node loss).

### GC Controller Flags

The garbage collection controllers are enabled by default. To customize:

```bash
# Disable event-driven GC (not recommended)
--enable-lv-garbage-collection=false

# Disable periodic orphan scanner (not recommended)
--enable-lvm-orphan-cleanup=false

# Change scan interval (default 30m)
--lvm-orphan-cleanup-interval=15m
```

### Full Example

```yaml
# StorageClass for recoverable local volumes
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: local-recoverable
provisioner: localdisk.csi.acstor.io
reclaimPolicy: Delete
volumeBindingMode: WaitForFirstConsumer
---
# PVC with ephemeral storage acknowledgement
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: my-data
  annotations:
    localdisk.csi.acstor.io/accept-ephemeral-storage: "true"
spec:
  accessModes:
    - ReadWriteOnce
  storageClassName: local-recoverable
  resources:
    requests:
      storage: 10Gi
```

## Pain Points

- **Data loss on recovery**: When a volume is recreated on a new node, it starts
  empty. Applications must tolerate data loss or re-hydrate from external
  sources.
- **Requires webhook**: PV Recovery does not function without the hyperconverged
  webhook, because PV node affinity would prevent rescheduling.
- **Temporary dual storage consumption**: Between recovery (new volume created)
  and GC (old volume cleaned up), the logical volume exists on two nodes
  temporarily.

## Related Documentation

- [PV Cleanup Design](pv-cleanup.md) - Handling PV deletion when a node is lost
- [Webhooks Design](webhooks.md) - Hyperconverged and validation webhooks
- [GC Controllers README](../../internal/gc/README.md) - Detailed GC
  implementation
