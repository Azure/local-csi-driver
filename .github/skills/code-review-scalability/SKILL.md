---
name: code-review-scalability
description: >-
  Review runtime and deployment changes for scalability regressions in
  Kubernetes controllers, admission webhooks, CSI drivers, and Azure cloud
  integrations. Use during PR review and when changes affect API clients,
  caches, watches, retries, ARM calls, concurrency, mount/device operations,
  garbage collection, or telemetry. Trace actual resource costs as nodes,
  volumes, requests, and churn grow; report evidence-backed risks with
  minimal, correctness-preserving fixes.
---

<!-- markdownlint-disable-next-line MD041 -->
## Purpose and boundaries

Find operations that become expensive or unavailable as the installation grows,
even when each operation is functionally correct. Review Kubernetes API-server
load, Azure Resource Manager (ARM) and resource-provider traffic, and node-local
work. A local CSI driver may have no ARM calls; mark that plane not applicable
rather than inventing a dependency.

Perform a read-only review. Produce findings through the host's normal review
output; do not independently publish comments, edit production code, or run
cluster load tests, cloud mutations, or destructive storage commands.

## Establish scope

1. Record the PR base and head commits, or the supplied pinned source snapshot.
   Review changed behavior and enough unchanged callers, dependencies, tests,
   and deployment configuration to explain its cost.
2. Separate introduced regressions from pre-existing risks. An unchanged helper
   can become a new problem when a change moves it into a hotter path. If only
   one snapshot is available, do not claim when the defect was introduced.
3. Determine deployment multiplicity: singleton leader, active replicas,
   DaemonSet nodes, sidecar workers, and independent clients. Inspect actual
   defaults and overrides, not just a constructor's name.
4. Trace framework and SDK behavior at the reviewed dependency revision.
   Consult current official service documentation for applicable limits, but
   do not retroactively apply today's limits to a historical incident.
   If relevant source or runtime configuration is unavailable, state the gap.

## Build a cost trace

For each relevant changed path, follow:

`trigger -> frequency -> fan-out -> concrete operation -> resource charged`

Include reconcile, admission, CSI retries, health probes, metrics collection,
startup, and periodic cleanup as possible triggers. Trace helper calls into
the actual cache, SDK, subprocess, or filesystem implementation.

Use a compact ledger while reasoning:

| Trigger and deployment | Work per trigger | Multiplier or bound | Resource |
| --- | --- | --- | --- |
| Per-node sweep every T seconds | Scan P objects | N nodes | CPU, bytes, API |
| RPC with retries | C requests per attempt | R RPC/s, A attempts | ARM or data API |

Name the units. `N * P / T` may describe objects processed per second, not
requests per second. A paginated list of P objects with page size L produces
approximately `ceil(P / L)` requests, while still transferring O(P) objects.
A cache-backed list has local CPU/copy cost; it is not a live API list per call.
Count its initial list/watch, reconnects, and replicas separately.

Inspect both steady state and synchronized bursts: rollout, restart, cache
expiry, failover, mass deletion, and throttled or unavailable dependencies.
Measure or derive costs; label illustrative numbers as assumptions, not
benchmarks. Do not infer an outage merely from a loop or an O(N) operation.

## Kubernetes API-server and controller checks

- **Read path:** Identify manager cached clients, uncached readers, typed
  clientsets, and explicitly uncached kinds. A seemingly narrow Get can start
  a broad informer; an existing cache can instead make repeated Gets cheap.
  Inspect cache scope, startup synchronization, and replica count.
- **Index registration:** Treat `FieldIndexer.IndexField` as a potential
  informer dependency, not just a local lookup optimization. Registration
  obtains an informer and creates one if absent; the informer lists and
  watches the configured resource scope when the cache runs. Check existing
  informer reuse, including full-object versus metadata-only caches, cache
  scope, startup synchronization, and DaemonSet fan-out. A selective field
  index does not narrow the underlying LIST/WATCH.
- **Selection:** Distinguish filtering in application code, cache field
  indexes, and API-server selectors. Verify index registration and supported
  server fields for that resource. Missing cache indexes can produce errors;
  do not assume an automatic full-scan fallback. Do not suggest a Pod's
  `spec.nodeName` field selector for a PersistentVolume. Verify pagination
  support on the actual reader; a cache limit is not a server continuation
  mechanism and can silently omit objects if treated as a complete inventory.
- **Watch cost:** Predicates filter delivered events, not necessarily the
  underlying list/watch or retained cache. Inspect selector-scoped cache
  configuration separately. Count duplicate node-local informers watching
  cluster-wide resources and startup/reconnect transfer.
- **Repeated work:** Trace list-then-filter, N+1 live reads, nested scans,
  per-object event mapping, and broad admission matching. A full cached list
  may still allocate and scan all objects. Do not replace a few scoped Gets
  with a cluster-wide informer without accounting for memory and watch cost.
- **Writes and queues:** Find unconditional no-op writes and self-triggered
  update loops. Verify predicates do not suppress required status, deletion,
  or finalizer events. Trace the actual queue limiter and requeue behavior;
  returning an error commonly invokes backoff and is not by itself a storm.
- **Request budgets:** Include admission deadlines and cancellation. Inspect
  client QPS/burst and queue concurrency, but do not present raising them as
  a fix for amplified work. A local limiter is not a cluster-wide budget.

## ARM and Azure backend checks

- **Endpoint and scope:** Classify each operation as ARM/resource-provider,
  storage data plane, identity/metadata service, or Kubernetes API. Trace
  convenience helpers, credential/key retrieval, account discovery, and
  background polling; a data operation may hide management-plane calls.
- **Amplification:** Count actual requests per volume operation, including
  listing pages, repeated properties/key reads, long-running-operation polls,
  SDK retries, driver retries, and CSI caller retries. Do not multiply retry
  counts blindly: establish which layers execute for the same failure.
- **Limits:** Identify the shared subscription, principal, region, account,
  resource, and operation class that receive traffic. ARM and individual
  providers may throttle differently. Verify current applicable limits before
  quoting a number; avoid one universal Azure QPS limit.
- **Caches:** Follow cache keys, scope, TTL, success/error population,
  invalidation, and eviction. Verify that a claimed hit actually skips the
  remote call. Simultaneous misses or expiry can still stampede even with a
  cache. Consider coalescing equivalent in-flight reads without serializing
  unrelated resources or retaining failures indefinitely.
- **Throttling and recovery:** Check SDK handling before adding another retry
  loop. Respect applicable Retry-After guidance, bounded backoff with jitter,
  request cancellation, and shared concurrency limits. Inspect error details:
  a 429 may represent throttling or another transient condition, including
  resource locks. Separate these from quota and authentication failures.
- **Fix tradeoffs:** Data-plane APIs, batching, or cached credentials can reduce
  ARM traffic, but may change identity, permissions, key rotation, networking,
  or failure behavior. Preserve those contracts and name any prerequisites.

## Node-local and cross-cutting checks

- **Mount and device work:** Follow volume-stat, stage, publish, unpublish, and
  cleanup paths into mount table parsing, recursive directory walks, device
  scans, and LVM commands. Include probes and stats polling, not only writes.
  Scope or batch work where possible; a helper's harmless name proves nothing.
- **Remote filesystem work:** Follow directory listing, stat, and health-check
  calls across FUSE/blobfuse, NFS, or SMB boundaries. A local filesystem call
  can enumerate a remote container or share. Verify the actual implementation,
  caching, pagination, and short-circuit behavior before counting backend
  requests. Model entries per directory, bytes returned, remote pages, mounted
  volumes, and probe/retry frequency. Do not equate these data-plane requests
  with ARM traffic. Prefer bounded checks that preserve mount-health detection;
  verify that a seemingly bounded local read also bounds remote enumeration.
- **Concurrency:** Count global, per-node, per-account, and per-volume lock
  scope, blocking I/O duration, and bounded worker slots. Distinguish necessary
  backend serialization from needless serialization of independent work.
  Do not simply remove storage locks or assume callers serialize every RPC.
- **Latency and cancellation:** A call taking S seconds under a global lock
  bounds throughput near `1/S`; workers with external waits can exhaust their
  budget even without CPU saturation. Follow context/deadline propagation
  through exec, retries, locks, and cleanup. Cancellation is not proof that a
  kernel-blocked operation or its children terminate immediately.
- **Cleanup:** Check whether each node cross-checks local volumes against all
  cluster PVs, repeats inventory for every volume, or scans on synchronized
  timers. Preserve ownership verification and safe orphan handling when
  proposing narrower caches or incremental sweeps.
- **Churn and cardinality:** Bound retained maps, keyed locks, expired entries,
  metric label combinations, subprocess output, and buffered data. Distinguish
  live object count from all objects ever seen. Trace eviction, not just TTL
  assignment. Count histogram series and replica multiplicity where relevant.

## Challenge the finding and the fix

Before reporting a candidate:

1. Establish a reachable workload or failure condition and a source-supported
   trigger-to-cost chain. Check whether existing caching, indexing, batching,
   limiter behavior, or a small domain bound already mitigates it.
2. Describe the pressured resource and resulting symptom: API throttling,
   admission timeout, serialized volume operations, CPU/memory exhaustion, or
   delayed cleanup. Separate demonstrated behavior from conditional risk.
3. Propose the smallest fix that reduces that specific cost. State what remains
   proportional to cluster size and what moves elsewhere. Pagination bounds a
   response, not total sweep work; more workers can worsen backend pressure.
4. Preserve correctness: cache freshness, ownership, complete pagination,
   idempotency, credential rotation, and required locking. Assess upgrade and
   rollback if the fix adds metadata or depends on a newly populated index.
5. Suggest a focused regression check using existing tests: count remote calls
   across cache hit/miss/expiry, bound concurrent operations, or vary object
   count and assert scan/exec counts. A fake client alone does not establish
   real informer, server filtering, SDK retry, or mount behavior.

## Findings and confidence

Use the host's requested review format. Each actionable finding should include
the source location, trigger and affected configuration, scaling variables and
cost model, actual bottleneck, severity/confidence, and a concrete safe fix with
a focused verification idea. Cite relevant helper and configuration evidence
even when the review comment must anchor on a small changed-line range.

Severity follows demonstrated impact and bounds, not a pattern's name:
critical for supported broad availability loss; high for substantial throttling
or blocked storage operations under a realistic scale condition; medium for
bounded degradation; low for an optimization with limited impact. State
conditional assumptions instead of automatically escalating severity.

Do not pad a review with generic advice. Consolidate one root cause reported
through multiple entry points. Say when no actionable issue is found and list
material unverified paths. Source-only findings do not require a live outage
reproduction, but they do require a complete causal trace.
