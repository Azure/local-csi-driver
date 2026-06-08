# local-csi-driver Capacity-Template Controller

## Scenario

The Kubernetes Cluster Autoscaler (CA) decides whether scaling up a node group
will help a pending pod by simulating scheduling onto a *template node*
synthesised from an existing node in that group. When a pod's PVC uses a CSI
driver whose `CSIStorageCapacity` objects are *node-specific* (e.g. selected by
`kubernetes.io/hostname`), no capacity object exists for the simulated template
node, the scheduler's storage-capacity check fails, and CA refuses to scale up
the group. This is upstream issue
[kubernetes/autoscaler#9700][autoscaler-9700].

The local-csi-driver runs the external-provisioner sidecar in node-local mode
(`--node-deployment`, with `Topology=true` and `--strict-topology`) so each
DaemonSet pod handles its own Provision/Delete requests. Capacity tracking is
a separate feature, enabled by `--enable-capacity` on the same sidecar; the
combination of strict per-node topology and capacity tracking causes each
DaemonSet pod to publish a `CSIStorageCapacity` keyed by hostname for
accurate per-node accounting. That accuracy is exactly what breaks CA's
simulation - the simulated template node has a synthetic hostname for which
no capacity object exists.

## Goals

- Allow CA to scale up node groups whose nodes use the local-csi-driver,
  without giving up per-node capacity accuracy for live nodes.
- Work on any cluster (AKS, vanilla Kubernetes, Karpenter) and any grouping
  dimension (VM SKU, AKS agent pool, zone) without code changes.
- Avoid touching `CSIStorageCapacity` objects published by the
  external-provisioner sidecar.

## Non-goals

- Reporting accurate free capacity. The published value is an operator-supplied
  *template* quantity, used only to satisfy CA's simulation.
- Replacing the external-provisioner's per-node capacity reporting for real
  nodes.

## Design

### Mitigation in upstream Cluster Autoscaler

[kubernetes/autoscaler#9702][autoscaler-9702] adds the label
`cluster-autoscaler.kubernetes.io/template-node=true` to every template node
CA generates during scale-up simulation. CSI vendors can use this label in a
dedicated `CSIStorageCapacity.NodeTopology` selector that matches only
template nodes, leaving real-node capacity reporting untouched.

### Capacity-template controller

A controller in the `local-csi-manager` Deployment publishes one
`CSIStorageCapacity` per `(StorageClass x node group)` pair.

**Opt-in.** A `StorageClass` whose provisioner is `localdisk.csi.acstor.io`
opts in by setting the annotation:

```yaml
metadata:
  annotations:
    localdisk.csi.acstor.io/template-capacity: "1800Gi"
```

The value is parsed as a `resource.Quantity` and used as the published
capacity for every node group.

**Grouping.** Nodes are grouped by a configurable label (flag
`--capacity-template-node-group-label`). The default is
`node.kubernetes.io/instance-type` (VM SKU) because local NVMe capacity is a
property of the VM SKU, not of an arbitrary pool name, and the upstream
instance-type label is portable across cloud providers. Other useful values:

| Label                              | Use case                                  |
| ---------------------------------- | ----------------------------------------- |
| `node.kubernetes.io/instance-type` | VM SKU (default; portable, matches NVMe)  |
| `kubernetes.azure.com/agentpool`   | AKS node pool (per-pool overrides)        |
| `topology.kubernetes.io/zone`      | Failure domain (zonal capacity skew)      |

**Topology selector.** Each managed `CSIStorageCapacity.NodeTopology`
selects on **both** labels:

```yaml
nodeTopology:
  matchLabels:
    node.kubernetes.io/instance-type: Standard_L8s_v3
    cluster-autoscaler.kubernetes.io/template-node: "true"
```

The `template-node=true` constraint means the object only matches CA's
simulated template nodes. Real nodes (which never carry that label) continue
to use the per-node objects published by the external-provisioner sidecar.

**Reconciliation.** The controller does a full sync on every event and on a
periodic 5-minute resync. On each reconcile it:

1. Lists `StorageClasses` with provisioner `localdisk.csi.acstor.io` and the
   opt-in annotation.
2. Lists `Nodes` and collects the distinct values of the configured
   group label.
3. For each `(class, group)` pair, creates or updates a `CSIStorageCapacity`
   in the manager's namespace.
4. Garbage-collects managed objects whose `(class, group)` is no longer
   desired. Managed objects are identified by the
   `localdisk.csi.acstor.io/managed-by=capacitytemplate` label, so objects
   published by the external-provisioner sidecar are never touched.

Each managed object also carries `localdisk.csi.acstor.io/storageclass` and
`localdisk.csi.acstor.io/node-group` labels for human inspection.

**Naming.** Objects are named `local-csi-template-<storageclass>-<group>`,
sanitised to DNS-1123 and truncated with a short hash if longer than 253
characters.

**Watches.** The reconciler watches:

- `StorageClass` (filtered by provisioner) - opt-in changes.
- `Node` - filtered to events that add, remove, or change the configured
  group label.
- Managed `CSIStorageCapacity` - filtered to objects with the managed-by
  label, so external changes trigger reconvergence.

### Disabled by default

The controller is opt-in via `--enable-capacity-template` (Helm value
`cleanup.capacityTemplate.enabled`), since clusters that do not use Cluster
Autoscaler do not need it.

## Limitations

- The published capacity is a single per-StorageClass value applied to every
  group. If two groups using the same grouping label value need different
  template capacities, switch the grouping label to a finer-grained one
  (e.g. agent pool instead of SKU).
- The mitigation requires CA at the version that adds the
  `template-node=true` label (kubernetes/autoscaler#9702). Older CAs will
  not match these capacity objects, but they will also not be harmed by them.

## References

- [kubernetes/autoscaler#9700][autoscaler-9700] - scale-up regression with
  node-specific `CSIStorageCapacity`.
- [kubernetes/autoscaler#9702][autoscaler-9702] - template-node label
  mitigation.
- [Kubernetes storage capacity tracking][k8s-csc].

[autoscaler-9700]: https://github.com/kubernetes/autoscaler/issues/9700
[autoscaler-9702]: https://github.com/kubernetes/autoscaler/pull/9702
[k8s-csc]: https://kubernetes.io/docs/concepts/storage/storage-capacity/
