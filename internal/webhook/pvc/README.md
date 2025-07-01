# PVC Controller

The PVC controller ensures that volumes created in an ephemeral pool (local SSD
or NVme) where replication has not been enabled, are only created using [generic
ephemeral volumes]. Standard PVCs create requests will be denied, unless
`localdisk.csi.acstor.io/accept-ephemeral-storage=true` annotation is added to
the PVCs.

## Controller Behaviour

The PVC controller watches for PVC create requests. When a request is received,
it will try to rule out non-local-csi-driver PVCs or PVCs from non-ephemeral
pools and allow them immediately. For PVCs from ephemeral pools, it allows those
with multiple replicas.

For unreplicated PVCs from ephemeral pools, it only allows them if they have an
ownerReference set to a Pod or an annotation indicating ephemeral storage is
acceptable. The former should only happen when a Pod is created with one or more
[generic ephemeral volumes]. The Pod is set as the PVC owner, so that when the
Pod is deleted, its PVCs are too. Otherwise, the annotation
`localdisk.csi.acstor.io/accept-ephemeral-storage=true` should be added to PVCs
to be allowed.

Manual PVC creation without a required annotation for unreplicated volumes from
ephemeral pools will be denied and and error sent back immediately.

## Events

Kubernetes events are sent whenever a PVC create request is allowed, denied or
errored. This will likely be too verbose, and we can scale back to only sending
events for ephemeral pools.

## Metrics

The following metrics are published on the Capacity Provisioner's metrics
endpoint:

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
