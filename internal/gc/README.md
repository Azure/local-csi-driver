# Garbage Collection Controllers

## Overview

This package contains two complementary garbage collection controllers for managing LVM volumes:

1. **PV Garbage Collection Controller**: Event-driven controller that monitors PersistentVolume updates for node annotation mismatches
2. **LVM Orphan Scanner**: Periodic controller that scans all LVM volumes on the node for cleanup opportunities

Both controllers share a unified `LVMVolumeManager` interface and work together to ensure orphaned LVM logical volumes are automatically cleaned up when they exist on nodes where they shouldn't be or when they have no corresponding PersistentVolume in the cluster.

## Problem Statement

In the local CSI driver, LVM volumes can be leaked on nodes when workloads are moved or rescheduled to different nodes. This happens in various scenarios:

- Pod rescheduling to different nodes
- Node annotation changes that redirect PVs to new nodes
- Workload migrations during cluster maintenance

When a workload moves and the PV's node annotations (`selectedNodeAnnotation` or `selectedInitialNodeParam`) are updated to point to a different node, the original LVM logical volume may remain on the previous node, becoming "orphaned" and consuming storage space unnecessarily.

## Controllers

### 1. PV Garbage Collection Controller

**Event-driven controller** that reacts to PersistentVolume updates and:

- Only processes Update events (ignores Create/Delete for efficiency)
- Checks if the PV's node annotations match the current node
- Deletes the corresponding LVM logical volume when there's a mismatch
- Uses controller-runtime's predicate filtering for optimal performance

### 2. LVM Orphan Scanner

**Periodic controller** that runs every 10 minutes and:

- Scans the default LVM volume group for logical volumes
- Lists all logical volumes using the shared `LVMVolumeManager` interface
- For each volume, uses efficient field indexing to check if there's a corresponding PV in the cluster
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

The controller uses a hierarchical approach to check node assignments:

- **Primary**: `selectedNodeAnnotation` (`localdisk.csi.acstor.io/selected-node`) - Current selected node
- **Fallback**: `selectedInitialNodeParam` (`localdisk.csi.acstor.io/selected-initial-node`) - Initial selected node (only checked if primary annotation doesn't exist)

The controller first checks if the `selectedNodeAnnotation` exists and matches the current node. If this annotation doesn't exist, it falls back to checking the `selectedInitialNodeParam`. A mismatch in the applicable annotation indicates the volume should be cleaned up.

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

## Shared Architecture

Both controllers use a unified `LVMVolumeManager` interface that provides:

```go
type LVMVolumeManager interface {
    // DeleteVolume deletes an LVM logical volume by volume ID
    DeleteVolume(ctx context.Context, volumeID string) error
    // GetVolumeName extracts the volume name from a volume ID
    GetVolumeName(volumeID string) (string, error)
    // GetNodeDevicePath returns the device path for a volume ID
    GetNodeDevicePath(volumeID string) (string, error)
    // UnmountVolume unmounts a volume at the specified device path
    UnmountVolume(ctx context.Context, devicePath string) error
    // ListLogicalVolumes lists logical volumes (used by the scanner)
    ListLogicalVolumes(ctx context.Context, opts *lvmMgr.ListLVOptions) ([]lvmMgr.LogicalVolume, error)
}
```

This interface is implemented by `lvmVolumeManagerAdapter` which wraps the existing LVM components and provides a clean abstraction for both controllers.

## Configuration

The PV Garbage Collection Controller is automatically initialized in the main application with:

```go
pvGCController := gc.NewPVGarbageCollector(
    mgr.GetClient(),           // Kubernetes client
    mgr.GetScheme(),           // Runtime scheme
    recorder,                  // Event recorder
    nodeName,                  // Current node name
    driver.SelectedNodeAnnotation,     // Selected node annotation key
    driver.SelectedInitialNodeParam,   // Initial node parameter key
    volumeClient,              // LVM core interface
    lvmMgr,                   // LVM manager
    mounter,                   // Volume mounter interface
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

## LVM Orphan Scanner

### Purpose

The `LVMOrphanScanner` controller provides periodic scanning and cleanup of LVM volumes that may have been missed by the event-driven PV controller. This ensures comprehensive cleanup of:

- Volumes that existed before the controller was deployed
- Volumes orphaned during controller downtime
- Volumes left behind by external LVM operations

### Periodic Scanning

The controller runs at configurable intervals (default: 10 minutes) and:

1. **Scans Default Volume Group**: Uses the shared `LVMVolumeManager` interface to list logical volumes
2. **Efficient PV Lookup**: Uses field indexing for O(1) PV lookups by volume handle
3. **Cross-references with Kubernetes**: Checks each volume against existing PVs in the cluster using indexed queries
4. **Applies Cleanup Logic**: Uses the same node annotation checking and shared `hasNodeAnnotationMismatch` function

### Field Indexing Optimization

The scanner uses efficient field indexing for PV lookups:

- **Index**: `spec.csi.volumeHandle` field on PersistentVolume objects
- **Query**: Direct lookup by volume ID using `client.MatchingFields`
- **Performance**: O(1) lookup time instead of O(n) list-and-filter operations

### Cleanup Scenarios

The controller identifies volumes for cleanup in two scenarios:

1. **Completely Orphaned**: No PersistentVolume exists with matching VolumeHandle
2. **Node Mismatch**: PV exists but node annotations don't match current node

Both scenarios use identical logic to the PV Garbage Collection Controller for consistency through shared functions.

### Configuration

```go
lvmOrphanScanner := gc.NewLVMOrphanScanner(
    mgr.GetClient(),                    // Kubernetes client
    mgr.GetScheme(),                    // Runtime scheme
    recorder,                           // Event recorder
    nodeName,                           // Current node ID
    driver.SelectedNodeAnnotation,      // Node annotation key
    driver.SelectedInitialNodeParam,    // Initial node param key
    lvmMgr,                            // LVM manager
    volumeClient,                       // LVM core interface
    mounter,                            // Volume mounter interface
    gc.LVMOrphanScannerConfig{
        ReconcileInterval: lvmOrphanScanInterval,  // Configurable scan frequency
    },
)
```

### Integration with Manager

The scanner implements `manager.Runnable` interface and is added to the controller-runtime manager:

```go
if err := mgr.Add(lvmOrphanScanner); err != nil {
    return fmt.Errorf("failed to add LVM orphan scanner: %w", err)
}
```

This ensures the periodic scanner:

- Starts when the manager starts
- Stops gracefully when the manager shuts down
- Respects context cancellation for clean shutdown
- Automatically sets up field indexing for efficient PV lookups

### Safety Features

- **Shared Volume Management**: Uses the same `LVMVolumeManager` interface as the PV controller for consistency
- **Unmounting Before Deletion**: Calls `UnmountVolume` before deleting to ensure clean cleanup
- **Driver Verification**: Only processes volumes that match the local CSI driver format
- **Field Index Safety**: Only indexes PVs managed by the local CSI driver
- **Graceful Shutdown**: Respects context cancellation for immediate stop during shutdown
- **Error Tolerance**: Continues processing other volumes even if individual cleanup operations fail

### Volume Deletion Process

Both controllers follow the same deletion process through the shared interface:

1. **Get Device Path**: Use `GetNodeDevicePath()` to find the volume's device path
2. **Unmount Volume**: Call `UnmountVolume()` to cleanly unmount any mounted filesystem
3. **Delete Volume**: Use `DeleteVolume()` to remove the LVM logical volume
4. **Handle Errors**: Use `lvmMgr.IgnoreNotFound()` to handle already-deleted volumes gracefully

The combination of both controllers ensures comprehensive and efficient garbage collection of orphaned LVM volumes through both reactive (event-driven) and proactive (periodic) approaches.

---

## Command-Line Configuration

Both controllers can be enabled or disabled independently using command-line flags:

### Flags

- **`--enable-pv-garbage-collection`** (default: `true`)
  - Controls the PV Garbage Collection Controller
  - When disabled, the event-driven controller will not be started
  - Example: `--enable-pv-garbage-collection=false`

- **`--enable-lvm-orphan-scanner`** (default: `true`)
  - Controls the LVM Orphan Scanner
  - When disabled, the periodic scanning controller will not be started
  - Example: `--enable-lvm-orphan-scanner=false`

- **`--lvm-orphan-scan-interval`** (default: `10m0s`)
  - Sets the interval for the LVM Orphan Scanner periodic scans
  - Accepts standard Go duration format (e.g., `5m`, `1h`, `30s`)
  - Example: `--lvm-orphan-scan-interval=15m`

### Usage Examples

```bash
# Run with both controllers enabled (default)
./local-csi-driver

# Disable only the PV garbage collection controller
./local-csi-driver --enable-pv-garbage-collection=false

# Disable only the LVM orphan scanner
./local-csi-driver --enable-lvm-orphan-scanner=false

# Disable both controllers
./local-csi-driver --enable-pv-garbage-collection=false --enable-lvm-orphan-scanner=false

# Run with custom scan interval (15 minutes)
./local-csi-driver --lvm-orphan-scan-interval=15m

# Run with custom scan interval (1 hour)
./local-csi-driver --lvm-orphan-scan-interval=1h
```

### Deployment Considerations

- **Production**: Keep both controllers enabled for comprehensive cleanup
- **Testing/Development**: May disable controllers to prevent cleanup during debugging
- **Specific Use Cases**:
  - Disable PV controller if you only want periodic cleanup
  - Disable scanner if you only want event-driven cleanup
  - Disable both if using external volume management

### Log Messages

When controllers are disabled, the application will log:

- `"PV garbage collection controller disabled"`
- `"LVM orphan scanner disabled"`

When enabled, the application will log:

- `"PV garbage collection controller configured"`
- `"LVM orphan scanner configured"`

## Testing

Both controllers include comprehensive unit tests with shared test utilities:

### Test Coverage

- **Node annotation mismatch detection** using the shared `hasNodeAnnotationMismatch` function
- **Volume ID parsing** with the shared `parseVolumeID` function
- **Mock LVM operations** through the unified mock interface
- **Field indexing** for efficient PV lookups in the scanner
- **Reconciliation logic** for both event-driven and periodic controllers

### Running Tests

```bash
# Run all garbage collection tests
go test ./internal/gc/

# Run with verbose output
go test -v ./internal/gc/

# Run specific test
go test ./internal/gc/ -run TestPVFailoverReconciler
```

## Monitoring

Monitor both controllers through:

- **Kubernetes Events**: Check events on PVs and Nodes for cleanup activities
- **Controller Logs**: Look for log entries with components:
  - `"pv-failover-reconciler"` for the event-driven controller
  - `"lvm-orphan-scanner"` for the periodic scanner
- **Metrics**: Standard controller-runtime metrics are available
- **Field Index Metrics**: Monitor PV indexing operations in the scanner logs

## Integration

Both controllers integrate seamlessly with the existing local CSI driver:

- **Shared Interface**: Both use the unified `LVMVolumeManager` interface
- **Shared Components**: Common LVM manager, mounter, and core instances
- **Shared Utilities**: Common functions for node annotation checking and volume parsing
- **Consistent Behavior**: Identical cleanup logic ensures predictable behavior
- **Same Annotations**: Uses the same node annotation constants
- **CSI Driver Compatibility**: Respects the same CSI driver identification
- **Non-Conflicting**: Operates alongside existing CSI controllers without conflicts
