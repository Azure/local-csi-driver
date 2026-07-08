# Release pipeline (Azure DevOps)

`release.yml` is the Azure DevOps pipeline that builds and pushes
the release artifacts to ACR. It replaces the former GitHub Actions release
workflow (`.github/workflows/push-tag.yml`).

It uses the **OneBranch governed templates**
(`v2/OneBranch.NonOfficial.CrossPlat.yml`) and mirrors the team's existing
internal OneBranch release pipeline. The release pipeline is kept **separate**
from pull-request validation (PR validation stays in GitHub Actions). See
<https://docs.opensource.microsoft.com/releasing/general-guidance/build-systems>.

## What it does

Triggered on pushes to `main` and on release tags
(`v[0-9]+.[0-9]+.[0-9]+`, plus `-preview.N` / `-hotfix.N`):

- **SetVars** derives the tag(s):
  - release tag: the git tag verbatim (e.g. `v0.2.13`);
  - `main`: a per-commit `v0.0.1-g<sha>` plus a floating `v0.0.1-latest`
    (base version `0.0.1`, matching the previous workflow).
- **Build (per image, per arch)**: builds `local-csi-driver` (`Dockerfile`) and
  `local-csi-manager` (`Dockerfile.manager`) on amd64 and arm64 using the
  OneBranch `onebranch.pipeline.imagebuildinfo@1` task and pushes arch-suffixed
  tags. The amd64 job additionally runs the container-structure tests
  (`containerTestsYAMLPath`) before pushing.
- **Manifest (per image)**: combines the per-arch images into a multi-arch
  manifest (`manifest_push`), and on `main` also publishes the floating
  `-latest` manifest pointing at the same arch images.
- **Helm**: `make helm-push` (which builds and pushes) to
  `oci://<registry>/<repoBase>/charts`.

Images and chart are pushed to `<registry>/<repoBase>/...`, defaulting to
`localcsidriver.azurecr.io/acstor` (the same target as the previous workflow).

## Build agents / pools

There is **no pool to provision or name**. OneBranch jobs declare a pool
`type` and OneBranch routes them to its shared managed fleet:

- `type: docker` for the image build/push jobs (with `hostArchitecture: arm64`
  for the arm64 jobs);
- `type: linux` for the tag-derivation and Helm jobs.

This is why OneBranch was chosen for the pool question: it avoids per-team 1ES
pool provisioning and does not depend on a specific shared pool name being
visible in the target project.

## Authentication

- **Images**: pushed via the OneBranch container tasks
  (`onebranch.pipeline.containercontrol@1` login +
  `onebranch.pipeline.imagebuildinfo@1` push) using the ARM (WIF) service
  connection (`acrServiceConnection`).
- **Helm**: pushed with `AzureCLI@2` bound to the same **ARM service connection
  with workload identity federation (WIF)** (`acrServiceConnection`), which runs
  `make helm-login` (`az acr login`) - no stored registry credentials.

## One-time setup

These are configured once by someone with the appropriate Azure DevOps / Azure
permissions; the pipeline YAML depends on them:

1. **Azure DevOps organization/project.** Use an org appropriate for a public
   GitHub repo. Do **not** intermingle public and private projects in one ADO
   organization. Only one ADO organization can be connected to a given GitHub
   repository at a time.
2. **GitHub connection.** In the ADO project, create a GitHub service connection
   to `Azure/local-csi-driver` so the pipeline can be triggered by pushes/tags
   and check out the source.
3. **ACR service connection (`acrServiceConnection`, ARM/WIF).** ARM service
   connection using workload identity federation whose identity has **AcrPush**
   on the target registry. Used by both the OneBranch container tasks (image
   push) and the Helm chart push. The ACR name used for container login is
   derived from `registry` (the label before `.azurecr.io`).
4. **Create the pipeline** in ADO pointing at
   `.pipelines/release.yml`, and confirm tag triggers are enabled
   (the ADO "Override the YAML trigger" option is not set).

For a production/MCR publish, switch the `extends` template to
`v2/OneBranch.Official.CrossPlat.yml@templates`.

## Parameters

| Parameter              | Default                     | Purpose                                |
| ---------------------- | --------------------------- | -------------------------------------- |
| `acrServiceConnection` | `local-csi-driver-acr`      | ARM (WIF) SC (image + Helm push)       |
| `registry`             | `localcsidriver.azurecr.io` | Registry host (ACR name derived)       |
| `repoBase`             | `acstor`                    | Repository base path                   |
| `major`/`minor`/`patch`| `0`/`0`/`1`                 | Base version for `main` dev tags       |
| `images`               | driver + manager            | Images to build (dockerfile + tests)   |
