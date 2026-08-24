# local-csi-driver Volume Sanitization

## Scenario Definition

local-csi-driver provisions volumes as LVM logical volumes carved out of a
shared volume group on each node. When a volume is deleted, the driver runs
`lvremove`, which removes the logical volume from LVM metadata but does **not**
overwrite the underlying physical extents. Those extents return to the volume
group's free pool still carrying the previous volume's data.

The next volume created on that node may be allocated the same extents. LVM
zeroes only the first 4 KiB of a new logical volume, to clear filesystem
signatures; the remainder is whatever the previous occupant left behind. A
`volumeMode: Filesystem` volume is formatted before use, so the residual data
sits in unallocated filesystem blocks, but a `volumeMode: Block` volume is
handed to the container with no intervening filesystem.

Because a volume group is shared by every workload on the node, extents are not
partitioned by workload, namespace, or trust domain. Nothing in the current
delete path prevents a volume's contents from remaining legible to whatever is
allocated those extents next. On a node deliberately hosting a single trust
domain this is immaterial, but the driver should not require that assumption to
hold.

This document describes sanitization on delete: overwriting a volume's extents
with zeroes before returning them to the free pool.

## Goals

- Overwrite a deleted volume's extents with zeroes, and flush them to the
  device, before those extents become available for reallocation.
- Hold extents out of the free pool until that overwrite has completed, so that
  a crash, restart, or timeout cannot release dirty capacity.
- Apply the same guarantee regardless of volume mode, access type, or whether
  the deletion came from `DeleteVolume` or from garbage collection.

The resulting guarantee is forward-only: it applies to volumes deleted by a
build that includes sanitization.

## Non-goals

- **Sanitizing pre-existing free space.** Extents already free at upgrade are
  not tracked and are not wiped. This resolves itself as the free pool turns
  over through normal create and delete activity. Operators needing the
  guarantee immediately should wipe or recreate the volume group before
  upgrading. Volume expansion draws from the same pool and is covered by the
  same caveat.
- **Forensic erasure below the device's block interface.** Zeroing writes
  through the logical block address space and does not reach data retained in
  NAND by wear levelling or over-provisioning. That would require
  self-encrypting drives or cryptographic erase.
- **Protecting against operators.** Node root access can read the volume group
  directly at any time. Sanitization does not change the node's administrative
  trust boundary.
- **Sanitizing volumes removed outside the driver**, such as a manual
  `lvremove`.
- **Encryption.** Per-volume encryption is a stronger long-term answer, but is
  a separate change with its own key management design. See
  [Alternatives considered](#alternatives-considered).

## Design

### Overview

```text
                DeleteVolume                    Wipe Reaper (per node)
              +---------------+                +----------------------+
              | tag LV        |                | list tagged LVs      |
              | rename LV     | -- signal -->  | activate             |
              | return OK     |                | zero and flush       |
              +---------------+                | lvremove             |
                                               +----------------------+
                      |                                    |
                      v                                    v
              extents still held                  extents released
              by quarantined LV                   clean to free pool
```

Deletion is split in two. The synchronous phase moves the volume into
quarantine and returns immediately. An asynchronous reaper performs the
overwrite and only then removes the logical volume.

The split is forced by the deletion deadline. `DeleteVolume` is invoked by the
`csi-provisioner` sidecar, which applies a per-call timeout; the chart does not
pass `--timeout`, so the sidecar default of 15 seconds applies. Only a few
gigabytes can be zeroed in that window, while volumes are routinely hundreds of
gigabytes. Since the driver must fail closed rather than release a partially
zeroed volume, a synchronous wipe would be cancelled on every call, restart
from offset zero, and never complete. Raising the timeout only moves the bet:
any fixed deadline is a wager against volume size and device speed.

### 1. Quarantine (synchronous)

On `DeleteVolume`, the driver tags the logical volume `local-csi-wipe-pending`,
renames it to `local-csi-wipe-<uuid>`, and returns success.

The rename is the **commit point** for destruction, and the generated name is
the marker the reaper acts on. A volume that is tagged but still under its
original name is never a wipe target: it has not been taken out of service and
may still be referenced by a PersistentVolume, so zeroing it would destroy live
data. If the rename fails, the tag is rolled back and the caller retries the
whole operation. Should a crash leave a tag behind, `EnsureVolume` clears it
and reinstates the volume, which is safe because it is the same volume, for the
same volume ID, being returned to the tenant that owns it.

Commitment is deliberately keyed on the **name rather than the tag**, because
each can be absent independently and only one of those states is safe to
misread. A committed volume can legitimately lose its tag, if a concurrent
reinstatement clears it between the tag and the rename; keying on the tag would
make such a volume invisible to the reaper forever while it still held the
tenant's data. The reverse is not true: a tagged volume under its own name is
simply still in service.

Tagging still happens first, because it is what tells the orphan scanner to
leave the volume alone while quarantine is in progress.

The name is trustworthy as a marker because only quarantine produces it: it is
the fixed prefix followed by a generated UUID, and the CSI layer rejects volume
handles that name themselves into that namespace, which a pre-provisioned
PersistentVolume could otherwise do.

Renaming also frees the original name so that a later `CreateVolume` for the
same volume ID cannot collide with a volume still awaiting its wipe. A fresh
UUID is used each time because the same volume ID can be created and deleted
repeatedly while an earlier quarantined volume is still pending.

The extents stay allocated to the quarantined volume throughout. LVM will not
hand them to any other volume while it exists, so quarantine is what provides
the safety property; the zeroing that follows is what makes it safe to end.

Recovery state lives entirely in LVM metadata. No new persistent store, CRD, or
on-disk journal is introduced, and quarantine survives a driver restart or node
reboot.

### 2. Wipe reaper (asynchronous)

A node-local `manager.Runnable` in the driver DaemonSet, modelled on the
existing `LVMOrphanScanner`. For each committed volume it runs these steps in
order:

1. **Activate** (`lvchange --activate y`), because a volume can be quarantined
   while deactivated and one with no device node cannot be written to.
2. **Re-check**: re-read the volume and skip it unless it is still committed to
   destruction and reports itself closed (see below).
3. **Sanitize**: zero the volume and flush the device.
4. **Remove** (`lvremove`), releasing the now-clean extents.

The reaper runs on a timer and is additionally signalled by `DeleteVolume`
through a buffered channel, so the timer is only a backstop for volumes
inherited from a previous process. It sweeps unconditionally at startup, which
is what makes quarantine crash-safe, and wipes one volume at a time: zeroing is
bound by device write bandwidth, so concurrency adds interference with
foreground I/O without adding throughput.

### 3. Zeroing primitive

The device is opened `O_WRONLY|O_EXCL` and that descriptor is held for the
whole operation. `O_EXCL` fails if the device is still mounted, which gives an
in-use guard with no window between check and write.

`O_EXCL` alone is not enough, because the driver also serves raw block volumes.
Those are published as a device node rather than mounted, and a pod holding one
open takes no exclusive claim, so the open would succeed while a workload was
still using the device. The reaper therefore also consults LVM's own open state
immediately before zeroing and defers to any volume reporting itself open. That
check reads both `lv_device_open` and the open indicator in `lv_attr`, and
treats an attribute string too short to carry it as open, so that a field that
stops being reported degrades into "defer" rather than "safe to wipe".

Zeroing prefers the `BLKZEROOUT` ioctl issued on that descriptor, letting the
device satisfy the request with a write-zeroes command rather than transferring
zeroes across the bus. Work is issued in bounded chunks so that cancellation is
observed promptly. Where the ioctl is unsupported, the driver falls back to
writing zeroes explicitly, resuming at the offset where the ioctl was rejected.
The device is flushed before `lvremove`, so the zeroes cannot still be in the
write cache while the extents are back in the free pool.

The driver does **not** fall back to a plain discard. `BLKDISCARD` permits, but
does not require, a device to return zeroes for discarded blocks, so a discard
that appears to succeed can leave data readable.

Sizing uses the volume's actual size as reported by `lvs`, not the capacity
originally requested, because LVM rounds allocations up to whole extents and
the remainder is part of what must be cleared.

### 4. Failure handling

Sanitization fails closed. A volume that could not be zeroed keeps its tag and
its extents and is retried with backoff. It is never removed after a failed or
partial wipe.

A node with a persistently failing device therefore accumulates quarantined
volumes and loses usable capacity. That is the intended trade: capacity is
recoverable by an operator, disclosed data is not. The condition is surfaced
through events, and operators have a manual release procedure (see
[Releasing a stuck volume](#releasing-a-stuck-volume)).

### 5. Deactivated volumes

`EnsureVolume` treats a volume as corrupted when it exists in LVM metadata but
its device node is missing, and previously removed and recreated it. That
condition is rarely corruption; it is usually a **deactivated** volume, most
often after a reboot. The extents are intact and still hold the previous
contents, so removing the volume returns populated extents to the free pool,
making this a remanence vector with no deletion involved at all.

The driver now attempts `lvchange --activate y`, re-checks that the device node
actually appeared (`lvchange` can report success while it is still missing),
and uses the volume normally if it did. Otherwise it quarantines the volume and
fails the request. It never plain-removes.

This was checked against [PV Recovery](pv-recovery.md), which deliberately
recreates volumes **empty** during `NodeStageVolume`. The two do not conflict:
PV Recovery's empty recreation happens on a node the volume has moved *to*,
where no logical volume exists locally, which is a different branch. The case
that does change is a node that reboots and stages the same volume again, where
the contents now survive. That is compatible with PV Recovery, whose contract
is that applications must tolerate data loss, not that data must be destroyed.

### 6. Deletion paths that bypass DeleteVolume

Every path that removes a logical volume routes through quarantine:

| Path | Component |
| --- | --- |
| CSI delete | `internal/csi/core/lvm/controller.go` |
| Orphan cleanup and PV failover GC | `internal/gc/volume_manager_adapter.go` |
| Deactivated volume replacement | `internal/csi/core/lvm/lvm.go` |

The orphan scanner must skip volumes that are committed to sanitization. They
have no corresponding PV by construction, so without an explicit exclusion the
scanner would treat every one as an orphan and race the reaper to remove it
unwiped.

A volume that carries only the tag is deliberately **not** skipped. Its
quarantine was never committed, so it is still under its own name and still has
a volume ID: if it has a PersistentVolume the normal orphan check leaves it
alone, and if it does not, it is a genuine orphan, and routing it through
quarantine both commits it and gets it wiped. Skipping it instead would leave
it reclaimable by no component at all.

Deletion also has to fail closed on ambiguous evidence. `DeleteVolume` must be
idempotent, so a volume that is already gone is a success, but that conclusion
must be about the volume itself. LVM reports an invisible volume group with the
same not-found wording, and a group can be temporarily invisible, for example
before device scanning has finished after a reboot. The driver distinguishes
the two and retries rather than reporting success, which would otherwise let a
PersistentVolume be removed while a populated volume survived on disk with
nothing left referencing it.

### CSI conformance

Checked against CSI v1.12.0, the version pinned in `go.mod`. `DeleteVolume`
remains idempotent, and no capabilities or error codes change. The
specification places no requirement on data remanence and does not require
capacity to be reclaimed by the time `DeleteVolume` returns.

`DeleteVolume` now returns before capacity is available for reuse.
`GetCapacity` continues to report correctly, because quarantined volumes
genuinely occupy the volume group.

## Alternatives considered

| Option | Why not |
| --- | --- |
| Zero synchronously in `DeleteVolume` | Exceeds the sidecar timeout at realistic volume sizes; fails closed, retries from zero, never completes. |
| `issue_discards=1`, or `blkdiscard` on delete | Discard does not guarantee zero reads. Silently ineffective where unsupported. |
| Zero at create time instead | Puts the cost on the provisioning critical path, where a timeout is visible as a failed Pod start. |
| Thin provisioning (dm-thin) | `zero_new_blocks` would cover expansion and pre-existing free space too, but adds a device-mapper layer to the hot path of a fast-local-NVMe driver, plus write amplification, overcommit accounting, and metadata exhaustion as a new failure mode. |
| Per-volume encryption (dm-crypt) | Stronger, and covers pre-existing free space, but needs a key management design and costs CPU on every I/O. A possible future direction, not a substitute. |
| Disable raw block volumes | Narrows the most direct exposure without addressing the underlying remanence, and removes a supported feature. |

## How to Enable

Sanitization is always on; there is no flag to disable it.

An opt-out cannot be scoped safely, because the beneficiary is the **next**
tenant to receive the extents, not the one whose volume is being deleted. A
per-volume or StorageClass opt-out would let one workload weaken a control
protecting a different one, leaving a node-global switch as the only coherent
scope -- exactly the kind of setting that gets turned off for a benchmark and
never restored. The performance argument is weak in any case: capacity is not
reclaimed until the wipe completes either way.

### Driver flags

These pace the work; they do not control whether it happens.

```bash
# Backstop sweep interval (default 60s). Deletions are signalled directly, so
# this only covers volumes inherited from a previous process.
--volume-wipe-interval=60s

# Volumes wiped concurrently per node (default 1). Zeroing is bound by device
# write bandwidth, so raising this adds interference without adding throughput.
--volume-wipe-concurrency=1
```

### Releasing a stuck volume

If a device is failing, volumes can stay quarantined indefinitely. Operators
can release one manually, accepting that its extents return to the free pool
**without** being sanitized:

```bash
# List volumes awaiting a wipe. Select on the name, not the tag: the name is
# the commit marker, and a committed volume may no longer carry the tag.
lvs -o lv_name,vg_name,lv_size,lv_tags --select 'lv_name=~^local-csi-wipe-'

# Release one, forfeiting the guarantee for its extents.
lvremove --yes <vg>/local-csi-wipe-<uuid>
```

This is deliberately a per-volume, auditable action rather than a configuration
setting, so it cannot be applied fleet-wide and forgotten.

### Observability

Implemented:

- A Kubernetes event on each successful wipe, and a warning event on each
  failed attempt carrying the attempt count, the retry delay and the
  underlying error, so quarantined capacity that is not draining is visible
  without reading node logs. Events are recorded against the node, because by
  the time a volume is quarantined its PersistentVolume is already gone.
- Tracing spans for quarantine, each sweep, and each individual wipe.

Not yet implemented, and tracked as follow-up work:

- Metrics for volumes awaiting wipe, wipe duration, bytes zeroed, and failures.
- A startup diagnostic recording whether `BLKZEROOUT` is supported on each
  managed device, so the fallback path is visible before it is needed.

## Pain Points

- **Capacity is reclaimed later than deletion reports.** A workload that
  deletes a large volume and immediately creates another of the same size on
  the same node may fail to provision until the wipe finishes.
- **Wiping competes with running workloads** for device write bandwidth.
  Concurrency is bounded to limit this, which in turn bounds how quickly
  capacity drains.
- **Persistent device failure consumes capacity.** Volumes that cannot be
  zeroed stay quarantined by design and need operator attention.
- **Provisioning failures replace self-healing.** A volume whose device node
  cannot be reactivated is no longer silently recreated.
- **Pre-existing free space is unprotected** until the pool turns over.

## Related Documentation

- [PV Recovery Design](pv-recovery.md) - Volume recreation and orphan
  collection
- [PV Cleanup Design](pv-cleanup.md) - PV deletion when a node is lost
- [GC Controllers README](../../internal/gc/README.md) - Garbage collection
  implementation
- [Security Policy](../../SECURITY.md) - Reporting security issues
