# local-csi-driver Helm Chart

This chart deploys:

- A node DaemonSet containing the driver and CSI sidecars
- A manager Deployment for admission webhooks and Released-PV cleanup
- CSI registration, RBAC, webhook, metrics, and certificate resources

The checked-in [`values.yaml`](values.yaml) file is the authoritative reference
for every value and default. To inspect the values for a published release,
run:

```sh
helm show values \
  oci://localcsidriver.azurecr.io/acstor/charts/local-csi-driver \
  --version <release>
```

## Install

Find the current version on the
[GitHub Releases page](https://github.com/Azure/local-csi-driver/releases/latest)
and substitute it without the `v` prefix:

```sh
helm install local-csi-driver \
  oci://localcsidriver.azurecr.io/acstor/charts/local-csi-driver \
  --version <release> \
  --namespace kube-system
```

Only one local-csi-driver release can be installed in a cluster because the
chart creates cluster-scoped CSI, webhook, and RBAC resources with fixed names.

## Uninstall

Delete workloads and storage resources that use the driver before uninstalling
the chart:

```sh
helm uninstall local-csi-driver --namespace kube-system
```

## Configuration

The table lists the configurable parameters in the current chart and their
default values. The checked-in [`values.yaml`](values.yaml) remains the
authoritative source.

<!-- markdownlint-disable MD033 -->
| Parameter | Description | Default |
| --- | --- | --- |
| `name` | Base name used for generated Kubernetes resources. | `csi-local` |
| `image.baseRepo` | Base repository prepended when an image repository begins with `/`. | `mcr.microsoft.com` |
| `image.driver.repository` | local-csi-driver container image repository. | `localcsidriver.azurecr.io/acstor/local-csi-driver` |
| `image.driver.tag` | local-csi-driver image tag. Uses the chart application version when unset. | Unset |
| `image.driver.pullPolicy` | local-csi-driver image pull policy. | `IfNotPresent` |
| `image.manager.repository` | local-csi-manager container image repository. | `localcsidriver.azurecr.io/acstor/local-csi-manager` |
| `image.manager.tag` | local-csi-manager image tag. Uses the chart application version when unset. | Unset |
| `image.manager.pullPolicy` | local-csi-manager image pull policy. | `IfNotPresent` |
| `image.csiProvisioner.repository` | external-provisioner container image repository. | `/oss/v2/kubernetes-csi/csi-provisioner` |
| `image.csiProvisioner.tag` | external-provisioner image tag. | `v5.2.0` |
| `image.csiProvisioner.pullPolicy` | external-provisioner image pull policy. | `IfNotPresent` |
| `image.csiResizer.repository` | external-resizer container image repository. | `/oss/v2/kubernetes-csi/csi-resizer` |
| `image.csiResizer.tag` | external-resizer image tag. | `v1.13.2` |
| `image.csiResizer.pullPolicy` | external-resizer image pull policy. | `IfNotPresent` |
| `image.nodeDriverRegistrar.repository` | node-driver-registrar container image repository. | `/oss/v2/kubernetes-csi/csi-node-driver-registrar` |
| `image.nodeDriverRegistrar.tag` | node-driver-registrar image tag. | `v2.16.0` |
| `image.nodeDriverRegistrar.pullPolicy` | node-driver-registrar image pull policy. | `IfNotPresent` |
| `daemonset.podSelector` | Pod selector labels for the DaemonSet. Uses chart defaults when empty. | `{}` |
| `daemonset.updateStrategy` | DaemonSet update strategy. | <code>type: RollingUpdate<br>rollingUpdate:<br>&nbsp;&nbsp;maxUnavailable: 10%</code> |
| `daemonset.nodeSelector` | Node selector used to limit the driver to eligible storage nodes. | `{}` |
| `daemonset.nodeAffinity` | Additional node affinity combined with the chart's OS and architecture requirements. | `{}` |
| `daemonset.tolerations` | Driver Pod tolerations. | <code>- effect: NoSchedule<br>&nbsp;&nbsp;operator: Exists<br>- effect: NoExecute<br>&nbsp;&nbsp;operator: Exists</code> |
| `daemonset.serviceAccount.annotations` | Annotations added to the driver service account. | `{}` |
| `raid.enabled` | **Experimental.** Create an mdadm RAID 0 device before configuring LVM. | `false` |
| `raid.volumeGroup` | Volume group created or reused by the RAID setup. Must match the StorageClass `volumeGroup` parameter when customized. | `containerstorage` |
| `diskSelection.addonPathPrefixes` | Device path prefixes appended to the built-in `/dev/nvme` prefix. | `[]` |
| `diskSelection.addonModels` | Device models appended to the built-in supported models. | `[]` |
| `diskSelection.addonTypes` | Device types appended to the built-in `disk` type. | `[]` |
| `cleanup.enabled` | Clean unused managed volume groups and physical volumes during driver shutdown. | `true` |
| `cleanup.lvGarbageCollection.enabled` | Enable event-driven LV cleanup after PV ownership moves to another node. | `true` |
| `cleanup.lvmOrphanCleanup.enabled` | Enable periodic orphan LV scanning. | `true` |
| `cleanup.lvmOrphanCleanup.interval` | Interval between periodic orphan scans. | `5m` |
| `cleanup.pvCleanup.enabled` | Enable manager cleanup of Released PVs whose topology nodes are unavailable. | `true` |
| `webhook.enforceEphemeral.enabled` | Enable validation of driver-backed PVC creation. | `true` |
| `webhook.hyperconverged.enabled` | Enable Pod affinity mutation and empty-volume availability recovery. | `true` |
| `webhook.service.port` | Webhook Service port. | `443` |
| `webhook.service.targetPort` | Manager webhook listener port. | `9443` |
| `webhook.service.type` | Kubernetes Service type for the webhook. | `ClusterIP` |
| `manager.serviceAccount.annotations` | Annotations added to the manager service account. | `{}` |
| `manager.deployment.replicas` | Number of manager replicas. | `2` |
| `manager.deployment.podSecurityContext` | Additional manager Pod security context. | `{}` |
| `manager.deployment.securityContext` | Additional manager container security context. | `{}` |
| `manager.deployment.nodeSelector` | Node selector for manager Pods. | `{}` |
| `manager.deployment.tolerations` | Manager Pod tolerations, including control-plane and `CriticalAddonsOnly` taints. | See `values.yaml` |
| `manager.deployment.affinity` | Manager affinity. Requires Linux amd64 or arm64, prefers AKS system nodes, and prefers hostname spreading. | See `values.yaml` |
| `manager.deployment.env` | Additional manager environment variables. | `[]` |
| `manager.deployment.extraArgs` | Additional manager command-line arguments. | `[]` |
| `manager.deployment.podLabels` | Additional manager Pod labels. | `{}` |
| `manager.deployment.podAnnotations` | Additional manager Pod annotations. | `{}` |
| `resources.driver` | Driver container resource requests and limits. | <code>limits:<br>&nbsp;&nbsp;memory: 600Mi<br>requests:<br>&nbsp;&nbsp;cpu: 10m<br>&nbsp;&nbsp;memory: 60Mi</code> |
| `resources.csiProvisioner` | external-provisioner resource requests and limits. | <code>limits:<br>&nbsp;&nbsp;memory: 500Mi<br>requests:<br>&nbsp;&nbsp;cpu: 10m<br>&nbsp;&nbsp;memory: 20Mi</code> |
| `resources.csiResizer` | external-resizer resource requests and limits. | <code>limits:<br>&nbsp;&nbsp;memory: 500Mi<br>requests:<br>&nbsp;&nbsp;cpu: 10m<br>&nbsp;&nbsp;memory: 20Mi</code> |
| `resources.nodeDriverRegistrar` | node-driver-registrar resource requests and limits. | <code>limits:<br>&nbsp;&nbsp;memory: 100Mi<br>requests:<br>&nbsp;&nbsp;cpu: 10m<br>&nbsp;&nbsp;memory: 20Mi</code> |
| `resources.manager` | Manager resource requests and limits. | <code>limits:<br>&nbsp;&nbsp;memory: 500Mi<br>requests:<br>&nbsp;&nbsp;cpu: 10m<br>&nbsp;&nbsp;memory: 64Mi</code> |
| `observability.metrics.enabled` | Create metrics RBAC resources. Metrics listeners remain configured independently. | `true` |
| `observability.manager.log.level` | Manager log verbosity. | `2` |
| `observability.manager.metrics.port` | Manager secure metrics port. | `8080` |
| `observability.manager.health.port` | Manager health and readiness port. | `8081` |
| `observability.manager.pprof.enabled` | Enable the manager pprof endpoint. | `false` |
| `observability.manager.pprof.port` | Manager pprof port. | `6060` |
| `observability.driver.log.level` | Driver log verbosity. | `2` |
| `observability.driver.metrics.port` | Driver metrics port. | `8080` |
| `observability.driver.health.port` | Driver health and readiness port. | `8081` |
| `observability.driver.pprof.enabled` | Enable the driver pprof endpoint. | `false` |
| `observability.driver.pprof.port` | Driver pprof port. | `6060` |
| `observability.driver.trace.endpoint` | OpenTelemetry collector address. An empty value disables tracing. | `""` |
| `observability.driver.trace.sampleRate` | Trace sample rate per million. Zero disables tracing. | `"1000000"` |
| `observability.csiProvisioner.log.level` | external-provisioner log verbosity. | `2` |
| `observability.csiProvisioner.http.port` | external-provisioner health and metrics port. | `8090` |
| `observability.csiResizer.log.level` | external-resizer log verbosity. | `2` |
| `observability.csiResizer.http.port` | external-resizer health and metrics port. | `8091` |
| `observability.nodeDriverRegistrar.log.level` | node-driver-registrar log verbosity. | `1` |
| `observability.nodeDriverRegistrar.http.port` | node-driver-registrar health and metrics port. | `8092` |
| `scalability.driver.workerThreads` | Driver CSI worker count. | `100` |
| `scalability.driver.kubeApi.qps` | Driver Kubernetes API client QPS. | `100` |
| `scalability.driver.kubeApi.burst` | Driver Kubernetes API client burst. | `200` |
| `scalability.csiProvisioner.workerThreads` | external-provisioner worker count. | `100` |
| `scalability.csiProvisioner.kubeApi.qps` | external-provisioner Kubernetes API client QPS. | `100` |
| `scalability.csiProvisioner.kubeApi.burst` | external-provisioner Kubernetes API client burst. | `200` |
| `scalability.manager.kubeApi.qps` | Manager Kubernetes API client QPS. | `100` |
| `scalability.manager.kubeApi.burst` | Manager Kubernetes API client burst. | `200` |
<!-- markdownlint-enable MD033 -->

The driver binary has its own command-line defaults. Values supplied by the
chart override those defaults. For example, the binary orphan scan interval is
`30m`, while the chart deploys `5m`.

## RAID configuration

By default, the driver adds eligible devices to an LVM volume group. When that
volume group contains multiple physical volumes, each CSI volume is created as
an LVM RAID0 LV with one stripe per physical volume.

`raid.enabled=true` selects a different experimental layout. A privileged init
container:

1. Enters the host namespaces with `nsenter`.
2. Installs mdadm when necessary.
3. Attempts to assemble an existing array.
4. Creates `/dev/md0` from two or more unused NVMe devices when no array
   exists.
5. Uses a single device directly when only one eligible device exists.
6. Creates the configured LVM volume group on the resulting device.

> [!WARNING]
> RAID 0 has no redundancy. Failure of one member loses the array. Migrating
> between mdadm and direct-LVM layouts is not supported and may require manual
> data destruction and node repair.

Enable mdadm RAID:

```sh
helm install local-csi-driver \
  oci://localcsidriver.azurecr.io/acstor/charts/local-csi-driver \
  --version <release> \
  --namespace kube-system \
  --set raid.enabled=true
```

To use a custom volume group:

```sh
helm install local-csi-driver \
  oci://localcsidriver.azurecr.io/acstor/charts/local-csi-driver \
  --version <release> \
  --namespace kube-system \
  --set raid.enabled=true \
  --set raid.volumeGroup=my-custom-vg
```

The StorageClass must select the same group:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: local-raid
provisioner: localdisk.csi.acstor.io
parameters:
  volumeGroup: my-custom-vg
reclaimPolicy: Delete
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
```

## Disk selection

The driver uses built-in device filters:

- Path prefix: `/dev/nvme`
- Models: `Microsoft NVMe Direct Disk`,
  `Microsoft NVMe Direct Disk v2`, and
  `Amazon EC2 NVMe Instance Storage`
- Type: `disk`

Addon values expand the built-in filters:

```yaml
diskSelection:
  addonPathPrefixes:
    - /dev/custom-nvme
  addonModels:
    - Contoso NVMe Disk
  addonTypes:
    - loop
```

A device must match a path prefix, model, and type. Within each category, it can
match a built-in or addon value. Addons never replace or remove built-in
defaults.

Disk selection is process-wide and is not configured through StorageClass
parameters. See [Disk Selection](../../docs/design/disk-selection.md) for the
selection and safety model.

## Related documentation

- [User Guide](../../docs/user-guide.md) - install and use the driver
- [Architecture](../../docs/architecture.md) - understand component and request
  flows
- [Troubleshooting](../../docs/troubleshooting.md) - diagnose deployment and
  storage failures
