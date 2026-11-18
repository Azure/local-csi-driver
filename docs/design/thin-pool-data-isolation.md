# Thin-Pool Data Isolation

## Problem Statement

The local-csi-driver provisions persistent volumes from node-local NVMe
devices. These devices can serve workloads owned by different tenants over
their lifetime.

When a tenant deletes a volume, LVM releases the logical volume's physical
extents but does not erase the data stored in those extents. A later volume can
receive some of the same extents. If the new volume exposes the underlying
block device without first sanitizing all allocated storage, the new tenant can
potentially read data written by the previous tenant.

The highest-risk sequence is:

1. Tenant A creates a raw-block persistent volume.
2. Tenant A writes sensitive data throughout the volume.
3. The persistent volume is deleted and the driver runs `lvremove`.
4. Tenant B creates another raw-block persistent volume in the same volume
   group.
5. LVM assigns extents formerly used by tenant A to tenant B's volume.
6. Tenant B reads stale bytes that were not overwritten during volume
   creation.

This is a data-isolation problem, not only a data-deletion problem. The driver
must ensure that storage previously used by one volume is not exposed through
another volume before it is initialized for the new owner.

## Current Design

### Disk and Volume Group Creation

The driver discovers local NVMe disks and creates LVM physical volumes and a
volume group from the selected devices. The default selection looks for disk
paths beginning with `/dev/nvme`, supported Microsoft NVMe Direct Disk models,
and devices whose type is `disk`.

The disk-selection behavior is described in
[`disk-selection.md`](disk-selection.md). Volume group creation and tagging are
implemented in `internal/csi/core/lvm/lvm.go`.

StorageClasses can also select a custom volume group by using the
`volumeGroup` parameter. Consequently, any thin-pool design must support one
independently managed pool in every volume group from which the driver
provisions volumes.

### Logical Volume Creation

The current implementation creates ordinary, physically allocated LVs in
`internal/csi/core/lvm/lvm.go`:

- With one physical volume, the driver creates a linear LV.
- With multiple physical volumes, the driver creates an LVM RAID0 LV using all
  selected physical volumes.
- The requested capacity is passed as the physical LV size.

The LVM wrapper in `internal/pkg/lvm/lvm.go` converts these options into an
`lvcreate` invocation. The created LV is not a thin LV and does not belong to a
thin pool.

This design provides predictable physical allocation. The RAID0 path also
allows an individual volume to use the aggregate bandwidth and IOPS of the
node's NVMe devices.

### Volume Publication

Filesystem volumes are formatted and mounted through the CSI node service.
Formatting a new filesystem overwrites filesystem metadata but does not erase
the entire block device.

Raw-block volumes are exposed directly through a bind mount of the LV block
device. They receive no filesystem formatting step. The relevant node paths
are in `internal/csi/node/node.go`.

The raw-block path therefore provides the clearest stale-data exposure: any
old bytes remaining in reused extents can be read directly by the new
consumer.

### Logical Volume Deletion

The controller removes an LV using `lvremove`. The deletion paths are in:

- `internal/csi/controller/controller.go`
- `internal/csi/core/lvm/controller.go`
- `internal/pkg/lvm/lvm.go`

`lvremove` removes the LV mapping and returns its extents to the volume group.
It does not overwrite all released extents.

LVM's ordinary-LV zeroing behavior does not provide a full-volume guarantee.
For an ordinary LV, the default `lvcreate --zero y` behavior clears only the
beginning of the LV, rather than every extent assigned to it. Signature wiping
similarly removes recognizable metadata but does not sanitize tenant data.

## Proposed Thin-Pool Design

### Storage Hierarchy

Create one driver-managed thin pool in each CSI-managed volume group and
allocate every CSI volume as a thin LV from that pool:

```text
Local NVMe devices
    |
    +-- LVM physical volumes
            |
            +-- CSI-managed volume group
                    |
                    +-- CSI thin pool
                            |
                            +-- Thin LV for PVC A
                            +-- Thin LV for PVC B
                            +-- Thin LV for PVC C
```

The pool is an infrastructure LV that persists while individual CSI volumes
are created and removed. Removing a thin LV does not remove its parent pool.

Conceptually, the pool is created with:

```sh
lvcreate \
  --type thin-pool \
  --zero y \
  --name csi-thinpool \
  --size <pool-data-size> \
  <volume-group>
```

Each persistent volume is then created with:

```sh
lvcreate \
  --virtualsize <requested-size> \
  --thinpool <volume-group>/csi-thinpool \
  --name <volume-name>
```

The exact commands may need a separate data LV and metadata LV when explicit
striping, metadata placement, or metadata sizing is required.

### How Thin-Pool Zeroing Prevents Data Leakage

A new thin LV has a virtual size but initially owns no physical data chunks.
Reads from unmapped regions return zero.

When a workload first writes to a logical region, device-mapper allocates a
physical chunk from the pool. With thin-pool zeroing enabled, the chunk is
zeroed before the new mapping can expose its contents to the thin LV. This
includes chunks that were previously mapped to a deleted thin LV.

The lifecycle is:

1. Thin LV A receives a pool chunk and writes tenant A's data.
2. Thin LV A is removed, returning the chunk to the pool.
3. Thin LV B later needs a chunk.
4. The pool can select the chunk formerly used by A.
5. The pool zeroes the chunk before making it visible through B's mapping.
6. Tenant B sees its own writes and zeroes in all otherwise unwritten bytes.

The security property depends on allocation-time zeroing. It does not depend
on which freed chunk is selected, whether discards reach the NVMe device, or
whether the deleted LV was a filesystem or raw-block volume.

The driver must explicitly request and verify zeroing. It must not rely on a
distribution's `lvm.conf` defaults. If an existing pool has zeroing disabled,
the driver should fail closed rather than provision volumes from it.

### Scope of the Guarantee

A zero-enabled thin pool prevents logical cross-volume disclosure through
normal thin LV devices. It is not cryptographic erasure.

It does not protect against:

- A process with host root access.
- A privileged workload that can open the pool data LV or physical NVMe
  devices.
- Direct forensic access to the underlying devices.
- Unsafe recovery or use of a pool whose metadata is corrupted.
- Storage snapshots or clones that intentionally retain shared data.

Encryption is required if these threats are in scope.

## Preserving Multi-NVMe Striping

### Current Striping

The current driver creates an independent RAID0 LV for each persistent volume
when the volume group contains multiple physical volumes:

```text
PVC A RAID0 LV -> NVMe 0, NVMe 1, NVMe 2
PVC B RAID0 LV -> NVMe 0, NVMe 1, NVMe 2
```

This determines the physical layout separately for each persistent volume.

### Thin-Pool Striping

Thin LVs do not independently control their physical layout. All thin LVs
inherit the layout of the shared pool data LV, commonly displayed as
`<pool-name>_tdata`.

If the pool is created with a simple command and no striping options, LVM can
create a linear data LV. A linear pool may allocate large consecutive regions
from one physical volume before moving to another. This would not preserve the
current aggregate multi-NVMe behavior.

To preserve striping, the pool data LV must itself be striped across all
selected NVMe physical volumes. Conceptually:

```sh
lvcreate \
  --type striped \
  --stripes <physical-volume-count> \
  --stripesize <stripe-size> \
  --size <pool-data-size> \
  --name csi-thinpool-data \
  <volume-group> \
  <physical-volume-list>

lvcreate \
  --size <metadata-size> \
  --name csi-thinpool-metadata \
  <volume-group>

lvconvert \
  --type thin-pool \
  --poolmetadata <volume-group>/csi-thinpool-metadata \
  --zero y \
  <volume-group>/csi-thinpool-data
```

The precise supported command shape should be verified against the LVM version
used in the driver image and host environment.

After creation, the driver should inspect the pool data LV and verify that its
segment type, stripe count, stripe size, and physical devices match the
expected configuration:

```sh
lvs -a \
  -o lv_name,segtype,lv_size,stripes,stripesize,devices \
  <volume-group>
```

Moving RAID0 below the pool preserves data distribution across the NVMe
devices, but changes the failure boundary:

- The pool layout is fixed when the pool is created.
- All thin volumes share the pool metadata.
- Damage to the pool can affect every persistent volume in the volume group.
- Loss of one device in a striped pool can invalidate the entire pool.

The existing RAID0 design is already nonredundant, but a shared pool can make
the operational blast radius larger.

## Potential Code Changes

### Add Thin-Pool Management

Add an idempotent operation that ensures a correctly configured pool exists
after the volume group has been created.

The operation should:

- Use a reserved pool name that cannot collide with a CSI volume name.
- Tag the pool as driver-managed infrastructure.
- Explicitly enable zeroing.
- Create pool data and metadata with intentional sizes and layouts.
- Serialize concurrent pool creation attempts.
- Validate an existing pool before adopting it.
- Reject an object with the expected name but the wrong LV type.
- Reject a thin pool that reports zeroing disabled.
- Verify the expected physical devices and striping configuration.
- Check that the pool is active, healthy, and monitored.

The normal Go-managed VG path and the optional mdadm setup path must converge on
the same pool creation and validation policy. A pool cannot span multiple
volume groups.

### Create Thin CSI Volumes

Change the volume creation logic in `internal/csi/core/lvm/lvm.go` to create a
thin LV from the managed pool:

- Use virtual size instead of physical size.
- Set the expected thin-pool name.
- Do not create an independent linear or RAID0 LV for each PVC.
- Apply an explicit CSI-volume tag.

The LVM option definitions in `internal/pkg/lvm/types.go` already expose
several relevant concepts, including virtual size, thin LVs, thin pools,
zeroing, discard behavior, metadata size, and full-pool behavior. Their command
generation and returned LV inspection data should be validated and extended as
needed.

### Strengthen Existing-Volume Validation

Current idempotency accepts an existing LV when its name and size are
compatible. In thin-pool mode, the driver should additionally require:

- The LV segment type is `thin`.
- The LV belongs to the expected managed pool.
- The LV carries the expected CSI-volume tag.
- Its virtual size is compatible with the request.
- It is not an infrastructure LV or an out-of-band object.

This prevents a legacy thick LV or manually created LV from bypassing the
isolation policy.

### Update Capacity Accounting

The current capacity path reports free space in the volume group. After a
thin pool consumes most VG extents, VG free space is no longer the capacity
available for CSI volume creation.

Thin provisioning introduces a capability that the current thick-LV design
does not have: LVM can create thin LVs whose combined virtual sizes exceed the
physical capacity of the pool. This repository must not use that capability.
Overcommit is not a supported deployment option, configuration choice, or
future extension of this design.

The driver must preserve the current capacity guarantee:

```text
A successful CreateVolume or expansion reserves enough physical pool
capacity for every byte of the resulting virtual volume to be written.
```

The permanent admission invariant for each managed pool is:

```text
sum of admitted CSI thin-LV virtual sizes
    + pending create reservations
    + pending expansion reservations
    <= safely allocatable pool data capacity
```

Safely allocatable capacity is less than the raw pool data size. It excludes
the configured operational reserve needed to keep the pool away from
exhaustion. VG extents reserved outside the pool for metadata growth,
autoextension, repair, and emergency recovery are also not CSI allocatable
capacity.

Capacity reporting and admission should instead account for:

- Safely allocatable pool data capacity.
- Pool metadata size and `Meta%`.
- Reserved VG headroom.
- Committed virtual capacity.
- Pending creates and expansions.

`Data%` records blocks already written and must not be used as available
capacity for admission. A sparse volume may consume little physical storage
while still holding a reservation for its complete virtual size.

For example:

```text
Pool data size:                         1,000 GiB
Operational reserve:                     100 GiB
Safely allocatable capacity:              900 GiB
Existing CSI thin-LV virtual sizes:        750 GiB
Maximum additional provisioned capacity:  150 GiB
```

A 200 GiB request must be rejected even if the existing volumes have written
only a few GiB. The driver has already promised the existing volumes that
their full 750 GiB can be written.

`CreateVolume` must perform capacity admission and thin-LV creation under a
per-pool lock:

1. Validate pool identity, zeroing, health, and metadata state.
2. Calculate safely allocatable pool capacity.
3. Sum the virtual sizes of all admitted CSI thin LVs.
4. Include create and expansion reservations not yet reflected by LVM.
5. Reject the request with the CSI insufficient-capacity response if it does
   not fit.
6. Create the thin LV while the reservation remains protected.

This serialization prevents concurrent requests from each observing the same
available capacity and collectively overcommitting the pool. Create retries
must be idempotent and must not count an existing LV or reservation twice.

`GetCapacity` must report:

```text
safely allocatable pool capacity
    - committed CSI thin-LV virtual capacity
    - pending reservations
```

It must not report VG free extents or physical chunks that remain unwritten but
are already promised to existing volumes.

The same reservation policy must be applied to volume expansion. Extending a
thin LV changes its virtual size and can succeed even when the pool cannot
support all future writes. The driver must reserve the requested virtual-size
increase before invoking `lvextend` and release the reservation if the
operation fails.

The implementation must not provide an overcommit ratio, best-effort mode, or
configuration switch that relaxes this invariant. Thin provisioning is used
here for zero-on-allocation and data isolation, not for capacity
overprovisioning.

### Exclude Infrastructure LVs from Enumeration and Garbage Collection

The pool and its internal LVs must never be treated as Kubernetes persistent
volumes.

Update volume listing and orphan-cleanup paths to exclude:

- The thin-pool LV.
- The pool data LV.
- The pool metadata LV.
- The LVM metadata spare.
- Any other driver infrastructure LV.

Filtering should primarily use explicit tags and verified LV types, rather
than only reserved name patterns.

This applies to:

- CSI volume enumeration in `internal/csi/core/lvm/controller.go`.
- Periodic orphan cleanup in `internal/gc/lvm_orphan_cleanup.go`.
- Failover cleanup and any path that directly invokes `lvremove`.

Without this change, garbage collection could classify the pool as an orphaned
CSI volume and attempt to remove it.

### Update Volume Group Cleanup

The current cleanup behavior uses the number of LVs in a VG when deciding
whether the VG can be removed. A persistent pool means an otherwise empty VG
will still contain infrastructure LVs.

Cleanup should:

1. Prove that no CSI thin LVs remain.
2. Remove the pool and its internal LVs deliberately.
3. Remove the volume group and physical-volume labels.

The pool must not be removed while any CSI thin LV exists.

### Add Pool Health and Monitoring

The driver should refuse new provisioning when the pool is unhealthy or above
configured thresholds.

Relevant signals include:

- Pool activation and health state.
- Data utilization.
- Metadata utilization.
- Whether pool monitoring is enabled.
- Remaining VG extents available for controlled extension.
- Whether automatic extension services are available and running.

Pool data and metadata exhaustion should produce actionable Kubernetes events
and metrics before workloads encounter I/O errors.

### Migrate Existing Volumes

Creating a thin pool from currently free VG extents does not protect existing
thick volumes. Existing volumes must be:

- Drained and recreated as thin volumes.
- Migrated using a separately designed and tested procedure.
- Or isolated in a legacy volume group until they are removed.

After thin-pool mode is enabled, the driver should not silently adopt or create
new thick CSI volumes.

## Validation Plan

The implementation should include tests for:

1. Raw-block cross-tenant reuse: fill volume A, delete it, allocate volume B,
   and require every byte of B to read as zero before B writes.
2. Partial-chunk reuse: partially write allocated chunks and verify all
   unwritten bytes are zero.
3. Filesystem-to-raw and raw-to-raw transitions.
4. Multiple discard modes to prove isolation does not depend on discard.
5. Rejection of a pool with zeroing disabled.
6. Rejection of legacy thick or incorrectly tagged LVs.
7. Concurrent pool creation and repeated CSI create and delete requests.
8. Garbage collection with an empty and populated pool.
9. Pool data and metadata exhaustion.
10. Capacity and expansion admission under concurrent requests, proving that
    combined virtual capacity can never exceed safely allocatable pool
    capacity.
11. Driver and node restart during create, delete, and pool extension.
12. Single-PV, multi-PV striped, and mdadm-backed volume groups.
13. Verification that the pool data LV spans the intended NVMe devices.
14. Newly addressable regions after volume expansion reading as zero.
15. Sparse-volume admission, proving that low `Data%` does not allow capacity
    already reserved by virtual volume sizes to be promised again.
16. Create and expansion retries, proving that reservations are neither
    duplicated nor leaked.
17. Out-of-band thin LVs, proving that unknown pool consumers are detected and
    provisioning fails closed rather than overcommitting capacity.

## Potential Issues and Operational Risks

- **Pool data exhaustion:** Reaching 100 percent can cause I/O errors, queued
  writes, workload hangs, or filesystem damage.
- **Pool metadata exhaustion:** Metadata exhaustion can affect every volume in
  the pool even when data space remains available.
- **Shared metadata failure domain:** Corruption or failed recovery can affect
  all tenants using the VG.
- **Accidental overprovisioning:** LVM permits aggregate virtual capacity to
  exceed physical pool capacity. The driver must permanently prohibit this
  through full virtual-capacity reservations, serialized admission, and
  fail-closed handling of unknown pool consumers.
- **Reservation drift:** Crashes, retries, out-of-band LVs, or incomplete
  cleanup can make accounting disagree with LVM state. Reconciliation must
  rebuild committed capacity from verified CSI thin LVs and block provisioning
  while the state is ambiguous.
- **Insufficient VG reserve:** Pool autoextension and metadata repair cannot
  work if all VG extents are allocated initially.
- **Monitoring dependency:** Automatic extension requires monitoring services
  and configuration that may not be present in the driver container or host
  namespace.
- **First-write latency:** Zeroing a newly allocated chunk adds latency and can
  amplify small writes.
- **Chunk-size tradeoffs:** Large chunks reduce metadata overhead but increase
  zeroing cost and space amplification. Small chunks increase metadata and
  transaction overhead.
- **Striping regression:** A default linear pool data LV can lose the current
  multi-NVMe performance behavior.
- **Larger blast radius:** A shared striped pool can make one device or metadata
  failure affect every CSI volume in the VG.
- **RAID0 durability:** Thin provisioning does not add redundancy. Loss of one
  NVMe device can invalidate data striped across the pool.
- **Garbage-collection safety:** Existing scanners can misclassify pool
  infrastructure unless filtering changes are deployed first.
- **Capacity-reporting regression:** Reporting VG free space after creating the
  pool can incorrectly advertise little or no usable capacity.
- **Legacy-volume bypass:** Existing or manually created thick LVs do not gain
  the thin-pool isolation guarantee.
- **Discard misconceptions:** TRIM and discard affect reclamation and device
  behavior but are not substitutes for allocation-time zeroing.
- **Non-cryptographic deletion:** Deleted data can remain on the underlying
  media even though it is not exposed through a newly allocated thin LV.
- **Recovery risk:** A questionable thin-metadata repair must not be treated as
  automatically safe for multi-tenant remounting.
- **Upgrade and rollback complexity:** Converting existing layouts is not a
  simple reversible configuration change and requires a drain or migration
  plan.

## Recommendation

Adopt one explicitly zero-enabled, driver-managed thin pool per CSI volume
group and require every newly provisioned CSI volume to be a tagged thin LV
from that pool.

Thin provisioning must not change the driver's capacity guarantee. The driver
must permanently prohibit overcommit by reserving each thin LV's complete
virtual size against safely allocatable physical pool capacity. There must be
no supported configuration or future mode that permits the combined admitted
virtual capacity to exceed that limit.

Do not roll out thin volume creation until:

1. Pool infrastructure is excluded from enumeration and garbage collection.
2. Create and expansion enforce serialized, full-size capacity reservations
   with no overcommit path.
3. Pool zeroing, health, metadata, and physical layout are verified.
4. Multi-NVMe striping is intentionally implemented in the pool data LV.
5. Existing thick volumes have a drain, migration, or isolation plan.
6. Data and metadata monitoring and exhaustion behavior are operationally
   defined.

This design addresses logical cross-tenant stale-data exposure while
preserving the opportunity to stripe I/O across the node's NVMe devices. It
does not replace encryption or privileged-host isolation where those stronger
security properties are required.
