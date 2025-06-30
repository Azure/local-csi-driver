# local-csi-driver Disk Selection

## Scenario Definition

In the local-csi-driver, we create an LVM volume group and LVM physical volumes
for the NVMe devices on the node. From that created volume group, we dynamically
provision LVM logical volumes.

Currently, disk selection is hard coded to look for disks that satisfy all the
following criteria:

- **Path prefix** for the disk is `/dev/nvme`
- **Model** is either `Microsoft NVMe Direct Disk` or
  `Microsoft NVMe Direct Disk v2`
- **Disk type** is `disk` (this can also be something like `loop` for loop devices)

When a volume is provisioned, we ensure that all disks matching the filter are
LVM physical volumes. We also ensure that the volume group is created with all
of the physical volumes.

We would like to make this more flexible, allowing users to specify their own
filters, so they can ensure that local-csi-driver picks up only the disks they
want and excludes those they do not want.

## Design

Currently, we allow customization of the volume group name through the
StorageClass parameter `localdisk.csi.acstor.io/vg`. For example:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: local
provisioner: localdisk.csi.acstor.io
parameters:
  localdisk.csi.acstor.io/vg: containerstorage
reclaimPolicy: Delete
volumeBindingMode: WaitForFirstConsumer
```

We will add three new parameters to the storage class to allow users to
customize the disk selection:

- `localdisk.csi.acstor.io/disk-path-prefixes`: The prefix of the disk path. For
  example, `/dev/nvme`.
- `localdisk.csi.acstor.io/disk-models`: The model of the disk. For example,
  `Microsoft NVMe Direct Disk`.
- `localdisk.csi.acstor.io/disk-types`: The type of the disk. For example, `disk`.

These parameters will be used to filter the disks that are selected for
provisioning. The driver will only select disks that match all of the
specified parameters. If a parameter is not specified, the driver will use the
default value. **The user is not required to specify any of the parameters.** Our
defaults will work for NVMe disks found on Azure VMs.

Comma-separated values will be supported for the parameters. For example, if
the user specifies `localdisk.csi.acstor.io/disk-path-prefixes: /dev/nvme,/dev/sda`,
the driver will select disks that have either `/dev/nvme` or `/dev/sda` as the
path prefix.

| Parameter                                   | Description                 | Default Value                                              |
|----------------------------------------------|----------------------------|------------------------------------------------------------|
| `localdisk.csi.acstor.io/vg`                     | Name of the volume group   | `containerstorage`                                         |
| `localdisk.csi.acstor.io/disk-path-prefixes`     | Prefix of the disk path    | `/dev/nvme`                                                |
| `localdisk.csi.acstor.io/disk-models`            | Model of the disk          | `Microsoft NVMe Direct Disk,Microsoft NVMe Direct Disk v2` |
| `localdisk.csi.acstor.io/disk-types`             | Type of the disk           | `disk`                                                     |

Example with all parameters:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: local
provisioner: localdisk.csi.acstor.io
parameters:
  localdisk.csi.acstor.io/vg: containerstorage
  localdisk.csi.acstor.io/disk-path-prefixes: /dev/nvme,/dev/sda
  localdisk.csi.acstor.io/disk-models: Microsoft NVMe Direct Disk,Microsoft NVMe Direct Disk v2
  localdisk.csi.acstor.io/disk-types: disk
reclaimPolicy: Delete
volumeBindingMode: WaitForFirstConsumer
```

## Pain Points

- The user must ensure that the disks they want to use are not already in use.
  This is a limitation of the current design and is not specific to this
  change.
- We do not support disks being added to the volume group after it is created.
  This is a limitation of the current design and is not specific to this
  change. We can add support for this in the future, but it is not a priority at
  this time.
- If there is a particular disk that the user does not want to use, but matches
  the filter, they do not have a good way to exclude it in this model. We would
  document that the user should format the disk that they want to use prior

## Puzzles and Edge Cases

- If a user creates two storage classes with the same VG name, but different
  disk selection parameters, we would just use the VG that was created first.
  We would document this behavior.
- We would likely want to change the parameters, especially the default
  `localdisk.csi.acstor.io/disk-models` parameter, to adjust it for new disk
  types. This would be a bit strange because storageclasses are meant to be
  immutable once created. This can present a puzzle if we pass along the default
  values as values in `VolumeContext` and used those in the non-ephemeral,
  annotated case. We would not be able to schedule to PV to the new node. We
  would need to keep track of the default parameters in our code instead.

## Related Documentation

- [StorageClass API](https://kubernetes.io/docs/concepts/storage/storage-classes/)
- [LVM Documentation](https://www.tldp.org/HOWTO/LVM-HOWTO/)
