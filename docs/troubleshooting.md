# Troubleshooting Guide

This guide provides troubleshooting steps for common issues with the
local-csi-driver.

## Collecting Diagnostic Information

### Support Bundle

The local-csi-driver provides a support bundle feature that collects logs,
events, and other diagnostic information to help with troubleshooting.

To generate a support bundle:

```sh
make get-support-bundle
```

This will create a support bundle in the `support-bundles` directory. You can
specify a specific time range with:

```sh
SUPPORT_BUNDLE_SINCE_TIME="2023-06-01T00:00:00Z" make get-support-bundle
```

### Checking Logs

Logs are essential for troubleshooting. You can view logs for the
local-csi-driver components using kubectl:

```sh
# View logs
kubectl logs -n kube-system daemonsets/csi-local-node --prefix --all-containers
```

### Checking Kubernetes Events

Kubernetes events provide valuable information about what's happening in the
cluster:

```sh
# View all events in the namespace
kubectl get events -n kube-system

# Watch events in real-time
kubectl get events -n kube-system --watch

# Filter events related to PVCs
kubectl get events -n <namespace> --field-selector involvedObject.kind=PersistentVolumeClaim
```

## Common Issues

### Helm Installation Failure

If the helm installation fails with an error similar to:

```log
Error: INSTALLATION FAILED: Unable to continue with install: ServiceAccount "csi-local-node" in namespace "kube-system" exists and cannot be imported into the current release: invalid ownership metadata; annotation validation error: key "meta.helm.sh/release-name" must equal "local-csi-driver-2nd-install": current value is "local-csi-driver"
```

Check that local-csi-driver is not already installed. There can only be one
instance installed per Kubernetes cluster.

### PVC Creation Stuck in Pending State

If your PVC is stuck in the "Pending" state:

1. Check the StorageClass:

   ```sh
   kubectl get sc
   ```

2. Verify the PVC specification:

   ```sh
   kubectl describe pvc <pvc-name> -n <namespace>
   ```

3. Check for any errors in the events:

   ```sh
   kubectl get events -n <namespace> | grep <pvc-name>
   ```

4. Check if the driver is running properly:

   ```sh
   kubectl get pods -n kube-system -l app=csi-local-node
   ```

5. Check the driver logs for any errors:

   ```sh
   kubectl logs -n kube-system  daemonsets/csi-local-node --prefix --all-containers
   ```

### Volume Mount Failures

If pods cannot mount volumes:

1. Check the pod events:

   ```sh
   kubectl describe pod <pod-name> -n <namespace>
   ```

2. Check if the driver pods are running on all nodes:

   ```sh
   kubectl get pods -n kube-system -l app=csi-local-node -o wide
   ```

3. Verify the PV status:

   ```sh
   kubectl get pv | grep <pvc-name>
   kubectl describe pv <pv-name>
   ```

### Ephemeral Storage Annotation Issues

If you're using a non-ephemeral volume and encounter issues:

1. Verify the annotation is correctly set:

   ```sh
   kubectl get pvc <pvc-name> -n <namespace> -o jsonpath='{.metadata.annotations}'
   ```

2. The annotation should include:

   ```yaml
   localdisk.csi.acstor.io/accept-ephemeral-storage: "true"
   ```

   Make sure that you understand the implications of using ephemeral storage, as
   data may be lost if the node is deleted or the pod is moved to another node.

## RAID-Related Issues

### RAID Init Container Fails

If the RAID init container fails to start or create the RAID array:

1. Check the init container logs:

   ```sh
   kubectl logs -n kube-system <pod-name> -c raid
   ```

2. Common issues and solutions:

   **No unused devices found**:
   - Verify NVMe devices exist: `kubectl exec -n kube-system <pod-name> -c
     driver -- ls -la /dev/nvme*`
   - Check if devices are already in use by LVM or have filesystems
   - Ensure devices don't have existing RAID metadata

   **mdadm installation failed**:
   - Verify network connectivity for package downloads
   - Check if the node's package manager is supported (tdnf or apt-get)
   - Manually install mdadm on the node if needed

   **Fewer than 2 devices for RAID**:
   - RAID 0 requires at least 2 devices
   - With only 1 device, the init container will create LVM directly on it
   - Ensure you have the expected number of NVMe devices on the node

   **RAID creation command failed**:
   - Check for disk errors: `kubectl exec -n kube-system <pod-name> -c driver --
     dmesg | grep -i error`
   - Verify devices are accessible and not faulty

3. View RAID array status:

   ```sh
   kubectl exec -n kube-system <pod-name> -c driver -- mdadm --detail /dev/md0
   ```

### Volume Group Name Mismatch

If volumes fail to provision when using RAID with a custom volume group:

1. Verify Helm values match StorageClass:

   ```sh
   helm get values local-csi-driver -n kube-system
   ```

2. Check the StorageClass parameters:

   ```sh
   kubectl get storageclass <storageclass-name> -o yaml
   ```

3. Ensure both `raid.volumeGroup` (Helm) and `volumeGroup` (StorageClass) have
   the same value.

### RAID Array Not Persisting After Reboot

If the RAID array disappears after node reboot:

1. Check if mdadm configuration is saved:

   ```sh
   kubectl exec -n kube-system <pod-name> -c driver -- cat /etc/mdadm/mdadm.conf
   ```

2. On the node, ensure mdadm is configured to auto-assemble arrays:
   - Update initramfs to include RAID configuration
   - Enable mdadm monitoring service if available

3. Consider making the RAID configuration persistent across reboots by:
   - Using a systemd service to assemble arrays on boot
   - Configuring the node's init system appropriately

### Performance Issues with RAID

If performance is not as expected:

1. Verify RAID array is using all expected devices:

   ```sh
   kubectl exec -n kube-system <pod-name> -c driver -- mdadm --detail /dev/md0 | grep "Number\\|Device"
   ```

2. Check RAID array status for degraded state:

   ```sh
   kubectl exec -n kube-system <pod-name> -c driver -- cat /proc/mdstat
   ```

3. Compare with LVM striping (default behavior when RAID is disabled):
   - RAID may not always provide better performance
   - Consider workload characteristics and test both configurations
