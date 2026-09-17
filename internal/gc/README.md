# Garbage Collection Controllers

The driver runs two node-local garbage collection mechanisms that remove LVM
logical volumes which are no longer owned by the current node.

These controllers are destructive. They operate only on driver-formatted
volume IDs, but the periodic scanner examines logical volumes in driver-managed
volume groups. Do not place unrelated logical volumes in those volume groups.

## Controller overview

| Controller | Trigger | Cleanup condition |
| --- | --- | --- |
| PV failover reconciler | Relevant PersistentVolume updates | The PV ownership metadata points to another node |
| LVM orphan scanner | Periodic scan | No matching PV exists, or the PV ownership metadata points to another node |

Both controllers use the current ownership annotation first:

```text
localdisk.csi.acstor.io/selected-node
```

If it is absent, they fall back to the initial node stored in CSI volume
attributes:

```text
localdisk.csi.acstor.io/selected-initial-node
```

## PV failover reconciler

The event-driven reconciler watches updates to PersistentVolumes managed by
`localdisk.csi.acstor.io`.

It only processes PVs that:

- Use this CSI driver
- Are in the `Available` or `Bound` phase
- Do not have a deletion timestamp
- Contain ownership metadata for a different node

If the corresponding LV exists locally, the reconciler attempts to unmount it
and then deletes it. An unmount failure is logged, but deletion continues. This
prevents a stale mount from indefinitely blocking reclamation of an LV that the
cluster now assigns to another node.

The reconciler records these PV events:

- `CleaningUpOrphanedVolume`
- `CleanedUpOrphanedVolume`
- `CleanupFailed`

## LVM orphan scanner

The periodic scanner covers cleanup that an update event may miss, including:

- LVs left behind while the controller was unavailable
- LVs whose PV was deleted
- LVs created or modified outside the normal CSI request flow
- LVs whose PV ownership moved to another node

The scanner:

1. Lists volume groups tagged as managed by local-csi-driver.
2. Falls back to the default `containerstorage` volume group if no tagged
   groups are found.
3. Lists the LVs in each selected volume group.
4. Builds each CSI volume ID as `<volume-group>#<logical-volume>`.
5. Uses the `spec.csi.volumeHandle` field index to query matching PVs without a
   cluster-wide list-and-filter operation.
6. Deletes the LV when no matching driver PV exists or ownership points to a
   different node.

The scanner attempts to resolve and unmount the device path before deletion.
Failure to resolve or unmount the path is logged, but LV deletion continues.
Failure to delete one LV does not stop the scanner from processing other LVs.

Orphan-scanner events are recorded against the current Node:

- `CleaningUpOrphanedLV`
- `CleanedUpOrphanedLV`
- `OrphanCleanupFailed`

## Configuration

The driver exposes these flags:

| Flag | Binary default | Purpose |
| --- | --- | --- |
| `--enable-lv-garbage-collection` | `true` | Enable the event-driven PV failover reconciler |
| `--enable-lvm-orphan-cleanup` | `true` | Enable the periodic LVM orphan scanner |
| `--lvm-orphan-cleanup-interval` | `30m` | Set the periodic scan interval |

The Helm chart supplies its own deployed defaults. In particular,
`cleanup.lvmOrphanCleanup.interval` currently defaults to `5m`, overriding the
binary's `30m` fallback.

The scanner constructor has a `10m` programmatic fallback when it receives a
zero interval. Normal driver startup passes the command-line value, so
operators should use the binary or Helm defaults above rather than relying on
the constructor fallback.

Example driver arguments:

```sh
# Disable event-driven failover cleanup.
--enable-lv-garbage-collection=false

# Disable periodic orphan scanning.
--enable-lvm-orphan-cleanup=false

# Scan every 15 minutes.
--lvm-orphan-cleanup-interval=15m
```

## Failure and retry behavior

- Kubernetes API and reconciliation errors are returned to controller-runtime
  for retry.
- A missing local LV is treated as already cleaned.
- The periodic scanner continues after an individual VG, PV lookup, unmount, or
  LV deletion failure.
- Cleanup is idempotent because deleting an already removed LV is treated as
  success.
- Neither controller recovers application data. Availability-mode recovery
  creates a new empty LV before old-node cleanup occurs.

## Testing

Run the package tests with:

```sh
go test ./internal/gc/...
```

## Related documentation

- [Architecture](../../docs/architecture.md) - cleanup ownership and lifecycle
- [PV Recovery](../../docs/design/pv-recovery.md) - failover and ownership
  transitions
