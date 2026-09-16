# Documentation

Use this index to find documentation by goal. Start with the user guide for
deployment tasks or the architecture document to understand how the system
works.

## Start here

- [User Guide](user-guide.md) - install, configure, and use local-csi-driver
- [Architecture](architecture.md) - understand components, CSI request flows,
  volume lifecycle, recovery, security boundaries, and code ownership
- [Troubleshooting](troubleshooting.md) - diagnose common operational problems

## Architecture and design

- [Architecture](architecture.md) - current end-to-end system explanation
- [PV Recovery](design/pv-recovery.md) - recover workload availability by
  recreating an empty local volume on another node
- [PV Cleanup](design/pv-cleanup.md) - handle PV deletion when its topology node
  is unavailable
- [Webhooks](design/webhooks.md) - validate PVC usage and inject workload
  affinity
- [Disk Selection](design/disk-selection.md) - understand current device
  filters and their safety limits

## Configuration and implementation reference

- [Helm chart reference](../charts/latest/README.md) - current chart values and
  deployment defaults
- [CSI package](../internal/csi/README.md) - CSI package responsibilities
- [Garbage collection](../internal/gc/README.md) - node-local cleanup
  implementation
- [Test documentation](../test/README.md) - test coverage and test environments

## Contributor documentation

- [Development Guide](development.md) - set up a development environment and
  build the project
- [AKS Development Clusters](../deploy/README.md) - create test clusters with
  the repository's Bicep templates
- [Contributing Guide](../CONTRIBUTING.md) - contribution policies and workflow
- [Security Policy](../SECURITY.md) - report security vulnerabilities
