---
name: csi-spec-conformance
description: 'Verify that changes to the local-csi-driver conform to the Container Storage Interface (CSI) specification. Use when adding or modifying any CSI gRPC RPC (Identity, Controller, Node), changing advertised capabilities, error codes, idempotency behavior, or capacity handling. Cross-references the upstream CSI spec, runs focused unit tests (the csi-test sanity suite is optional), and applies a per-RPC conformance checklist.'
---

# CSI Spec Conformance Check

A skill for confirming that the driver's CSI gRPC surface conforms to the
[Container Storage Interface specification][csi-spec].

[csi-spec]: https://github.com/container-storage-interface/spec

This repository pins the spec at **v1.12.0**
(`github.com/container-storage-interface/spec v1.12.0` in `go.mod`). Always
check behavior against that exact tag, not `master`.

Authoritative references (pin to the same tag):

- Spec prose (error tables, idempotency rules, field semantics):
  <https://github.com/container-storage-interface/spec/blob/v1.12.0/spec.md>
- Protobuf service/message definitions:
  <https://github.com/container-storage-interface/spec/blob/v1.12.0/csi.proto>

## When to use this skill

Trigger this skill whenever a change touches the CSI gRPC surface, including:

- Adding, removing, or modifying an RPC handler in `internal/csi/`.
- Changing advertised capabilities (`controllerCapabilities`,
  `nodeCapabilities`, plugin/volume-expansion capabilities) in
  `internal/csi/core/lvm/lvm.go`.
- Changing returned gRPC status codes or the typed-error translators
  (`fromCoreError` in `internal/csi/controller/controller.go`,
  `fromVolumeError` in `internal/csi/node/node.go`).
- Changing capacity/size handling, idempotency logic, or volume-id parsing.

## Where the CSI surface lives in this repo

| Service    | Server                                   | Volume logic (LVM core)               |
| ---------- | ---------------------------------------- | ------------------------------------- |
| Identity   | `internal/csi/identity/`                 | n/a                                   |
| Controller | `internal/csi/controller/controller.go`  | `internal/csi/core/lvm/controller.go` |
| Node       | `internal/csi/node/node.go`              | `internal/csi/core/lvm/node.go`       |

- Capabilities are declared in `internal/csi/core/lvm/lvm.go`
  (`controllerCapabilities`, `nodeCapabilities`, plugin capabilities).
  Commented-out entries mark deliberately-unsupported RPCs - those RPCs must
  return `codes.Unimplemented`.
- Typed volume errors live in `internal/csi/core/interfaces.go` (`core.Err*`)
  and are translated to gRPC status codes at the server boundary. Keep the
  typed-error + translator pattern: core/volume layers return `core.Err*`,
  servers translate.
- The sanity suite lives in `test/sanity/` and runs the upstream
  `github.com/kubernetes-csi/csi-test/v5` sanity tests.

## Workflow

Follow these phases in order. Do not skip the spec cross-reference.

### Phase 1: Scope the change

1. List every CSI RPC affected by the diff (`git diff` against the base branch,
   focus on `internal/csi/`).
2. For each affected RPC, open the matching section of `spec.md` at tag v1.12.0
   and the message definitions in `csi.proto`. Note:
   - Required vs optional request/response fields.
   - The RPC's **error table** (the gRPC codes the RPC is allowed to return and
     the precondition for each).
   - Whether the RPC **MUST be idempotent**, and what "already done" returns.
   - Which **capability** must be advertised for the RPC/feature to be callable.

### Phase 2: Apply the per-RPC conformance checklist

For every affected RPC, verify each item. Cite the spec line/section for any
deviation.

- [ ] **Capability gating.** If the RPC or a feature it exposes requires a
      capability, that capability is advertised in `lvm.go`, AND the reverse
      holds: every advertised capability has a working implementation.
      Unsupported RPCs return `codes.Unimplemented`, not a fake success.
- [ ] **Required-field validation.** Missing/empty required request fields
      return `codes.InvalidArgument` (e.g. volume_id, capacity_range where
      required, volume_capability, target/staging path). Check before any side
      effects.
- [ ] **Error codes match the spec table.** Each failure path returns a code
      listed in that RPC's error table. Common mappings used here:
  - invalid input -> `InvalidArgument`
  - object not found -> `NotFound`
  - exists with incompatible params -> `AlreadyExists`
  - requested `capacity_range` is not allowed by the plugin (e.g. missing,
    zero, or otherwise invalid) -> `OutOfRange` for `CreateVolume`,
    `NodeExpandVolume`, and `ControllerExpandVolume`. Recovery is "fix the
    range", so this MUST NOT be used for transient exhaustion.
  - not enough space / cannot provision the requested capacity -> always
    `ResourceExhausted`, in every RPC (`CreateVolume` and the expand RPCs
    alike). Recovery is "free space and retry with backoff". Do not collapse
    this into `OutOfRange`.
  - wrong node / not staged -> `FailedPrecondition`
  - unknown/internal failure -> `Internal` (never leak a bare `Unknown`;
    non-status errors must be wrapped, e.g. via `fromCoreError` /
    `fromVolumeError`).
- [ ] **Idempotency.** Calling the RPC twice with the same args succeeds. For
      create/expand, if the volume already satisfies the request, return `0 OK`
      with the **actual** current capacity/size (LVM rounds up to 4 MiB extents
      - return the real size, not the requested size). For
      delete/unpublish/unstage, a missing object returns success, not
      `NotFound`.
- [ ] **Capacity correctness.** Returned `capacity_bytes` reflects the actual
      allocated size from the backend (re-query the LV), and respects
      `required_bytes` / `limit_bytes`. See issue #116 for the create-path
      precedent.
- [ ] **VolumeCapabilities honored.** Access type (Mount vs Block) and access
      mode are validated and respected; unsupported combinations are rejected
      appropriately.
- [ ] **No side effects on validation failure.** Argument validation happens
      before any LVM mutation.

### Phase 3: Run the focused unit tests

The fast, always-runnable conformance gate is the focused unit-test suite for
the affected packages. Run it for any change that is unit-testable, e.g.:

```bash
go test ./internal/csi/node/... ./internal/csi/core/lvm/... -count=1
```

The csi-test sanity suite (`make test-sanity`) is the heavier, executable
conformance gate, but it is **optional** for this skill: it drives a real
driver instance and requires a deployed cluster, so do **not** run it by
default. Only run it when explicitly requested, or when a change is risky and a
suitable cluster is available. When it is run:

- Deliberate, documented exceptions are listed in the `skips` slice in
  `test/sanity/sanity_test.go`. If a sanity test newly fails, the correct fix
  is almost always the driver, not a new skip. Only add a skip with an explicit
  justification comment, and flag it for human review.

### Phase 4: Validate advertised capabilities end-to-end

1. Diff the capability lists in `lvm.go` against the RPCs actually implemented.
2. Confirm `*GetCapabilities` responses (Identity plugin caps, Controller caps,
   Node caps) match the spec's capability enums for v1.12.0 and match real
   behavior.
3. Confirm any newly-enabled capability is also exercised by sanity and/or e2e
   tests.

### Phase 5: Report

Produce a short report:

- RPCs reviewed and the spec section consulted for each.
- Checklist results, with spec citations for any deviation found.
- Unit test results (and sanity results only if the suite was run).
- Any `skips` added or capabilities changed, called out for human review.

## Guardrails

- Conform to the spec **version pinned in `go.mod`** (v1.12.0). Do not cite
  `master`.
- Do not silence a failing sanity test by adding a skip unless you can cite a
  concrete, documented reason; prefer fixing the driver.
- Preserve the typed-error + server-side translation pattern; do not return
  bare `Internal`/`Unknown` for cases the spec assigns a specific code.
- Keep changes minimal and surgical; do not "fix" unrelated RPCs in the same
  pass - note them for a follow-up instead.
