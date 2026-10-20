---
name: code-review-lvm
description: >-
  Review changes involving LVM CLI commands, wrappers, parsing, volume
  provisioning, activation, device nodes, expansion, RAID, quarantine,
  cleanup, or LVM container packages. Use as part of normal PR review even
  when LVM is only reached through a helper. Resolve and fetch the LVM2
  source matching the reviewed runtime package, including distro patches,
  and check command contracts, partial success, retries, data preservation,
  concurrency, capacity, upgrade, and rollback against that source.
---

<!-- markdownlint-disable-next-line MD041 -->
## Purpose and boundaries

Find mismatches between the driver's assumptions and the LVM implementation it
actually runs. Review changed behavior and its callers, not just command syntax.

Perform a read-only review and return findings through the host's normal
PR-review output. Do not independently publish additional comments, change
production code, or mutate storage. Do not run privileged containers, attach
host devices, or execute destructive LVM commands to investigate a review.
Runtime mutation tests need a separately authorized disposable storage fixture.

## Establish scope and matching source

1. Record the PR base and head, relevant released version, target architecture,
   and affected command paths. Distinguish introduced regressions, intentional
   safety changes, and pre-existing defects. A historical fix is not evidence
   that the reviewed branch contains it.
2. Inspect the Dockerfile, package installation, image reference, and deployment
   overrides. Resolve the built image digest and installed LVM/device-mapper
   RPM versions and source-RPM names using its package database or trustworthy
   build metadata. A pinned base digest does not pin packages subsequently
   installed from live repositories. A current distro spec alone does not prove
   what a released image shipped.
3. Fetch the matching distro source RPM and upstream LVM2 archive into a
   temporary or review-artifact directory outside the repository. Extract with
   path-traversal protections. Verify available published checksums; retain the
   source identity, patches, build spec/options, and relevant runtime config.
   Apply the distro patches to the review copy as the spec directs. Reuse an
   already verified source cache only when its identity matches this review.
4. Check affected runtime settings: `lvm.conf`, device filters/devices files,
   `DM_DISABLE_UDEV`, udev availability, locking, activation, and helper tools.
   Follow into libdevmapper, filesystem helpers, or host-kernel behavior when
   the changed contract depends on them. A userspace version alone cannot
   establish every host-kernel or filesystem property.
5. For a package/configuration change, inspect both old and new implementations.
   If the image is not built or inaccessible, name the evidence gap and the
   candidate source version used. Continue supported static analysis, but do
   not claim exact runtime compatibility or substitute the newest upstream
   source as though it were the shipped implementation.

Use the separate CSI skill's latest stable specification for CSI obligations.
The matching installed LVM source, not the latest LVM release or the CSI Go
module version, determines backend behavior.

## Trace each command contract

Follow `RPC/controller -> helper -> option marshaling -> argv -> LVM parser ->
implementation -> persistent effects -> observed result -> caller/retry`.

- Verify accepted flags, positional names, default units, signed/absolute
  sizes, boolean values, force/yes behavior, and shell versus direct execution.
  Inspect marshaling rather than assuming an option struct produces the
  intended argv. Check supported LV types and actual helper availability.
- Trace requested bytes through every conversion. Human-readable Kubernetes
  quantities can have no suffix; unitless LVM sizes need not mean bytes.
  Check sector, extent, stripe, and RAID metadata allocation requirements,
  overflow, reported actual size, and both lower and upper capacity bounds.
  Aggregate VG free space does not guarantee a feasible per-PV RAID allocation.
- Check reporting fields, JSON mode, string/number/boolean representations,
  units, empty reports, hidden LVs, and multi-segment duplication against the
  selected report options. Do not infer one row per LV from a segment report.
- Match error classification to reachable diagnostics in this package version.
  Trace whole-command exit status, stderr/stdout, wrapping, and caller handling.
  Distinguish missing LV, unavailable VG/PV, active/in-use devices, stale paths,
  allocator exhaustion, and unrelated I/O errors. Generic substring matches
  must not turn unavailable storage or partial success into successful deletion.
  Consider locale when parsing text; prefer structured evidence where available.

## Keep storage states separate

Track these independently: PV/VG/LV metadata, kernel device-mapper mapping,
activation state, `/dev/mapper` node, `/dev/<vg>/<lv>` path, filesystem, mount,
and Kubernetes ownership.

- A missing device path does not prove metadata or data corruption. Check
  deactivation, reboot, disabled udev, and interrupted node creation before
  recommending destructive recreation. Preserve data if recovery is uncertain.
- Activation can be an idempotent success for an already-active mapping.
  Verify whether that path repairs device nodes; trace explicit `vgmknodes`
  or equivalent reconciliation and the caller's postcondition check.
- Metadata removal, mapping removal, and path cleanup need not be atomic.
  Before removing residual paths, verify ownership and that they cannot refer
  to a live or newly recreated volume. Missing metadata is not permission to
  remove arbitrary host files or active device-mapper mappings.
- Separate mdadm assembly from LVM activation. Existing-array restart paths,
  UUID/member identity, and device naming matter as well as first-time creation.

## Close partial-success and retry paths

For each mutation, identify durable transitions and consider failure,
cancellation, process death, and restart immediately after each transition.
Trace the next invocation using the state that actually remains.

| Transition | Question for the next attempt |
| --- | --- |
| PV/VG/LV metadata written | Does rediscovery reuse it without destroying data? |
| Mapping activated | Are required device nodes actually present? |
| LV capacity increased | Is filesystem growth still pending? |
| LV renamed or tagged for cleanup | Is ownership and deletion intent unambiguous? |
| Wipe partially completed | Is retry safe, and is reuse prevented until completion? |
| LV metadata removed | Can remaining mappings/nodes be reclaimed safely? |

An adequate LV size does not prove that filesystem expansion finished.
Follow the packaged LVM implementation's ordering and error exits for combined
LV/filesystem operations. Check block-volume and filesystem-volume paths
separately. A proposed retry command must itself work at the already-grown LV
size; do not assume rerunning the same command always repairs the filesystem.

After an ambiguous command error, distinguish observation from mutation.
Inspect whether any recovery context is still usable and has a bounded budget.
Do not recommend unbounded background retries or treat cancellation as rollback.
`exec.CommandContext` also does not prove prompt termination of kernel-blocked
I/O or every descendant/helper process.

## Concurrency, discovery, and lifecycle

- Check the actual serialization domain for creation, repair, removal, and
  reaping. Per-VG singleflight coalesces matching calls in one process; it is
  not a cross-process lock or automatic exclusion against another operation.
  Include overlapping DaemonSet instances, administrators, CSI RPCs, and GC.
- Trace get-create-get races and partial creation. Verify that rediscovery
  retains eligible already-LVM devices, tolerates a competing creator, and
  does not mistake stale filesystem paths for existing metadata.
- Follow ownership tags/selectors from provisioning through quarantine and
  reaping. Include pre-created untagged custom/default VGs, mixed tagged and
  untagged inventory, unavailable VGs, and empty results. Do not fix discovery
  by sweeping every VG on the host.
- Check upgrade, mixed-version rollout, and rollback after the new code has
  changed tags, names, LV layout, wipe state, or metadata. Can the older binary
  safely distinguish active volumes from pending deletion and resume cleanup?
  Preserve confirmed safety improvements rather than restoring destructive
  behavior merely to match an older release.
- For frequent reports, lock contention, device scanning, wipe bandwidth, or
  serialized operations, also apply the scalability skill. Derive cost from
  the real invocation rate and backend work, not just the presence of a loop.

## Evidence and findings

Use existing safe unit tests and mocks to validate driver branching, but do not
treat mocked CLI success as proof of real LVM semantics. Source inspection can
establish a reachable failure sequence without a cluster reproduction; label
that evidence honestly. Record unresolved runtime assumptions separately.

For each actionable finding, provide the changed driver location, matching
LVM source location/version, trigger and remaining state, user impact, retry
behavior, and the smallest correctness-preserving remedy. State whether it is
introduced or pre-existing. Scope the severity to the demonstrated conditions.
Use the host's normal review format rather than requiring a standalone report.
Include package/source limitations when material, and do not report a known
historical bug as newly introduced without tracing the reviewed diff.
