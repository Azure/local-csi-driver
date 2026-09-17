# local-csi-driver Webhooks

The manager can host two admission webhooks:

- A validating webhook for PersistentVolumeClaim creation
- A mutating webhook that adds storage-aware node affinity to Pods

Both webhooks are enabled by default in the Helm chart. They are independent
features, but the hyperconverged webhook is also coupled to the driver's PV
recovery behavior.

## Manager lifecycle

The manager runs as a Deployment and uses controller-runtime leader election.
When either webhook is enabled, the manager:

1. Starts the certificate rotator.
2. Creates or renews the serving certificate Secret.
3. Updates the validating and mutating webhook configurations with the CA
   bundle.
4. Waits for certificate setup before registering handlers.
5. Reports Ready only after the certificates and webhook server are ready.

The default chart deploys two manager replicas. Leader election limits
leader-elected controller work to one replica. It does not gate the webhook
server: all ready manager replicas register and serve admission handlers, and
the webhook Service can route requests to any of them.

Both webhook configurations use `failurePolicy: Ignore`. If the webhook cannot
be reached or returns a transport-level failure, the API server allows the
request without validation or mutation. This favors cluster availability but
means the storage safeguards are not guaranteed during a webhook outage.

## Enforce-ephemeral validating webhook

The validating webhook handles PVC `CREATE` requests at `/validate-pvc`.

A request is allowed when any of the following is true:

- The PVC has
  `localdisk.csi.acstor.io/accept-ephemeral-storage: "true"`.
- The PVC has a Pod owner reference, as expected for a generic ephemeral
  volume.
- The PVC has no StorageClass.
- The StorageClass does not exist.
- The StorageClass uses another provisioner.

A standard PVC using the local-csi-driver provisioner is denied when it does
not have the acknowledgement annotation or a Pod owner.

The annotation acknowledges that storage is local to a node and can be lost.
It does not make the volume durable or replicated.

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: local-data
  annotations:
    localdisk.csi.acstor.io/accept-ephemeral-storage: "true"
spec:
  accessModes:
    - ReadWriteOnce
  storageClassName: local
  resources:
    requests:
      storage: 10Gi
```

The handler records response and latency metrics and logs its admission
decisions.

## Hyperconverged mutating webhook

The mutating webhook handles Pod `CREATE` requests at `/mutate-pod`. It finds
PVC-backed volumes provisioned by local-csi-driver, resolves their PVs, and
adds node affinity for the nodes recorded in PV metadata.

When the webhook is disabled, the driver returns accessible topology from
`CreateVolume` and Kubernetes stores immutable node affinity on the PV. When
the webhook is enabled, the chart also starts the driver with
`--run-alongside-webhook=true`. For PVC-backed volumes with provisioner
metadata, the driver then:

- Omits accessible topology from the CSI response
- Stores the creating node in
  `localdisk.csi.acstor.io/selected-initial-node`
- Updates `localdisk.csi.acstor.io/selected-node` when staging occurs on a
  different node

The webhook uses this metadata to add Pod affinity instead of relying on
immutable PV affinity.

## Failover modes

The StorageClass parameter
`localdisk.csi.acstor.io/failover-mode` selects the affinity type.

| Mode | Pod affinity | Result when the current node is unavailable |
| --- | --- | --- |
| `availability` | Preferred | The Pod can move and receive a new empty LV |
| `durability` | Required | The Pod targets an owning node, subject to the limitations below |

`availability` is used when the parameter is absent or invalid.

Availability mode improves workload recovery but does not replicate data.
During `NodeStageVolume` on another node, the driver updates PV ownership and
creates an empty LV using the recorded capacity.

For a Pod with no existing required node affinity whose durability-mode PVs
all record the same owning node, durability mode restricts the Pod to that
node. This preserves access to the existing local data while the node remains
recoverable, but the workload cannot run when the node is unavailable.

The current implementation does not guarantee that restriction in these
cases:

- The Pod already has required node affinity. The webhook appends a separate
  `NodeSelectorTerm`, and Kubernetes ORs required terms. A node matching an
  existing term can therefore remain eligible without matching the storage
  term.
- The Pod references durability-mode PVs owned by different nodes. The webhook
  combines the owning nodes in one storage term, so any one of those nodes can
  satisfy it even though no node contains every existing LV.

If either case schedules the Pod away from an LV that contains existing data,
`NodeStageVolume` can transfer PV ownership and create an empty LV on the
selected node. Workloads that depend on durability mode must avoid existing
required node affinity and ensure all referenced durability-mode PVs share the
same owning node.

For Pods that reference multiple local-csi-driver PVs, use the same failover
mode for every PV. The current handler applies one affinity mode to the combined
node list and uses the mode from the last processed PV. Mixed modes can
therefore produce order-dependent behavior.

## Existing Pod affinity

The webhook preserves existing `spec.affinity` and appends its storage
requirement:

- Durability mode appends a required node selector term. Required terms are
  ORed, so this does not intersect the storage constraint with existing terms.
- Availability mode appends a preferred term with weight 100.

Applications should inspect the resulting Pod affinity when combining storage
affinity with their own required node selectors.

## Configuration

```yaml
webhook:
  enforceEphemeral:
    enabled: true
  hyperconverged:
    enabled: true
```

Disabling the hyperconverged webhook also causes the chart to set
`--run-alongside-webhook=false`, restoring PV topology affinity and disabling
automatic empty-volume recovery on another node.

## Related documentation

- [Architecture](../architecture.md) - manager and CSI request lifecycle
- [User Guide](../user-guide.md) - configure PVCs and failover modes
- [PV Recovery](pv-recovery.md) - ownership changes and empty-volume recovery
