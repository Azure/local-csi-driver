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

To install local-csi-driver using Helm:

   ```sh
   helm install local-csi-driver oci://localcsidriver.azurecr.io/local-csi-driver/local-csi-driver --version 0.0.1-latest --namespace kube-system --wait --atomic
   ```

Only one instance of local-csi-driver can be installed per cluster.

Helm chart values are documented in: [Helm chart README](./charts/latest/README.md).

## Creating a StorageClass

To create a StorageClass for the local-csi-driver, apply the following YAML:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: local
parameters:
  local.csi.azure.com/vg: containerstorage
provisioner: local.csi.azure.com
reclaimPolicy: Delete
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
```

Save this YAML to a file (e.g., `storageclass.yaml`) and apply it:

```sh
kubectl apply -f storageclass.yaml
```

## Creating a StatefulSet

To create a StatefulSet using the StorageClass, apply the following YAML:

```yaml
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: statefulset-cns-lvm
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
        - name: statefulset-cns
          image: mcr.microsoft.com/azurelinux/busybox:1.36
          command:
            - "/bin/sh"
            - "-c"
            - set -euo pipefail; trap exit TERM; while true; do echo $(date -u +"%Y-%m-%dT%H:%M:%SZ") | tee -a /mnt/cns/outfile; sleep 1; done
          volumeMounts:
            - name: ephemeral-storage
              mountPath: /mnt/cns
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
`local.csi.azure.com/accept-ephemeral-storage: "true"` annotation to the
PersistentVolumeClaim. Note: The data on the volume is local to the node and will
be lost if the node is deleted or the pod is moved to another node.

```yaml
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: statefulset-cns-lvm-annotation
  labels:
    app: busybox
spec:
  podManagementPolicy: Parallel  # default is OrderedReady
  serviceName: statefulset-cns
  replicas: 10
  template:
    metadata:
      labels:
        app: busybox
    spec:
      nodeSelector:
        "kubernetes.io/os": linux
      containers:
        - name: statefulset-cns
          image: mcr.microsoft.com/azurelinux/busybox:1.36
          command:
            - "/bin/sh"
            - "-c"
            - set -euo pipefail; trap exit TERM; while true; do echo $(date) >> /mnt/cns/outfile; sleep 1; done
          volumeMounts:
            - name: persistent-storage
              mountPath: /mnt/cns
  updateStrategy:
    type: RollingUpdate
  selector:
    matchLabels:
      app: busybox
  volumeClaimTemplates:
    - metadata:
        name: persistent-storage
        annotations:
          local.csi.azure.com/accept-ephemeral-storage: "true"
      spec:
        accessModes: ["ReadWriteOnce"]
        storageClassName: local
        resources:
          requests:
            storage: 10Gi
```
