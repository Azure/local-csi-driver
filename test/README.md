# Testing

This document explains the E2E and other test coverage for local-csi-driver.

## Unit Tests

To run the unit tests, run:

```sh
make test
```

This will run all the unit tests in the project. Unit tests are throughout the
code base. All libraries (e.g. `internal/pkg/*`) should be covered completely.
Elsewhere, functions should be covered as appropriate, depending on their
complexity.

It is expected that all logic outside the controller reconcilers will be covered
by unit tests. Within the controller reconcilers, complex logic should be
extracted into functions that are covered by unit tests.

We also have some unit tests that cover controller behavior and run against a
fake k8s api server. They do not require compiling and deploying images into a
real cluster, making them quick to run. All controller code paths should be
testedhere.

Within the code, look for `suite_test.go` as the starting points for
integration tests and `*_test.go` files for the test cases for each controller.

[Ginkgo][ginkgo] is used for the integration tests.
See[controller-tests](https://book.kubebuilder.io/cronjob-tutorial/writing-tests)
for a guide.

## E2E Tests

### Setup

These tests are designed to run on a running cluster, and they verify behavior
triggering externally on that cluster (e.g. by creating PVCs).

Currently a [Kind][kind] cluster is presumed. If there is not already a Kind
cluster present (`make single` or `make multi` will create one), then a new
cluster will be provisioned for the test.

`make clean` will remove the Kind cluster.

If `CREATE_CLUSTER` is unset  or set to `false`, then the current cluster set in
`kubectl` context will be used. This is useful for testing against remote
clusters.

`SKIP_UNINSTALL` set to `true` will leave the cluster running for debugging or
to speed up successive runs.

`SKIP_INSTALL_PROMETHEUS` set to `true` will skip installing
[Prometheus][prometheus], which is installed by default so that metrics can be
verified.

`SKIP_METRICS` set to `true` will skip the metrics endpoint tests. These rarely
change and add to test duration.

Optionally, when [Jaeger][jaeger] is installed into an existing cluster using
`make jaeger`, tracing will be enabled and accessible on
[http://127.0.0.1:16686/](http://127.0.0.1:16686/). This can be incredibly
helpful when debugging.

To further optimize dev cycles, only specific tests can be run by setting
`FOCUS`. For example, `FOCUS="should delete pv" make e2e` will run only the PV
delete test.

`LABEL_FILTER` can be used to run tests with specific labels. For example,
`LABEL_FILTER="lvm" make e2e` will run only tests with the `lvm` label.

### E2E Integration Tests

These tests are run in a Kubernetes cluster. To run the E2E tests, you need to
set up a Kubernetes cluster and install the project in it. You can do this the
following command to do all of these steps in one go:

```sh
REGISTRY=<registry>.azurecr.io make docker-build helm-build docker-push helm-push test-e2e
```

Those tests that are part of the E2E suite are run in the cluster. The tests are
written in Go and use the Ginkgo testing framework. The tests are located in the
`test/e2e` directory. By default, the `test-e2e` target will include tests that
can run in a `kind` cluster.

This will run the E2E tests in the AKS cluster. The tests are written in Go and
are chosen with the "aks" and "e2e" label selectors.

You can also run the E2E tests in a local Kubernetes cluster using
[kind](https://kind.sigs.k8s.io/). To do this, you need to install `kind` and
create a local cluster. You can do this with:

```sh
make single
```

> **Note**: Most of the tests are skipped in this mode, as they require a real
AKS cluster to run.

If you have a real AKS cluster, you can run the
tests with:

```sh
REGISTRY=<registry>.azurecr.io make docker-build helm-build docker-push helm-push test-e2e-aks
```

For more information on how to set up the AKS cluster, see the deployment
instructions in the deployment [README](../deploy/README.md).

### External E2E Tests

To run the [external E2E tests](https://github.com/kubernetes/kubernetes/tree/master/test/e2e/storage/external),
you need to set up a Kubernetes cluster and install the project in it. You can
do this with:

```sh
REGISTRY=<registry>.azurecr.io make docker-build helm-build docker-push helm-push test-e2e-aks
```

This will run the external E2E tests in the cluster using the AKS cluster. The
tests are written in Go and use the Ginkgo testing framework. There are two
kinds of tests, the `lvm` and `lvm-annotation` tests.

The `lvm` tests only run the generic ephemeral volume tests, which is the only
kind of volume permitted by default by the driver.

The `lvm-annotation` tests run all the tests specified by the external tests. It
uses [`kyverno`](https://github.com/kyverno/kyverno) to add the
`localdisk.csi.acstor.io/accept-ephemeral-storage: "true"` annotation to the
persistent volume claim. This allows the tests to test the
"accept-ephemeral-storage" mode of the driver. The tests are located in the
`test/e2e/external` directory.

### Sanity Tests

To run the sanity tests, run:

```sh
REGISTRY=<registry>.azurecr.io make docker-build helm-build docker-push helm-push test-sanity
```

The sanity tests are conformance tests that check if the driver adheres to the
CSI spec. The tests are written in Go and use the Ginkgo testing framework. The
implementation can be found in the
[`kubernetes-csi/csi-test`](https://github.com/kubernetes-csi/csi-test) repo.
The tests are located in the `test/sanity` directory. The tests are run on an
AKS cluster. If you find a change that you made that breaks the tests, please
refer to the
[`container-storage-interface/spec`](https://github.com/container-storage-interface/spec)
repo to review the spec to make any necessary changes to the driver.

### Scale and AKS Integration Tests

#### Scale Tests

To run scale tests, which verify the driver's behavior under high load:

```sh
SCALE=150 make test-scale
```

This will create 150 volumes and run a set of tests against them. The number
of volumes can be adjusted by changing the `SCALE` variable.

#### Upgrade Tests

To test the driver's functionality after [upgrading the Kubernetes
cluster](https://learn.microsoft.com/en-us/azure/aks/upgrade-aks-cluster?tabs=azure-cli):

```sh
LABEL_FILTER=lvm make test-upgrade
LABEL_FILTER=lvm-annotation make test-upgrade

```

These tests ensure that the driver remains functional after a cluster upgrade.

### Restart and Scaledown Tests

#### Restart Tests

To test the driver's behavior after [restarting the
cluster](https://learn.microsoft.com/en-us/azure/aks/start-stop-cluster?tabs=azure-cli):

```sh
make test-restart
```

#### Scaledown Tests

To test the driver's behavior when [scaling down the
cluster](https://learn.microsoft.com/en-us/azure/aks/scale-cluster?tabs=azure-cli):

```sh
make test-scaledown
```

These tests ensure that the driver handles node removal gracefully.

## Container Structure

`make test-container-structure` verifies that the Docker images produced are
valid and pass tests. For example, it tests that the correct files and their
permissions are correct, and that when run, they produce the expected output.

See [/test/container-structure/README.md](./container-structure/README.md)
for details.

## Unit

`make test` runs the unit tests.

Unit tests are throughout the code base. All libraries (e.g. `internal/pkg/*`)
should be covered completely. Elsewhere, functions should be covered as
appropriate, depending on their complexity.

It is expected that all logic outside the controller reconcilers will be covered
by unit tests. Within the controller reconcilers, complex logic should be
extracted into functions that are covered by unit tests.

[kind]: https://kind.sigs.k8s.io/
[ginkgo]: https://onsi.github.io/ginkgo/
[prometheus]: https://prometheus.io/
[jaeger]: https://www.jaegertracing.io/
