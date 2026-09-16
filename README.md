# ⚡local-csi-driver

[Install](#install) | [Architecture](./docs/architecture.md) |
[Docs](./docs/README.md) |
[Troubleshooting](./docs/troubleshooting.md) | [Contribute](CONTRIBUTING.md)

![Made for Kubernetes](https://img.shields.io/github/tag/azure/local-csi-driver.svg?style=flat-square&label=release&color=purple)
![Latest commit](https://img.shields.io/github/last-commit/azure/local-csi-driver?style=flat-square)
![License](https://img.shields.io/badge/License-MIT-blue.svg)

local-csi-driver provides access to local NVMe drives on Kubernetes clusters.

## Install

Before proceeding, ensure you have the following installed:

- A Linux Kubernetes cluster with eligible local NVMe devices
- A `kubectl` version compatible with the cluster
- [Helm 3](https://helm.sh/docs/intro/install/)

Find the current release on the
[GitHub Releases page](https://github.com/Azure/local-csi-driver/releases/latest)
and substitute its version without the `v` prefix:

```sh
helm install local-csi-driver \
  oci://localcsidriver.azurecr.io/acstor/charts/local-csi-driver \
  --version <release> \
  --namespace kube-system
```

Only one instance of local-csi-driver can run per cluster.

See the [User Guide](./docs/user-guide.md) for guidance on configuring a
StorageClass and managing volumes.

See [Architecture](./docs/architecture.md) for the deployment topology, CSI
request flows, volume lifecycle, recovery model, security boundaries, and code
ownership map.

Helm chart values are documented in the [Helm chart README](./charts/latest/README.md).

## Development

For details on how to set up your development environment, see the
[Development Guide](./docs/development.md).

## Testing

See [/test/README.md](./test/README.md) for details on test coverage and
how to run.

## Contributing

Please read our [contribution guide](CONTRIBUTING.md) which outlines all of
our policies, procedures, and requirements for contributing to this project.

## License

This project is licensed under the [MIT License](LICENSE).

**Trademarks** This project may contain trademarks or logos for projects, products,
or services. Authorized use of Microsoft trademarks or logos is subject to and
must follow Microsoft’s Trademark & Brand Guidelines. Use of Microsoft
trademarks or logos in modified versions of this project must not cause
confusion or imply Microsoft sponsorship. Any use of third-party trademarks or
logos are subject to those third-party’s policies.
