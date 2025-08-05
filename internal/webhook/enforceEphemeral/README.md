# PVC Controller

The PVC controller enforces the use of [generic ephemeral volumes] for volumes created on NVMe disks. Standard PVC create requests are denied unless the PVC includes the `localdisk.csi.acstor.io/accept-ephemeral-storage=true` annotation.

## Controller Behaviour

The PVC controller monitors PVC creation requests. If a request is not for a local-csi-driver PVC, it is allowed immediately.

A PVC is only permitted if it either has an `ownerReference` set to a Pod or includes the annotation `localdisk.csi.acstor.io/accept-ephemeral-storage=true`. The `ownerReference` is typically set when a Pod is created with one or more [generic ephemeral volumes], ensuring that the PVC is deleted when the Pod is removed. Alternatively, manually created PVCs must include the annotation to be accepted.

## Events
Kubernetes events are generated for each PVC creation request that is allowed, denied, or encounters an error.

## Metrics

The following metrics are published on the controller's endpoint:

```console
# HELP pvc_total Number of processed pvc requests, partitioned by operation and whether it was allowed.
# TYPE pvc_total counter
pvc_total{operation="create",allowed="true"} 12
pvc_total{operation="create",allowed="false"} 3

# HELP pvc_duration_seconds Distribution of the length of time to handle pvc requests.
# TYPE pvc_duration_seconds histogram
pvc_duration_seconds_bucket{le="0.005"} 0
pvc_duration_seconds_bucket{le="0.01"} 0
pvc_duration_seconds_bucket{le="0.025"} 0
pvc_duration_seconds_bucket{le="0.05"} 0
pvc_duration_seconds_bucket{le="0.1"} 0
pvc_duration_seconds_bucket{le="0.25"} 0
pvc_duration_seconds_bucket{le="0.5"} 0
pvc_duration_seconds_bucket{le="1"} 0
pvc_duration_seconds_bucket{le="2.5"} 0
pvc_duration_seconds_bucket{le="5"} 0
pvc_duration_seconds_bucket{le="10"} 0
pvc_duration_seconds_bucket{le="+Inf"} 0
pvc_duration_seconds_sum 0
pvc_duration_seconds_count 0
```

[generic ephemeral volumes]:
    https://kubernetes.io/docs/concepts/storage/ephemeral-volumes/#generic-ephemeral-volumes
