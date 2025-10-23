# User Guide

This guide provides step-by-step instructions for setting up and using the
local-csi-driver, including installing Helm, creating a StorageClass, and
deploying a StatefulSet.

## Prerequisites

Before proceeding, ensure you have the following installed:

- Kubernetes cluster (v1.11.3+)
- Kubectl (v1.11.3+)
- Helm (v3.16.4+)

## Installing Helm

To install Helm, please follow the official [Helm installation guide](https://helm.sh/docs/intro/install/).

## Installing local-csi-driver

Find the latest release by navigating to
<https://github.com/Azure/local-csi-driver/releases/latest>.

Substitute the release name (without the 'v' prefix) in the Helm install command
below:

   ```sh
   helm install local-csi-driver oci://localcsidriver.azurecr.io/acstor/charts/local-csi-driver --version <release> --namespace kube-system
   ```

Only one instance of local-csi-driver can be installed per cluster.

Helm chart values are documented in: [Helm chart
README](../charts/latest/README.md).

### Installing with RAID Support

For improved performance, you can enable automatic RAID 0 array creation across
multiple NVMe devices:

```sh
helm install local-csi-driver oci://localcsidriver.azurecr.io/acstor/charts/local-csi-driver --version <release> --namespace kube-system --set raid.enabled=true
```

When RAID is enabled, an init container will automatically:

- Detect unused NVMe devices on each node
- Create a RAID 0 array using mdadm (if 2+ devices are available)
- Create an LVM volume group on the RAID device

For more details on RAID configuration, see the [Helm chart README](../charts/latest/README.md#raid-configuration).

## Creating a StorageClass

To create a StorageClass for the local-csi-driver, apply the following YAML:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: local
provisioner: localdisk.csi.acstor.io
reclaimPolicy: Delete
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
```

### StorageClass Parameters

The StorageClass supports the following optional parameters:

- `volumeGroup`: Specifies a custom LVM volume group name. If not specified,
  defaults to `containerstorage`.

Example with custom volume group:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: local-custom-vg
provisioner: localdisk.csi.acstor.io
reclaimPolicy: Delete
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
parameters:
  volumeGroup: "my-custom-vg"
```

Save this YAML to a file (e.g., `storageclass.yaml`) and apply it:

```sh
kubectl apply -f storageclass.yaml
```

> [!TIP]
> To maximize performance, local-csi-driver automatically stripes data
> across all available local NVMe disks on a per-VM basis. Striping is a
> technique where data is divided into small chunks and evenly written across
> multiple disks simultaneously, which increases throughput and improves overall
> I/O performance. This behavior is enabled by default and cannot be disabled.

### StorageClass Parameters

The local-csi-driver supports several optional parameters in the StorageClass:

| Parameter | Description | Values | Default |
|-----------|-------------|---------|---------|
| `localdisk.csi.acstor.io/failover-mode` | Controls pod scheduling behavior in hyperconverged setups | `availability`, `durability` | Not set (defaults to `availability`) |
| `localdisk.csi.acstor.io/limit` | Maximum storage per node (bytes) | Number | Optional |

#### Failover Modes

When using hyperconverged storage (storage and compute on the same nodes), the `failover-mode` parameter controls how pods are scheduled:

- **availability**: Uses preferred node affinity. Pods prefer to be scheduled on nodes with local storage but can be placed elsewhere if storage nodes are unavailable. This prioritizes pod availability over data persistance. New empty volume will be provisioned on the new failover node.

- **durability**: Uses required node affinity. Pods must be scheduled on nodes with local storage and will remain pending if storage nodes are unavailable. This ensures data persistance when possible but may affect pod availability.

Example StorageClass with failover mode:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: local-availability
provisioner: localdisk.csi.acstor.io
parameters:
  localdisk.csi.acstor.io/failover-mode: "availability"
reclaimPolicy: Delete
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
```

## Creating a StatefulSet

To create a StatefulSet using the StorageClass, apply the following YAML:

```yaml
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: statefulset-lcd-lvm
  labels:
    app: busybox
spec:
  podManagementPolicy: Parallel
  replicas: 10
  template:
    metadata:
      labels:
        app: busybox
    spec:
      nodeSelector:
        "kubernetes.io/os": linux
      containers:
        - name: statefulset-lcd
          image: mcr.microsoft.com/azurelinux/busybox:1.36
          command:
            - "/bin/sh"
            - "-c"
            - set -euo pipefail; trap exit TERM; while true; do date -u +"%Y-%m-%dT%H:%M:%SZ" | tee -a /mnt/lcd/outfile; sleep 1; done
          volumeMounts:
            - name: ephemeral-storage
              mountPath: /mnt/lcd
      volumes:
        - name: ephemeral-storage
          ephemeral:
            volumeClaimTemplate:
              spec:
                resources:
                  requests:
                    storage: 10Gi
                volumeMode: Filesystem
                accessModes:
                  - ReadWriteOnce
                storageClassName: local
  updateStrategy:
    type: RollingUpdate
  selector:
    matchLabels:
      app: busybox
```

Save this YAML to a file (e.g., `statefulset.yaml`) and apply it:

```sh
kubectl apply -f statefulset.yaml
```

## Guidance on Ephemeral Annotation

By default, the local-csi-driver only permits the use of generic ephemeral
volumes. If you want to use a persistent volume claim that is not linked to the
lifecycle of the pod, you need to add the
`localdisk.csi.acstor.io/accept-ephemeral-storage: "true"` annotation to the
PersistentVolumeClaim. Note: The data on the volume is local to the node and
will be lost if the node is deleted or the pod is moved to another node.

```yaml
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: statefulset-lcd-lvm-annotation
  labels:
    app: busybox
spec:
  podManagementPolicy: Parallel  # default is OrderedReady
  serviceName: statefulset-lcd
  replicas: 10
  template:
    metadata:
      labels:
        app: busybox
    spec:
      nodeSelector:
        "kubernetes.io/os": linux
      containers:
        - name: statefulset-lcd
          image: mcr.microsoft.com/azurelinux/busybox:1.36
          command:
            - "/bin/sh"
            - "-c"
            - set -euo pipefail; trap exit TERM; while true; do date -u +"%Y-%m-%dT%H:%M:%SZ" >> /mnt/lcd/outfile; sleep 1; done
          volumeMounts:
            - name: persistent-storage
              mountPath: /mnt/lcd
  updateStrategy:
    type: RollingUpdate
  selector:
    matchLabels:
      app: busybox
  volumeClaimTemplates:
    - metadata:
        name: persistent-storage
        annotations:
          localdisk.csi.acstor.io/accept-ephemeral-storage: "true"
      spec:
        accessModes: ["ReadWriteOnce"]
        storageClassName: local
        resources:
          requests:
            storage: 10Gi
```

## Uninstalling local-csi-driver

To uninstall local-csi-driver, apply the following steps:

1. Clean up storage resources. You must first delete all PersistentVolumeClaims
   and/or PersistentVolumes.

2. Delete your storage class. Run the following command:

   ```sh
   kubectl delete storageclass $storageClassName
   ```

3. Uninstall local-csi-driver. Run the following command:

   ```sh
   helm uninstall local-csi-driver -n kube-system
   ```
