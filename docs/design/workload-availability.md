# Workload Availability

Status: proposed

## Background

local-csi-driver was initially intended to run on cloud VMs where the local NVMe
disks are not truly persistent. Data loss can occur if the VM is migrated to
another physical node, which can happen if the VM is shutdown and restarted.

To ensure users are aware of this behaviour, the [PVC Validation
Webhook][webhooks] only allows [generic ephemeral volumes][generic pvc]
unless an annotation is set.

When nodes are removed from the cluster they may take a long time to recover or
never come back. Since the expectation is that the data is ephemeral, the
current default behaviour is to allow the application to restart on another
node, with an empty data volume as soon as possible. The [Pod Mutation
Webhook][webhooks] enables this by replacing strict node affinity with
preferred.

The downside to this approach is that the node may recover (with data intact)
after the workload has been moved and is still recovering state.

There are cases where the current behaviour is not optimal:

1. Nodes with non-ephemeral disks. Where a node will not lose data after a
   shutdown/restart it may be more efficient to wait for the node to recover.
2. Workloads with large datasets (e.g. databases) that are expensive to recover,
   either by re-seeding it as a replica or by restoring data. Users may prefer
   to wait for a limited amount of time for the node to recover before
   abandoning its data and restarting the workload on another node. If the data
   is lost, the workload should still be able to start on the original node, but
   with an empty volume.
3. AI workloads where the strict node affinity is set by the application to pin
   it to specific GPU(s). In this case, the application's strict affinity will
   override the preferred, and the application will never be moved to another
   node. The strict affinity has a side effect of blocking PV/PVC delete if the
   node does not recover. Note: this is not strictly related to this proposal.

## Goals

- Allow users to override default behaviour of prioritizing application
  availability over data availability.
- Allow users to configure how long to wait for node recovery before abandoning
  data.

## Non-goals

- Orphaned PV/PVC handling where the node does not recover and the objects can't
  be deleted due to finalizers not getting removed. This will be covered in
  a separate design. See [Persistent Volume (PV) Cleanup][pv cleanup].

## Design

### User Interface

Users should be able to configure whether to data availability over the default
application availability. If data availability is set, they should be able to
configure how long to wait for the node to recover.

They should be able to set a default for all volumes in the StorageClass, or
override per-volume by setting annotations on PVCs.

#### StorageClass Parameters

- `localdisk.csi.acstor.io/availability`: Either `application` or `data`.
- `localdisk.csi.acstor.io/node-recovery-timeout`: Duration to wait for node
  recovery in Go-parsable time format, e.g. `10m` for 10 minutes.

### PVC Annotations

Same as the StorageClass parameters. These will override the defaults or the
StorageClass parameters.

## Implementation

## Related Documentation

- [Webhooks][webhooks]
- [Persistent Volume (PV) Cleanup][pv cleanup]
- [generic ephemeral volumes][generic pvc]

[webhooks]: webhooks.md
[pv cleanup]: pv-cleanup.md
[generic pvc]: https://kubernetes.io/docs/concepts/storage/ephemeral-volumes/#generic-ephemeral-volumes
