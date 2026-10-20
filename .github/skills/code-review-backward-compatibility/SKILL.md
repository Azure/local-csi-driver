---
name: code-review-backward-compatibility
description: >-
  Review pull requests for backward-incompatible behavior against the latest
  stable release, including rollback safety. Use during code review, upgrade
  and rollback risk assessment, and changes to configuration, admission, CSI
  operations, storage lifecycle, recovery,
  ownership, or persisted metadata. Find customer-visible behavior changes,
  including newly required dependencies and error propagation, not only API
  signature changes. Distinguish regressions from intentional safety fixes
  and pre-existing defects.
---

<!-- markdownlint-disable-next-line MD041 -->
## Purpose

Identify what an existing installation experiences after an upgrade. A change
can conform to an API and still break scheduling, recovery, or an operational
workflow. Conversely, preserving unsafe behavior is not always the right fix.

Perform a read-only review. Return findings through the host's normal review
output; do not independently publish additional comments, modify production
source, change configuration, or run destructive storage operations.

## Establish the comparisons

1. Resolve the latest published stable GitHub release, excluding drafts and
   prereleases. Record its tag and resolved commit, plus the PR base and head
   commits. Use supplied pinned snapshots when the review environment provides
   them. Do not substitute the current checkout or highest local tag.
2. Compare the PR base with its head to identify changes introduced by the PR.
   Compare the release with the head to identify upgrade-visible differences.
   Inspect release-to-base differences before attributing a finding to the PR.
3. Check ancestry and release-line applicability when history is available.
   Report an inappropriate or unavailable baseline instead of claiming the
   review passed. Do not treat unreleased sibling PRs as already deployed.

## Trace behavior, not just changed lines

Read the changed entry points and their relevant callers, helpers, tests,
defaults, and public documentation. Trace the complete operation through
admission, scheduling, provisioning, staging, recovery, and cleanup when those
components participate in the changed behavior.

For each changed condition or helper, identify which existing inputs move
between branches. Pay particular attention to new fallbacks, stricter checks,
early returns, changes to success timing, and new dependencies. Determine
whether a fallback merely supplies metadata or also enables new side effects.

Use a compact transition table while reasoning:

| Existing input or state | Released outcome | Proposed outcome | Impact |
| --- | --- | --- | --- |
| Supported configuration or persisted resource | Code path | Code path | Effect |

Do not assume tests using newly generated resources establish upgrade safety.
Read the actual fields persisted by the released implementation and the
metadata required by the proposed implementation.

## Dependency and error-path review

For each added or changed external lookup, compare both success and failure
paths. Unchanged business logic can still become unavailable when an optional
metadata lookup becomes a prerequisite.

- Enumerate changed reads, writes, and helper error returns. Trace each error
  through its caller to the admission response or CSI status, including
  staging and expansion. Compare released log-and-continue or fallback paths
  with proposed early returns.
- Check not-found, permission-denied, transient service failure, and ambiguous
  results separately. Distinguish cache hits from cache misses requiring a
  live read. Check changes from direct reads to indexed lists against actual
  RBAC and cache setup; do not assume either compatibility or an outage.
- Identify the previously working operation and concrete failure condition.
  Separate a dependency-availability regression from deliberate rejection of
  invalid ownership or missing storage.
- Before concluding, account for each changed dependency and error path in
  the reasoning, even if the happy path preserves data and all tests pass.
  Report material changes with evidence, not speculative outage claims.

## Compatibility checklist

### Configuration and admission

- Check Helm values, flags, StorageClass parameters, defaults, omitted values,
  invalid-value handling, and existing user overrides.
- Evaluate combinations of application constraints and driver constraints.
  Check aggregate behavior, processing order, and partially resolved inputs.
- Distinguish admission rejection from successful admission followed by an
  unschedulable Pod. Explain when the change first affects a running workload,
  such as restart, replacement, or restaging, rather than implying eviction.
- Do not assume a webhook failure policy ignores explicit denial responses.

### Persisted state and storage lifecycle

- Consider both dynamically provisioned resources and supported imports or
  static resources. Include absent, empty, and populated optional metadata.
- Follow the real resource identity and each supported ownership source.
  Verify fallback precedence against the released resource format.
- Check both healthy and missing backend state. Determine whether an upgrade
  changes fail-closed behavior into allocation, recreation, formatting,
  deletion, quarantine, or broader cleanup authority.
- Check capacity accounting, pending resize, reclamation timing, background
  work, error codes, idempotency, and retries where the diff affects them.
- Do not assume new provisioning metadata is retroactively written to
  existing resources.

## Rollback safety

Assess rollback in every review, independently of forward upgrade safety.
Trace release -> proposed version -> release after the proposed version has
performed real operations. Reverting binaries or manifests does not undo
persisted state changes.

- Identify metadata, annotations, labels, resource identities, configuration,
  on-disk formats, and backend state the proposed version can create or
  modify. Read the released code that would consume that state after rollback.
- Check whether the released version can discover, mount, resize, recover,
  and clean up both pre-existing resources and resources created or changed
  by the proposed version. Include partial upgrades and failed operations.
- Consider mixed-version node plugins, controllers, and webhooks during
  rollout and rollback. Trace differing interpretations of ownership,
  capabilities, defaults, and cleanup authority across those components.
- Identify irreversible migrations, deletion or formatting, and safety
  guarantees that an older binary would stop enforcing. State whether
  rollback requires conversion, ordering, feature restrictions, or is unsafe;
  do not recommend restoring unsafe behavior merely to allow downgrade.
- Explicitly report rollback as supported by the inspected paths,
  conditional, blocked, or unverified, with evidence and conditions. If the
  change has no rollback-relevant effect, explain why. Missing released source
  or runtime evidence is a limitation, not proof of rollback safety.

## Classify and substantiate findings

Separate these outcomes:

- **Regression:** a previously supported configuration or valid persisted state
  stops working, loses data, or changes semantics unintentionally.
- **Intentional breaking change:** a correctness or safety improvement changes
  an observable contract. Explain migration or rollout requirements without
  automatically recommending restoration of unsafe behavior.
- **Pre-existing defect:** present on the PR base or release and not worsened by
  this PR. Do not present it as a new regression. If changed control flow
  exposes the defect to previously unaffected inputs, describe that new
  exposure separately.
- **Uncertain risk:** plausible but missing a verified precondition or trace.
  State what evidence is needed and avoid claiming reproduction.

Use the host's requested review format rather than requiring a separate report.
For each material finding, give the affected existing configuration, released
and proposed outcomes, concrete impact, classification, severity, confidence,
and source locations for both paths. Verify that each cited file and line
range exists in the exact snapshot being cited. State whether evidence is
source-level, unit-tested, or reproduced on a cluster. A descriptive PR title,
comment, or existing test assertion is not proof of compatibility.

Prefer narrowly relevant regression tests using existing test infrastructure.
Do not create a cluster, touch host devices, or expand validation infrastructure
without authorization. Record unavailable checks explicitly.

Report no material breaking change when that is what the evidence supports.
Do not invent findings to satisfy a checklist, hardcode a historical release,
or claim that a prompt guarantees exhaustive detection.
