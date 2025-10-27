# local-csi-driver Helm Chart

## Prerequisites

- [Install Helm](https://helm.sh/docs/intro/quickstart/#install-helm)

## Install

Find the latest release by navigating to
<https://github.com/Azure/local-csi-driver/releases/latest>.

Substitute the release name (without the 'v' prefix) in the Helm install command
below:

```console
helm install local-csi-driver oci://localcsidriver.azurecr.io/acstor/charts/local-csi-driver --version <release> --namespace kube-system
```

## Uninstall

```console
helm uninstall local-csi-driver --namespace kube-system
```

## Configuration

This table list the configurable parameters of the latest Local CSI Driver chart
and their default values.

<!-- markdownlint-disable MD033 -->
| Parameter                                     | Description                                                                        | Default                                                                                                                  |
| --------------------------------------------- | ---------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| `name`                                        | Name used for creating resource.                                                   | `csi-local`                                                                                                              |
| `image.baseRepo`                              | Base repository of container images.                                               | `mcr.microsoft.com`                                                                                                      |
| `image.driver.repository`                     | local-csi-driver container image.                                                  | `/acstor/local-csi-driver`                                                                                               |
| `image.driver.tag`                            | local-csi-driver container image tag. Uses chart version when unset (recommended). |                                                                                                                          |
| `image.driver.pullPolicy`                     | local-csi-driver image pull policy.                                                | `IfNotPresent`                                                                                                           |
| `image.csiProvisioner.repository`             | csi-provisioner container image.                                                   | `/oss/kubernetes-csi/csi-provisioner`                                                                                    |
| `image.csiProvisioner.tag`                    | csi-provisioner container image tag.                                               | `v5.2.0`                                                                                                                 |
| `image.csiProvisioner.pullPolicy`             | csi-provisioner image pull policy.                                                 | `IfNotPresent`                                                                                                           |
| `image.csiResizer.repository`                 | csi-resizer container image.                                                       | `/oss/kubernetes-csi/csi-resizer`                                                                                        |
| `image.csiResizer.tag`                        | csi-resizer container image tag.                                                   | `v1.13.2`                                                                                                                |
| `image.csiResizer.pullPolicy`                 | csi-resizer image pull policy.                                                     | `IfNotPresent`                                                                                                           |
| `image.nodeDriverRegistrar.repository`        | csi-node-driver-registrar container image.                                         | `/oss/kubernetes-csi/csi-node-driver-registrar`                                                                          |
| `image.nodeDriverRegistrar.tag`               | csi-node-driver-registrar container image tag.                                     | `v2.13.0`                                                                                                                |
| `image.nodeDriverRegistrar.pullPolicy`        | csi-node-driver-registrar image pull policy.                                       | `IfNotPresent`                                                                                                           |
| `daemonset.nodeSelector`                      | Node selector for the DaemonSet. If empty, all nodes are selected.                 |                                                                                                                          |
| `daemonset.tolerations`                       | Tolerations for the DaemonSet. If empty, no tolerations are applied.               | <code>- effect: NoSchedule<br>&nbsp;&nbsp;operator: Exists<br>- effect: NoExecute<br>&nbsp;&nbsp;operator: Exists</code> |
| `daemonset.serviceAccount.annotations`        | Annotations for the service account. If empty, no annotations are applied.         |                                                                                                                          |
| `raid.enabled`                                | Enables RAID 0 setup using mdadm. When enabled, an init container will combine unused NVMe devices into a RAID 0 array and create an LVM volume group on it. | `false`                                                                                                                  |
| `raid.volumeGroup`                            | The volume group name to create on the RAID device. Must match the `volumeGroup` parameter in StorageClass if using a custom name. | `containerstorage`                                                                                                       |
| `cleanup.enabled`                             | Cleanup volume groups and physical volumes on pod termination if logical volumes are not in use. | `true`                                                                                                                   |
| `webhook.enforceEphemeral.enabled`            | Enables the enforce ephemeral PVC validation webhook.                                      | `true`                                                                                                                   |
| `webhook.hyperconverged.enabled`              | Enables the hyperconverged webhook.                                                | `true`                                                                                                                   |
| `webhook.service.port`                        | The webhook service's port.                                                        | `443`                                                                                                                    |
| `webhook.service.targetPort`                  | The target port for the webhook service.                                           | `9443`                                                                                                                   |
| `webhook.service.type`                        | The type of the webhook service. Can be ClusterIP, NodePort, or LoadBalancer.      | `ClusterIP`                                                                                                              |
| `resources.driver`                            | local-csi-driver resource configuration.                                           | <code>limits:<br>&nbsp;&nbsp;memory: 600Mi<br>requests:<br>&nbsp;&nbsp;cpu: 10m<br>&nbsp;&nbsp;memory: 60Mi</code>       |
| `resources.csiProvisioner`                    | csi-provisioner resource configuration.                                            | <code>limits:<br>&nbsp;&nbsp;memory: 100Mi<br>requests:<br>&nbsp;&nbsp;cpu: 10m<br>&nbsp;&nbsp;memory: 20Mi</code>       |
| `resources.csiResizer`                        | csi-resizer resource configuration.                                                | <code>limits:<br>&nbsp;&nbsp;memory: 500Mi<br>requests:<br>&nbsp;&nbsp;cpu: 10m<br>&nbsp;&nbsp;memory: 20Mi</code>       |
| `resources.nodeDriverRegistrar`               | csi-node-driver-registrar resource configuration.                                  | <code>limits:<br>&nbsp;&nbsp;memory: 100Mi<br>requests:<br>&nbsp;&nbsp;cpu: 10m<br>&nbsp;&nbsp;memory: 20Mi</code>       |
| `observability.metrics.enabled`               | Toggles metrics rbac rule creation. May be expanded in the future.                 | `true`                                                                                                                   |
| `observability.driver.log.level`              | local-csi-driver log level.                                                        | `2`                                                                                                                      |
| `observability.driver.metrics.port`           | local-csi-driver metrics port.                                                     | `8080`                                                                                                                   |
| `observability.driver.health.port`            | local-csi-driver health port.                                                      | `8081`                                                                                                                   |
| `observability.driver.trace.endpoint`         | The address to send traces to. Disables tracing if not set.                        |                                                                                                                          |
| `observability.driver.trace.sampleRate`       | Sample rate per million. 0 to disable tracing, 1000000 to trace everything.        | `1000000`                                                                                                                |
| `observability.csiProvisioner.log.level`      | csi-provisioner log level.                                                         | `2`                                                                                                                      |
| `observability.csiProvisioner.http.port`      | csi-provisioner health and metrics port.                                           | `8090`                                                                                                                   |
| `observability.csiResizer.log.level`          | csi-resizer log level.                                                             | `2`                                                                                                                      |
| `observability.csiResizer.http.port`          | csi-resizer health and metrics port.                                               | `8091`                                                                                                                   |
| `observability.nodeDriverRegistrar.log.level` | csi-node-driver-registrar log level.                                               | `2`                                                                                                                      |
| `observability.nodeDriverRegistrar.http.port` | csi-node-driver-registrar health and metrics port.                                 | `8092`                                                                                                                   |
<!-- markdownlint-enable MD033 -->

## RAID Configuration

The local-csi-driver supports automatic RAID 0 array creation via mdadm for
improved performance when multiple NVMe devices are available on a node. This
feature is controlled by the `raid.enabled` parameter.

### Enabling RAID

To enable RAID 0 setup, install the chart with:

```console
helm install local-csi-driver oci://localcsidriver.azurecr.io/acstor/charts/local-csi-driver \
  --version <release> \
  --namespace kube-system \
  --set raid.enabled=true
```

### How RAID Works

When `raid.enabled=true`, an init container runs on each node before the CSI
driver starts:

1. **Device Discovery**: Scans for unused NVMe devices (devices not mounted, not
   in use by LVM, and without existing RAID metadata)
2. **Single Device**: If only one unused device is found, it creates an LVM
   volume group directly on that device
3. **Multiple Devices**: If two or more unused devices are found:
   - Installs `mdadm` if not already present (supports tdnf and apt-get package managers)
   - Creates a RAID 0 array at `/dev/md0` using all unused devices
   - Saves the RAID configuration to `/etc/mdadm/mdadm.conf`
   - Creates an LVM physical volume on the RAID device
   - Creates an LVM volume group on the RAID device

### Custom Volume Group Name

By default, the volume group is named `containerstorage`. To use a custom name:

```console
helm install local-csi-driver oci://localcsidriver.azurecr.io/acstor/charts/local-csi-driver \
  --version <release> \
  --namespace kube-system \
  --set raid.enabled=true \
  --set raid.volumeGroup=my-custom-vg
```

**Important**: If you use a custom volume group name, you must also specify it
in your StorageClass:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: local-raid
provisioner: localdisk.csi.acstor.io
reclaimPolicy: Delete
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
parameters:
  volumeGroup: "my-custom-vg"
```

### RAID vs LVM Striping

- **RAID disabled** (default): The CSI driver uses LVM's built-in RAID 0
  striping across multiple devices
- **RAID enabled**: Creates a mdadm RAID 0 array first, then LVM on top of it

mdadm RAID 0 provides better performance in some scenarios and may be preferred
for certain workloads.

### Requirements

- Two or more unused NVMe devices on the node (or one device for single-disk setup)
- Node must support either `tdnf` or `apt-get` package manager for mdadm installation
- Sufficient privileges for the init container (runs as root with privileged mode)

## Troubleshooting

See [Troubleshooting](https://github.com/Azure/local-csi-driver/blob/main/docs/troubleshooting.md).
