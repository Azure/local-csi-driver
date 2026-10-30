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

The default chart deploys two manager replicas. Leader election allows one
manager to run the managed webhooks and controllers while another replica is
available for failover.

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
`--run-alongside-webhook=true`. For PVC-backed availability volumes with
provisioner metadata, the driver then:

- Omits accessible topology from the CSI response for availability mode
- Stores the creating node in
  `localdisk.csi.acstor.io/selected-initial-node`
- Updates `localdisk.csi.acstor.io/selected-node` when staging occurs on a
  different node

The webhook uses this metadata to add Pod affinity instead of relying on
immutable PV affinity.

Durability volumes retain accessible topology in the CSI response. Kubernetes
therefore records hard PV node affinity as a binding and scheduling backstop,
including when a claim is not bound when the Pod is admitted.

## Failover modes

The StorageClass parameter
`localdisk.csi.acstor.io/failover-mode` selects the affinity type.

| Mode | Pod affinity | Result when the current node is unavailable |
| --- | --- | --- |
| `availability` | Preferred | The Pod can move and receive a new empty LV |
| `durability` | Required | The Pod remains Pending for the node with the existing LV |

`availability` is used when the parameter is absent or invalid.

Availability mode improves workload recovery but does not replicate data.
During `NodeStageVolume` on another node, the driver updates PV ownership and
creates an empty LV using the recorded capacity.

Durability mode restricts the Pod to the node recorded by the PV. It preserves
access to the existing local data while that node remains recoverable, but the
workload cannot run when the node is unavailable. Staging also verifies the
recorded owner node and requires the existing LV; it does not change ownership
or create an empty durability replacement.

For Pods that reference multiple local-csi-driver PVs, durability constraints
take precedence over availability preferences. Multiple durability volumes
must have a common storage node.

Before upgrading, verify that each workload's durability PVs resolve to a common
storage node. Missing or conflicting placement now causes replacement Pod
creation to be denied; running Pods are not evicted. Reconcile actual data
placement before restarting affected workloads. `WaitForFirstConsumer` does not
relocate already-bound PVs.

## Existing Pod affinity

The webhook preserves existing `spec.affinity` and composes its storage
requirement:

- Durability mode adds the storage requirement to every existing non-empty
  required node selector term, preserving Kubernetes AND semantics. Empty
  selectors and empty terms remain unsatisfiable.
- Availability mode appends a preferred term with weight 100.

Applications should inspect the resulting Pod affinity when combining storage
affinity with their own required node selectors. Independent requirements can
make a Pod unschedulable.

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
