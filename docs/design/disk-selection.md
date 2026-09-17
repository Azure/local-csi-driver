# local-csi-driver Disk Selection

local-csi-driver discovers eligible local block devices and adds unformatted
devices to an LVM volume group. Disk selection is configured for the driver
process, not per StorageClass.

## Selection model

A device must match all three selection categories:

- Path prefix
- Device model
- Device type

Within each category, matching any configured value is sufficient.

The built-in defaults are:

| Category | Values |
| --- | --- |
| Path prefix | `/dev/nvme` |
| Model | `Microsoft NVMe Direct Disk`, `Microsoft NVMe Direct Disk v2`, `Amazon EC2 NVMe Instance Storage` |
| Type | `disk` |

Model and type comparisons are case-insensitive. Path matching uses a
case-sensitive prefix comparison.

After a device matches the filter, the probe checks whether it is already
formatted. Formatted devices are not returned as available devices. Failure to
determine whether a matching device is formatted causes the scan to fail
rather than treating the device as safe to use.

## Configuration

The driver accepts additive command-line values:

| Driver flag | Helm value |
| --- | --- |
| `--disk-path-prefixes` | `diskSelection.addonPathPrefixes` |
| `--disk-models` | `diskSelection.addonModels` |
| `--disk-types` | `diskSelection.addonTypes` |

Addon values are appended to the built-in defaults. Empty values and exact
duplicates are ignored. The built-in values cannot currently be removed or
replaced.

For example:

```yaml
diskSelection:
  addonPathPrefixes:
    - /dev/custom-nvme
  addonModels:
    - Contoso NVMe Disk
  addonTypes:
    - loop
```

This configuration expands each category independently. A selected device can,
for example, use the addon path while matching a built-in model and type.

The chart joins each list into a comma-separated driver argument. Changing the
values updates the DaemonSet and causes the restarted driver Pods to use the new
filter.

## StorageClass relationship

Disk-selection values are not StorageClass parameters. Every StorageClass
handled by one driver instance uses the same process-wide filter.

The StorageClass can select a volume group with the `volumeGroup` parameter:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: local
provisioner: localdisk.csi.acstor.io
parameters:
  volumeGroup: containerstorage
reclaimPolicy: Delete
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
```

The selected volume group must exist or be created from devices discovered by
the driver. With mdadm RAID enabled, `raid.volumeGroup` and the StorageClass
`volumeGroup` parameter must refer to the same group.

## Limitations and safety

- Filters are additive and cannot exclude one device that otherwise matches.
- Disk selection is process-wide, so two StorageClasses cannot use different
  device filters on the same driver Pod.
- A path prefix can match more devices than intended. Review the complete
  device inventory before adding broad prefixes such as `/dev/sd`.
- The filter does not establish that data on a device is disposable. It only
  checks the device attributes and whether the device appears formatted.
- Existing volume groups can outlive a filter change. Removing an addon value
  does not remove devices that were already initialized as LVM physical
  volumes.
- Built-in defaults are maintained in `internal/pkg/probe/filter.go`; chart
  values intentionally contain only additions.

## Related documentation

- [Architecture](../architecture.md) - storage lifecycle and privilege model
- [User Guide](../user-guide.md) - configure disk selection
- [Helm chart reference](../../charts/latest/README.md) - current values and
  defaults
