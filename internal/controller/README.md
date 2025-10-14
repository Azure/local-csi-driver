# Garbage Collection Controllers

## Overview

This package contains two complementary garbage collection controllers for managing LVM volumes:

1. **PV Garbage Collection Controller**: Event-driven controller that monitors PersistentVolume updates for node annotation mismatches
2. **LVM Orphan Cleanup Controller**: Periodic controller that scans all LVM volumes on the node for cleanup opportunities

Both controllers work together to ensure orphaned LVM logical volumes are automatically cleaned up when they exist on nodes where they shouldn't be or when they have no corresponding PersistentVolume in the cluster.

## Problem Statement

In the local CSI driver, volumes can sometimes end up on incorrect nodes due to various scenarios:
- Node rescheduling of workloads
- Node annotation changes
- Manual volume movements
- Controller restarts or failures

When a PV's node annotations (`selectedNodeAnnotation` or `selectedInitialNodeParam`) don't match the node where the actual LVM volume exists, the volume becomes "orphaned" and consumes storage space unnecessarily.

## Controllers

### 1. PV Garbage Collection Controller

**Event-driven controller** that reacts to PersistentVolume updates and:

- Only processes Update events (ignores Create/Delete for efficiency)
- Checks if the PV's node annotations match the current node
- Deletes the corresponding LVM logical volume when there's a mismatch
- Uses controller-runtime's predicate filtering for optimal performance

### 2. LVM Orphan Cleanup Controller

**Periodic controller** that runs every 10 minutes and:

- Scans all LVM volume groups with the configured tag (`local-csi` by default)
- Lists all logical volumes in those volume groups
- For each volume, checks if there's a corresponding PV in the cluster
- Deletes volumes that are either:
  - Completely orphaned (no PV exists)
  - Have node annotation mismatches with existing PVs

## Solution

These controllers:

1. **Watches PersistentVolumes** - Monitors all PVs managed by the local CSI driver (`localdisk.csi.acstor.io`)
2. **Detects Mismatches** - Identifies when PV node annotations don't match the current node
3. **Verifies Volume Existence** - Checks if the LVM volume actually exists on the current node
4. **Cleans Up Orphaned Volumes** - Deletes LVM logical volumes that shouldn't be on the current node
5. **Records Events** - Logs cleanup activities for monitoring and debugging

## How it Works

### Event Filtering

The controller is optimized to only reconcile on meaningful events:
- **Update Events**: When PV annotations or status change (primary use case)
- **Generic Events**: For periodic reconciliation triggers
- **Skips Create Events**: New PVs won't have orphaned volumes to clean up
- **Skips Delete Events**: PVs being deleted don't need cleanup processing

### Node Annotation Checking

The controller checks two annotations/parameters:
- **`selectedNodeAnnotation`** (`localdisk.csi.acstor.io/selected-node`): Current selected node
- **`selectedInitialNodeParam`** (`localdisk.csi.acstor.io/selected-initial-node`): Initial selected node

If either of these doesn't match the current node ID, the controller considers it a potential mismatch.

### LVM Volume Detection

Before deleting any volume, the controller:
1. Parses the volume ID (format: `<volume-group>#<logical-volume>`)
2. Checks if the corresponding LVM logical volume exists on the current node
3. Only deletes volumes that actually exist locally

### Safety Mechanisms

- Only processes PVs managed by the local CSI driver
- Skips PVs that are being deleted (have deletion timestamp)
- Skips PVs not in `Available` or `Bound` state
- Uses LVM's `IgnoreNotFound` to handle already-deleted volumes gracefully
- Records detailed events for audit trails

## Configuration

The controller is automatically initialized in the main application with:

```go
pvGCController := controller.NewPVGarbageCollector(
    mgr.GetClient(),           // Kubernetes client
    mgr.GetScheme(),           // Runtime scheme
    recorder,                  // Event recorder
    nodeName,                  // Current node name
    driver.SelectedNodeAnnotation,     // Selected node annotation key
    driver.SelectedInitialNodeParam,   // Initial node parameter key
    volumeClient,              // LVM core interface
    lvmMgr,                   // LVM manager
)
```

## Events Generated

The controller generates Kubernetes events on PVs:
- **`CleaningUpOrphanedVolume`** - When starting cleanup of a mismatched volume
- **`CleanedUpOrphanedVolume`** - When successfully cleaned up a volume
- **`CleanupFailed`** - When cleanup failed (with retry)

## Error Handling

- **Parse Errors**: Invalid volume IDs are logged and skipped
- **LVM Errors**: Failed deletions are retried after 5 minutes
- **Missing Volumes**: Already-deleted volumes are considered successful
- **API Errors**: Kubernetes API failures are retried by controller-runtime

---

## LVM Orphan Cleanup Controller

### Purpose

The `LVMOrphanCleanup` controller provides periodic scanning and cleanup of LVM volumes that may have been missed by the event-driven PV controller. This ensures comprehensive cleanup of:
- Volumes that existed before the controller was deployed
- Volumes orphaned during controller downtime
- Volumes left behind by external LVM operations

### Periodic Scanning

The controller runs at configurable intervals (default: 30 minutes) and:
1. **Scans Volume Groups**: Lists all volume groups with the configured tag (default: `local-csi`)
2. **Enumerates Volumes**: Gets all logical volumes from each matching volume group
3. **Cross-references with Kubernetes**: Checks each volume against existing PVs in the cluster
4. **Applies Cleanup Logic**: Uses the same node annotation checking as the PV controller

### Cleanup Scenarios

The controller identifies volumes for cleanup in two scenarios:

1. **Completely Orphaned**: No PersistentVolume exists with matching VolumeHandle
2. **Node Mismatch**: PV exists but node annotations don't match current node

Both scenarios use identical logic to the PV Garbage Collection Controller for consistency.

### Configuration

```go
lvmOrphanCleanup := controller.NewLVMOrphanCleanup(
    mgr.GetClient(),                    // Kubernetes client
    mgr.GetScheme(),                    // Runtime scheme
    recorder,                           // Event recorder
    nodeName,                           // Current node ID
    driver.SelectedNodeAnnotation,      // Node annotation key
    driver.SelectedInitialNodeParam,    // Initial node param key
    lvmMgr,                            // LVM manager
    volumeClient,                       // LVM core interface
    controller.LVMOrphanCleanupConfig{
        ReconcileInterval: lvmOrphanCleanupInterval,  // Configurable scan frequency
        VGNameTag:        "local-csi",               // VG tag filter
    },
)
```

### Integration with Manager

The controller implements `manager.Runnable` interface and is added to the controller-runtime manager:

```go
if err := mgr.Add(lvmOrphanCleanup); err != nil {
    return fmt.Errorf("failed to add LVM orphan cleanup controller: %w", err)
}
```

This ensures the periodic controller:
- Starts when the manager starts
- Stops gracefully when the manager shuts down
- Respects context cancellation for clean shutdown

### Safety Features

- **Volume Group Filtering**: Only processes VGs with the configured tag to avoid affecting non-CSI volumes
- **Driver Verification**: Only processes volumes that match the local CSI driver format
- **Graceful Shutdown**: Respects context cancellation for immediate stop during shutdown
- **Error Tolerance**: Continues processing other volumes even if individual cleanup operations fail

The combination of both controllers ensures comprehensive and efficient garbage collection of orphaned LVM volumes through both reactive (event-driven) and proactive (periodic) approaches.

---

## Command-Line Configuration

Both controllers can be enabled or disabled independently using command-line flags:

### Flags

- **`--enable-pv-garbage-collection`** (default: `true`)
  - Controls the PV Garbage Collection Controller
  - When disabled, the event-driven controller will not be started
  - Example: `--enable-pv-garbage-collection=false`

- **`--enable-lvm-orphan-cleanup`** (default: `true`)
  - Controls the LVM Orphan Cleanup Controller
  - When disabled, the periodic scanning controller will not be started
  - Example: `--enable-lvm-orphan-cleanup=false`

- **`--lvm-orphan-cleanup-interval`** (default: `30m0s`)
  - Sets the interval for the LVM Orphan Cleanup Controller periodic scans
  - Accepts standard Go duration format (e.g., `5m`, `1h`, `30s`)
  - Example: `--lvm-orphan-cleanup-interval=15m`

### Usage Examples

```bash
# Run with both controllers enabled (default)
./local-csi-driver

# Disable only the PV garbage collection controller
./local-csi-driver --enable-pv-garbage-collection=false

# Disable only the LVM orphan cleanup controller
./local-csi-driver --enable-lvm-orphan-cleanup=false

# Disable both controllers
./local-csi-driver --enable-pv-garbage-collection=false --enable-lvm-orphan-cleanup=false

# Run with custom cleanup interval (15 minutes)
./local-csi-driver --lvm-orphan-cleanup-interval=15m

# Run with custom cleanup interval (1 hour)
./local-csi-driver --lvm-orphan-cleanup-interval=1h
```

### Deployment Considerations

- **Production**: Keep both controllers enabled for comprehensive cleanup
- **Testing/Development**: May disable controllers to prevent cleanup during debugging
- **Specific Use Cases**:
  - Disable PV controller if you only want periodic cleanup
  - Disable periodic controller if you only want event-driven cleanup
  - Disable both if using external volume management

### Log Messages

When controllers are disabled, the application will log:
- `"PV garbage collection controller disabled"`
- `"LVM orphan cleanup controller disabled"`

When enabled, the application will log:
- `"PV garbage collection controller configured"`
- `"LVM orphan cleanup controller configured"`

## Testing

The controller includes comprehensive unit tests covering:
- Node annotation mismatch detection
- Volume ID parsing
- Mock LVM operations
- Reconciliation logic

Run tests with:
```bash
go test ./internal/controller/
```

## Monitoring

Monitor the controller through:
- **Kubernetes Events**: Check events on PVs for cleanup activities
- **Controller Logs**: Look for log entries with component "pv-garbage-collector"
- **Metrics**: Standard controller-runtime metrics are available

## Integration

The controller integrates seamlessly with the existing local CSI driver:
- Shares the same LVM manager instance
- Uses the same node annotation constants
- Respects the same CSI driver identification
- Operates alongside existing CSI controllers without conflicts