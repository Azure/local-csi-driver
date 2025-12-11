# ⚡local-csi-driver

[Install](#install) • [Docs](./docs/user-guide.md) •
[Troubleshooting](./docs/troubleshooting.md) • [Contribute](CONTRIBUTING.md)

![Made for Kubernetes](https://img.shields.io/github/tag/azure/local-csi-driver.svg?style=flat-square&label=release&color=purple)
![Supports Kubernetes v1.11.3+](https://img.shields.io/badge/Supports-Kubernetes_v1.11.3+-326ce5.svg?style=flat-square&logo=Kubernetes&logoColor=white)
![Latest commit](https://img.shields.io/github/last-commit/azure/local-csi-driver?style=flat-square)
![License](https://img.shields.io/badge/License-MIT-blue.svg)

local-csi-driver provides access to local NVMe drives on Kubernetes clusters.

## Install

Before proceeding, ensure you have the following installed:

- Kubernetes cluster (v1.11.3+)
- Kubectl (v1.11.3+)
- [Helm (v3.16.4+)](https://helm.sh/docs/intro/install/)

To install the latest release:

```sh
helm install local-csi-driver oci://localcsidriver.azurecr.io/acstor/charts/local-csi-driver --version 0.2.9 --namespace kube-system
```

Only one instance of local-csi-driver can run per cluster.

See the [User Guide](./docs/user-guide.md) for guidance on configuring a
StorageClass and managing volumes.

Helm chart values are documented in the [Helm chart README](./charts/latest/README.md).

## Development

For details on how to set up your development environment, see the
[Development Guide](./docs/development.md).

## Testing

See [/test/README.md](./test/README.md) for details on test coverage and how to run.

## Contributing

Please read our [contribution guide](CONTRIBUTING.md) which outlines all of our policies,
procedures, and requirements for contributing to this project.

## License

This project is licensed under the [MIT License](LICENSE).

**Trademarks** This project may contain trademarks or logos for projects, products,
or services. Authorized use of Microsoft trademarks or logos is subject to and
must follow Microsoft’s Trademark & Brand Guidelines. Use of Microsoft
trademarks or logos in modified versions of this project must not cause
confusion or imply Microsoft sponsorship. Any use of third-party trademarks or
logos are subject to those third-party’s policies.
