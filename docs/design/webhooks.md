# local-csi-driver Webhooks Design Document

## Design

local-csi-driver defines two webhooks to enhance user experience and reduce
potential user errors.

1. **Validation Webhook**: This webhook validates PersistentVolumeClaim (PVC)
resources to ensure that only generic ephemeral storage is utilized, unless the
user explicitly permits the creation of persistent volumes via the
**"localdisk.csi.acstor.io/accept-ephemeral-storage"** annotation. This process
ensures that users understand that the provisioned volumes will share the
lifecycle of the node, and true persistence cannot be guaranteed.

2. **Mutation Webhook**: Also known as the hyperconverged webhook, this feature
modifies workload Pods to incorporate node affinity rules, ensuring that Pods
are scheduled on the same node as the PersistentVolume (PV). The driver
supports this behavior in two ways: through the webhook or by directly setting
node affinity on the PV. However, since the node affinity of
the PV is immutable, if the node is deleted, the PV resources become unusable
and require manual cleanup. By managing node affinity through the webhook, we
can avoid adding it directly to the PV and instead apply it to the workload.
This method enables the workload to be rescheduled in the event of node
deletion.
While this may result in the loss of data within the PV, it ensures that
the workload remains operational and can continue to write new data.

## Configuration

Both of the webhooks are optional and can be enabled or disabled via the
appropriate values for `.Values.webhook` in the Helm chart.

## Validation (EnforceEmphemeral) Webhook

The validation webhook serves a critical role in ensuring that users fully
understand the implications of utilizing local storage for their workloads. By
default, this webhook restricts the creation of volumes to generic ephemeral
storage. This means that any volumes provisioned by the driver will share the
lifecycle of the pod, rendering them unable to outlive the node on which they
reside.

To provide flexibility, users can opt to create persistent volumes by including
the annotation `localdisk.csi.acstor.io/accept-ephemeral-storage: "true"` in their
PersistentVolumeClaims (PVCs). This annotation signifies that the user
acknowledges the potential risks of data loss associated with local storage and
accepts the consequences of its use.

### Example

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: my-pvc
  annotations:
    localdisk.csi.acstor.io/accept-ephemeral-storage: "true" # Optional, allows creation of persistent volumes
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 10Gi
  storageClassName: local
```

## Mutation (Hyperconverged) Webhook

Since the driver is designed to work with local storage, it is essential to
ensure that workloads are scheduled on the same node as the
PersistentVolumeClaim. Driver supports this behavior in two ways: by directly
setting node affinity on the PersistentVolume (PV) or through the webhook.

### PV Node Affinity

Hyperconvergence of PVC and the workload can be achieved by setting node
affinity on the PersistentVolume (PV) to a specific node. This ensures that the
PV can only bind to one node where it is provisioned, and the workload Pods will
be scheduled on the same node as the PV.

This approach is straightforward and works well for most use cases. However, it
has a significant limitation: the node affinity of the PV is immutable. This
means that if the node is deleted, the PV resources become unusable and to
unblock the workloads, manual cleanup of PV resources is required. This is why
the mutation webhook is provided as an alternative approach for use cases where
cluster administrators want to avoid manual cleanup.

### Example of PV with Node Affinity

```yaml
apiVersion: v1
kind: PersistentVolume
metadata:
  finalizers:
    - external-provisioner.volume.kubernetes.io/finalizer
    - kubernetes.io/pv-protection
  name: pvc-d6efb13d-707f-4876-8622-ea7bf2399c14
  [...]
spec:
  accessModes:
    - ReadWriteOnce
  capacity:
    storage: 10Gi
  claimRef:
    apiVersion: v1
    kind: PersistentVolumeClaim
    [...]
  csi:
    driver: localdisk.csi.acstor.io
    volumeAttributes:
      localdisk.csi.acstor.io/selected-initial-node: nvme-node-0
    volumeHandle: containerstorage#pvc-d6efb13d-707f-4876-8622-ea7bf2399c14
  nodeAffinity:
    required:
      nodeSelectorTerms:
        - matchExpressions:
            - key: topology.localdisk.csi.acstor.io/node
              operator: In
              values:
                - nvme-node-0
  persistentVolumeReclaimPolicy: Delete
  storageClassName: local
  volumeMode: Filesystem
 ```

### Webhook Approach

When the webhook is enabled, instead of setting node affinity on the
PersistentVolume (PV), the driver adds a
`localdisk.csi.acstor.io/selected-initial-node` parameter to the volume context of
the PV. This parameter is later used by the mutation webhook to modify the
workload Pods to include preferred node affinity rules to the specified node
ensuring that the workload and the PV are scheduled on the same node.

In the event of a node deletion, the workload Pods can be seamlessly rescheduled
to a different node. During this process, the PersistentVolume (PV) will be
reprovisioned on the new node as a blank volume, enabling the workload to
continue functioning and writing new data. When such failovers occur, the PV
resources will be annotated with `"localdisk.csi.acstor.io/selected-node"`
information, which will later be used by the hyperconverged webhook to update
the node affinity of the workload on future failovers.

### Example of PV with Webhook Node Affinity

```yaml
apiVersion: v1
kind: PersistentVolume
metadata:
  annotations:
    localdisk.csi.acstor.io/selected-node: nvme-node-1
  finalizers:
    - external-provisioner.volume.kubernetes.io/finalizer
    - kubernetes.io/pv-protection
  name: pvc-d6efb13d-707f-4876-8622-ea7bf2399c14
  [...]
spec:
  accessModes:
    - ReadWriteOnce
  capacity:
    storage: 10Gi
  claimRef:
    apiVersion: v1
    kind: PersistentVolumeClaim
    [...]
  csi:
    driver: localdisk.csi.acstor.io
    volumeAttributes:
      localdisk.csi.acstor.io/selected-initial-node: nvme-node-0
      [...]
    volumeHandle: containerstorage#pvc-d6efb13d-707f-4876-8622-ea7bf2399c14
  persistentVolumeReclaimPolicy: Delete
  storageClassName: local
  volumeMode: Filesystem
```

When mutating hyperconverged webhook is disabled, driver will manage
hyperconvergence through the PersistentVolume (PV) node affinity.
