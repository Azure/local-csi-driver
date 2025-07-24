# Development Guide

## Setting up the environment

## Pre-commit

Pre-commit is used to enforce code quality and style. You should install
`python` and `pip` using your package manager. To install pre-commit, run:

```sh
pip install pre-commit
```

Then, install the pre-commit hooks:

```sh
pre-commit install
```

You can run pre-commit manually on all files with:

```sh
pre-commit run --all-files
```

`pre-commit` will automatically run on every commit, ensuring that your code
adheres to the specified style and quality checks.

To add additional hooks, you can modify the `.pre-commit-config.yaml` file.
For more details on configuring pre-commit hooks, refer to the
[official pre-commit documentation][pre-commit].

The most important hook for our project is the [golangci-lint] hook. It enforces
the Go code style and checks for common mistakes. It is configured in the
`.golangci.yml` file. To see the available linters, refer to the [golangci-lint
documentation].

If you receive a comment on a PR that could have been caught by a linter and
fixed automatically, consider searching for a `pre-commit` hook or `golangci-lint`
linter that could have caught the issue. If you find one, please add it to the
`.pre-commit-config.yaml` file and the `.golangci.yml` file. This will help ensure
that the issue is caught in the future and that the code adheres to the
specified style and quality checks.

## Building the project

### Prerequisites

- [Go 1.24 or later](https://go.dev/dl/)
- Docker
- Make

The rest of the utilities are installed automatically by the Makefile.

### Building the binary

To build the project, run:

```sh
make build
```

This will compile the project and place the binary in the `bin` directory.

```sh
./bin/local-csi-driver --version
```

### Building and Pushing the docker image

To build the Docker image, run:

```sh
REGISTRY=<your registry> make docker-build
```

Substitute `<your registry>` with the desired Docker registry. This will build the
Docker image and tag it with the specified registry.

```sh
REGISTRY=<your registry> make docker-push
```

This will push the Docker image to the specified registry. You will often run
these two commands together, so you can combine them into one command:

```sh
REGISTRY=<your registry> make docker-build docker-push
```

## Building the Helm chart

To build the Helm chart, run:

```sh
make helm-build
```

This will package a helm chart for the project and place it in the `dist`
directory.

## Deploying the project

### Creating a local Kubernetes cluster

Some development can be done in a local Kubernetes cluster. To create a local
Kubernetes cluster, you can use [kind]. To create a local cluster, run:

```sh
make single
```

This will create a local Kubernetes cluster using kind. You can then use this
cluster to test the project. To delete the cluster, run:

```sh
make clean
```

### Creating an AKS cluster

Most of the development is done in an AKS cluster. To create an AKS cluster,
you can use the make target `aks`. The AKS cluster is created using [bicep].

To create a cluster, run:

```sh
make aks
```

For more details on the bicep template and available parameters, refer to the
[deploy/README.md](../deploy/README.md) file.

### To Deploy

Build and push the Docker image and Helm chart to your registry and Helm
repository, respectively. You can do this with:

```sh
REGISTRY=<registry> make docker-build helm-build docker-push helm-push
```

To deploy the project to your Kubernetes cluster, you can call all the make targets
in one command:

```sh
REGISTRY=<registry> make docker-build helm-build docker-push helm-push helm-install
```

This will build the Docker image, push it to the specified registry, build the
Helm chart, push it to the specified Helm repository, and install the project to
your Kubernetes cluster.

### To Uninstall

To uninstall the project from your Kubernetes cluster, run:

```sh
make helm-uninstall
```

This will remove the project from your cluster.

## Testing the project

The project has a number of tests that can be run to ensure that the project is
working correctly. The tests are divided into categories: unit tests, E2E tests,
External E2E tests, and sanity tests. The tests are written in Go.

Generally, if something can be tested with a unit test, it should be. If
something is too complex to test with a unit test, it should be tested with an
E2E test. The sanity and external E2E tests are conformance tests provided by
the Kubernetes community to test CSI drivers. For more details on the tests,
refer to the [test/README.md](../test/README.md) file. Unit tests can be found throughout
the project, but other tests are located in the `test` directory.

### Unit tests

To run the unit tests, run:

```sh
make test
```

This will run all the unit tests in the project.

### E2E Tests

These tests are run in a Kubernetes cluster. To run the E2E tests, you need to
set up a Kubernetes cluster and install the project in it. You can do this with:

```sh
REGISTRY=<registry>.azurecr.io make docker-build helm-build docker-push helm-push test-e2e
```

Those tests that are part of e2e suite are run in the cluster. The tests are
written in Go and use the Ginkgo testing framework. The tests are located in the
`test/e2e` directory. By default, the test-e2e test target will include tests
that we can run in a `kind` cluster. If you have a real AKS cluster, you can run
the tests

```sh
REGISTRY=<registry>.azurecr.io make docker-build helm-build docker-push helm-push test-e2e-aks
```

This will run the E2E tests in the AKS cluster. The tests are written in Go and
are chosen with the "aks" and "e2e" label selectors.

You can also run the E2E tests in a local Kubernetes cluster using [kind]. To do
this, you need to install `kind` and create a local cluster. You can do this
with:

```sh
make single
```

Note: Most of the tests are skipped in this mode, as they require a real aks
cluster to run.

### External E2E Tests

To run the [external E2E tests], you need to set up a Kubernetes cluster and
install the project in it. You can do this with:

```sh
REGISTRY=<registry>.azurecr.io make docker-build helm-build docker-push helm-push test-e2e-aks
```

This will run the external E2E tests in the cluster using the AKS cluster. The
tests are written in Go and use the Ginkgo testing framework. There are two
kinds of tests, the `lvm` and `lvm-annotation` tests.

The `lvm` tests only run the generic ephemeral volume tests, which is the only
kind of volume permitted by default by the driver. The `lvm-annotation` tests
run all the tests specified by the external tests.

It uses [`kyverno`][kyverno] to add the
`localdisk.csi.acstor.io/accept-ephemeral-storage: "true"` annotation to the
persistent volume claim. This allows the tests to test the
"accept-ephemeral-storage" mode of the driver. The tests are located in the
`test/e2e/external` directory.

### Sanity Tests

To run the sanity tests, run:

```sh
REGISTRY=<registry>.azurecr.io make docker-build helm-build docker-push helm-push test-sanity
```

The sanity tests are conformance tests that checks if the driver adheres to the
csi spec. The tests are written in Go and use the Ginkgo testing framework. The
implementation can be found in the  [`kubernetes-csi/csi-test`][csi-test] repo.
The tests are located in the `test/sanity` directory.

The tests are run on an AKS cluster. If you find a change that you made that
breaks the tests, please refer to the
[`container-storage-interface/spec`][csi-spec] repo to review the spec.

[pre-commit]: https://pre-commit.com
[golangci-lint]: https://github.com/golangci/golangci-lint
[golangci-lint documentation]: https://golangci-lint.run/usage/configuration
[bicep]: https://learn.microsoft.com/en-us/azure/azure-resource-manager/bicep/overview?tabs=bicep
[kind]: https://kind.sigs.k8s.io
[external E2E tests]: https://github.com/kubernetes/kubernetes/tree/master/test/e2e/storage/external
[kyverno]: https://github.com/kyverno/kyverno
[csi-test]: https://github.com/kubernetes-csi/csi-test
[csi-spec]: https://github.com/container-storage-interface/spec
