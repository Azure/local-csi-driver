# AKS Development Clusters

The `deploy` directory contains Bicep templates and parameter files for creating
AKS clusters used by local-csi-driver development and tests.

These templates create Azure resources and can incur cost. Use a dedicated
development subscription and remove the resource group when it is no longer
needed.

## Make variables

| Variable | Default | Purpose |
| --- | --- | --- |
| `AKS_TEMPLATE` | `nvme` | Parameter file name under `deploy/parameters` |
| `AKS_LOCATION` | `uksouth` | Azure region for the resource group |
| `AKS_RESOURCE_GROUP` | `${USER}-local-csi-driver-${AKS_TEMPLATE}` | Resource group name |
| `AKS_IS_TEST` | `false` | Mark the deployment as a test environment |

## Available parameter sets

The value of `AKS_TEMPLATE` must match a JSON file in `deploy/parameters`
without the `.json` suffix.

Current parameter sets include:

- `azurelinux`
- `azurelinux-userpool`
- `azurelinux-zonal`
- `nvme`
- `nvme-arm`
- `nvme-autoscaler`
- `nvme-four-disks`
- `nvme-scale`
- `nvme-single-node`
- `nvme-two-disks`
- `nvme-ubuntu`
- `nvme-v4`
- `nvme-v4-ubuntu`
- `nvme-zonal`
- `ubuntu`
- `ubuntu-fips`

Inspect the selected JSON file before deployment for VM sizes, node counts,
zones, Kubernetes settings, and other template-specific values.

## Create a cluster

```sh
make aks AKS_TEMPLATE=nvme
```

Override the region and resource group when necessary:

```sh
make aks \
  AKS_TEMPLATE=nvme-arm \
  AKS_LOCATION=eastus \
  AKS_RESOURCE_GROUP=my-local-csi-test
```

The Makefile installs the pinned Bicep CLI when needed and calls
`deploy/scripts/create.sh`.

## Preview a deployment

Run the Bicep what-if path before creating resources:

```sh
make aks-cluster-requirements AKS_TEMPLATE=nvme
```

## Delete a cluster

```sh
make aks-clean AKS_RESOURCE_GROUP=my-local-csi-test
```

This deletes the Azure resource group and all resources it contains.
