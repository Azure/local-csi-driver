# Fixing CVE

This document outlines the steps to fix a CVE (Common Vulnerabilities and
Exposures) in go modules used by the local-csi-driver project.

## Steps to Fix CVE in Go Modules

1. **Identify the CVE**: Determine which CVE needs to be fixed. This can be done
   by checking the project's dependencies for known vulnerabilities. Check to
   see what version the CVE affects and if there is a patch available. If there
   is no patch available, you may need to wait for the upstream project to
   release a fix.

2. **Update direct dependencies**: If the CVE affects a direct dependency,
   update the dependency to a version that contains the fix. This can be done by
   running the following command:

   ```bash
   go get <module>@<version>
   ```

   Replace `<module>` with the name of the module and `<version>` with the
   version that contains the fix.

3. **Update indirect dependencies**: If the CVE affects an indirect dependency,
   you may can see the edges of the module graph by running:

   ```bash
    go mod graph | grep <module>
    ```

    For example:

    ```bash
    go mod graph | grep " github.com/docker/docker"
    github.com/google/cadvisor@v0.52.1 github.com/docker/docker@v26.1.4+incompatible
    ```

    ```bash
    go mod graph | grep " github.com/google/cadvisor"

    local-csi-driver github.com/google/cadvisor@v0.52.1
    k8s.io/kubernetes@v1.33.1 github.com/google/cadvisor@v0.52.1
    ```

    In this case, cadvisor is a dependency of the latest kubernetes module.
    Furthermore, cadvisor has not fixed this issue in the latest version, so we
    need to replace the modules in the `go.mod` file with a patch version that
    contains the fix. This can be done by adding a `replace` directive in the
    `go.mod` file:

    ```go
    replace github.com/docker/docker v26.1.4+incompatible => github.com/docker/docker v26.1.5+incompatible
    ```

4. **Run `go mod tidy`**: After updating the dependencies, run the following
   command to clean up the `go.mod` and `go.sum` files:

   ```bash
   go mod tidy
   ```
