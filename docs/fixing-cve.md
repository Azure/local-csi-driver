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
    go mod graph | grep " example.com/affected/module"
    example.com/parent/module@v1.2.3 example.com/affected/module@v4.5.6
    ```

    ```bash
    go mod graph | grep " example.com/parent/module"
    local-csi-driver example.com/parent/module@v1.2.3
    ```

    If the parent dependency cannot yet select the fixed version, a temporary
    `replace` directive may be necessary:

    ```go
    replace example.com/affected/module v4.5.6 => example.com/affected/module v4.5.7
    ```

    Document why the replacement is needed and remove it when the parent
    dependency adopts the fixed version.

4. **Run `go mod tidy`**: After updating the dependencies, run the following
   command to clean up the `go.mod` and `go.sum` files:

   ```bash
   go mod tidy
   ```
