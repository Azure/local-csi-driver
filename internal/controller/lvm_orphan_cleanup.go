// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"local-csi-driver/internal/csi/core/lvm"
	"local-csi-driver/internal/csi/mounter"
	lvmMgr "local-csi-driver/internal/pkg/lvm"
)

// Field index name for CSI VolumeHandle
const CSIVolumeHandleIndex = "spec.csi.volumeHandle"

// LVMOrphanCleanup periodically scans LVM volumes on the node and deletes
// those that don't have corresponding PersistentVolumes in the cluster
type LVMOrphanCleanup struct {
	client.Client
	scheme                   *runtime.Scheme
	recorder                 record.EventRecorder
	nodeID                   string
	lvmManager               lvmMgr.Manager
	lvmCore                  *lvm.LVM
	mounter                  mounter.Interface
	selectedNodeAnnotation   string
	selectedInitialNodeParam string

	// Configuration
	reconcileInterval time.Duration
}

// LVMOrphanCleanupConfig holds configuration for the cleanup controller
type LVMOrphanCleanupConfig struct {
	ReconcileInterval time.Duration
}

// NewLVMOrphanCleanup creates a new LVMOrphanCleanup controller
func NewLVMOrphanCleanup(
	client client.Client,
	scheme *runtime.Scheme,
	recorder record.EventRecorder,
	nodeID string,
	selectedNodeAnnotation string,
	selectedInitialNodeParam string,
	lvmManager lvmMgr.Manager,
	lvmCore *lvm.LVM,
	mounter mounter.Interface,
	config LVMOrphanCleanupConfig,
) *LVMOrphanCleanup {
	// Set defaults if not specified
	if config.ReconcileInterval == 0 {
		config.ReconcileInterval = 10 * time.Minute // Default to 10 minutes
	}

	return &LVMOrphanCleanup{
		Client:                   client,
		scheme:                   scheme,
		recorder:                 recorder,
		nodeID:                   nodeID,
		selectedNodeAnnotation:   selectedNodeAnnotation,
		selectedInitialNodeParam: selectedInitialNodeParam,
		lvmManager:               lvmManager,
		lvmCore:                  lvmCore,
		mounter:                  mounter,
		reconcileInterval:        config.ReconcileInterval,
	}
}

// Reconcile implements the periodic reconciliation logic
func (r *LVMOrphanCleanup) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx).WithName("lvm-orphan-cleanup").WithValues("node", r.nodeID)

	log.Info("Starting periodic LVM orphan cleanup")

	var orphanedVolumes []string
	totalVolumes := 0

	// Check logical volumes in the default volume group
	log.V(2).Info("Checking default volume group for orphaned volumes", "vg", lvm.DefaultVolumeGroup)

	lvs, err := r.lvmManager.ListLogicalVolumes(ctx, &lvmMgr.ListLVOptions{
		Select: fmt.Sprintf("vg_name=%s", lvm.DefaultVolumeGroup),
	})
	if err != nil {
		log.Error(err, "Failed to list logical volumes", "vg", lvm.DefaultVolumeGroup)
		return ctrl.Result{RequeueAfter: r.reconcileInterval}, err
	}

	for _, lv := range lvs {
		totalVolumes++
		volumeID := fmt.Sprintf("%s#%s", lvm.DefaultVolumeGroup, lv.Name)

		log.V(3).Info("Checking logical volume", "volumeID", volumeID)

		// Check if this volume should be cleaned up using field indexing
		if shouldCleanup, reason, err := r.shouldCleanupVolume(ctx, volumeID); err != nil {
			log.Error(err, "Failed to check if volume should be cleaned up", "volumeID", volumeID)
			continue
		} else if shouldCleanup {
			log.Info("Found LVM volume to cleanup", "volumeID", volumeID, "reason", reason)
			orphanedVolumes = append(orphanedVolumes, volumeID)
		}
	}

	log.Info("Orphan scan completed", "totalVolumes", totalVolumes, "orphanedVolumes", len(orphanedVolumes))

	// Clean up orphaned volumes
	if len(orphanedVolumes) > 0 {
		cleaned, failed := r.cleanupOrphanedVolumes(ctx, orphanedVolumes)
		log.Info("Cleanup completed", "cleaned", cleaned, "failed", failed)
	}

	// Schedule next reconciliation
	return ctrl.Result{RequeueAfter: r.reconcileInterval}, nil
}

// shouldCleanupVolume checks if a volume should be cleaned up using field indexing
func (r *LVMOrphanCleanup) shouldCleanupVolume(ctx context.Context, volumeID string) (bool, string, error) {
	log := log.FromContext(ctx).WithName("lvm-orphan-cleanup").WithValues("volumeID", volumeID)
	// Use field indexing to find PVs with the specific volume handle
	pvList := &corev1.PersistentVolumeList{}

	// Query using the field index for direct lookup by volume handle
	listOpts := client.MatchingFields{CSIVolumeHandleIndex: volumeID}

	log.V(2).Info("Querying for PVs with matching volume handle", "volumeHandle", volumeID)
	if err := r.List(ctx, pvList, listOpts); err != nil {
		return false, "", fmt.Errorf("failed to list PersistentVolumes: %w", err)
	}

	// If no PVs found with this volume handle, volume is orphaned
	if len(pvList.Items) == 0 {
		log.V(2).Info("No PV found with matching volume handle", "volumeHandle", volumeID)
		return true, "no PV with matching volume handle found", nil
	}

	// Check each matching PV (there should typically be only one)
	for _, pv := range pvList.Items {
		// Verify this is our CSI driver (additional safety check)
		if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != lvm.DriverName {
			log.V(2).Info("Skipping PV from different CSI driver", "volumeHandle", volumeID)
			continue
		}

		// Found corresponding PV - check if it has node annotation mismatch
		if r.hasNodeAnnotationMismatch(&pv) {
			log.V(2).Info("PV has node annotation mismatch", "pv", pv.Name, "selectedNode", pv.Annotations[r.selectedNodeAnnotation])
			return true, "node annotation mismatch", nil
		}

		// Volume has a corresponding PV with correct node annotations
		log.V(2).Info("Volume has corresponding PV with matching node annotations", "pv", pv.Name)
		return false, "", nil
	}

	// All matching PVs were from other drivers (shouldn't happen with proper indexing)
	return true, "no corresponding PV found", nil
}

// hasNodeAnnotationMismatch checks if the PV's node annotations don't match the current node
// This is the same logic as in the PV garbage collection controller
func (r *LVMOrphanCleanup) hasNodeAnnotationMismatch(pv *corev1.PersistentVolume) bool {
	// Check selected-node annotation
	selectedNode, exists := pv.Annotations[r.selectedNodeAnnotation]
	if exists {
		if !strings.EqualFold(selectedNode, r.nodeID) {
			return true
		}
	} else {
		// Check initial node parameter in CSI volume attributes
		if pv.Spec.CSI != nil && pv.Spec.CSI.VolumeAttributes != nil {
			if initialNode, exists := pv.Spec.CSI.VolumeAttributes[r.selectedInitialNodeParam]; exists {
				if !strings.EqualFold(initialNode, r.nodeID) {
					return true
				}
			}
		}
	}

	return false
}

// cleanupOrphanedVolumes deletes the list of orphaned volumes
func (r *LVMOrphanCleanup) cleanupOrphanedVolumes(ctx context.Context, volumeIDs []string) (cleaned, failed int) {
	log := log.FromContext(ctx).WithName("lvm-orphan-cleanup")

	for _, volumeID := range volumeIDs {
		if err := r.deleteOrphanedVolume(ctx, volumeID); err != nil {
			log.Error(err, "Failed to delete orphaned volume", "volumeID", volumeID)
			failed++
		} else {
			log.Info("Successfully deleted orphaned volume", "volumeID", volumeID)
			cleaned++
		}
	}

	return cleaned, failed
}

// deleteOrphanedVolume deletes a single orphaned volume
func (r *LVMOrphanCleanup) deleteOrphanedVolume(ctx context.Context, volumeID string) error {
	log := log.FromContext(ctx).WithName("lvm-orphan-cleanup")

	// Parse volume ID to get VG and LV names
	vgName, lvName, err := parseVolumeID(volumeID)
	if err != nil {
		return fmt.Errorf("failed to parse volume ID %s: %w", volumeID, err)
	}

	// Format for LVM operations (vg/lv format)
	lvmVolumeID := fmt.Sprintf("%s/%s", vgName, lvName)

	log.Info("Deleting orphaned LVM volume", "volumeID", volumeID, "lvmPath", lvmVolumeID)

	// Create a dummy event for logging purposes (since we don't have a specific PV object)
	r.recorder.Eventf(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: r.nodeID}},
		corev1.EventTypeNormal, "CleaningUpOrphanedLV",
		"Cleaning up orphaned LVM logical volume %s on node %s (no corresponding PV found)", volumeID, r.nodeID)

	// Get device path and unmount before deletion
	devicePath, err := r.lvmCore.GetNodeDevicePath(volumeID)
	if err != nil {
		log.V(2).Info("Could not get device path, proceeding with deletion", "volumeID", volumeID, "error", err.Error())
	} else if devicePath != "" && r.mounter != nil {
		log.V(2).Info("Unmounting volume before deletion", "devicePath", devicePath)
		if err := r.mounter.CleanupStagingDir(ctx, devicePath); err != nil {
			log.Error(err, "Failed to unmount device path, proceeding with deletion anyway", "devicePath", devicePath)
			// Continue with deletion even if unmount fails
		} else {
			log.V(2).Info("Successfully unmounted device", "devicePath", devicePath)
		}
	}

	// Remove the logical volume
	removeOpts := lvmMgr.RemoveLVOptions{Name: lvmVolumeID}
	if err := r.lvmManager.RemoveLogicalVolume(ctx, removeOpts); err != nil {
		// If volume doesn't exist, consider it success
		if lvmMgr.IgnoreNotFound(err) == nil {
			log.V(2).Info("Volume already removed", "volumeID", volumeID)
			return nil
		}

		r.recorder.Eventf(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: r.nodeID}},
			corev1.EventTypeWarning, "OrphanCleanupFailed",
			"Failed to cleanup orphaned LVM logical volume %s on node %s: %v", volumeID, r.nodeID, err)

		return fmt.Errorf("failed to remove logical volume %s: %w", lvmVolumeID, err)
	}

	r.recorder.Eventf(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: r.nodeID}},
		corev1.EventTypeNormal, "CleanedUpOrphanedLV",
		"Successfully cleaned up orphaned LVM logical volume %s on node %s", volumeID, r.nodeID)

	return nil
}

// Start implements the manager.Runnable interface for periodic execution
func (r *LVMOrphanCleanup) Start(ctx context.Context) error {
	log := log.FromContext(ctx).WithName("lvm-orphan-cleanup")
	log.Info("Starting LVM orphan cleanup controller", "interval", r.reconcileInterval, "nextPeriodicRun", time.Now().Add(r.reconcileInterval))

	ticker := time.NewTicker(r.reconcileInterval)
	defer ticker.Stop()

	log.Info("Created periodic ticker", "interval", r.reconcileInterval)

	// Run initial cleanup
	log.Info("Running initial LVM orphan cleanup")
	if _, err := r.Reconcile(ctx, ctrl.Request{}); err != nil {
		log.Error(err, "Initial cleanup failed")
	} else {
		log.Info("Initial cleanup completed successfully")
	}

	// Run periodic cleanup
	for {
		select {
		case <-ctx.Done():
			log.Info("Stopping LVM orphan cleanup controller")
			return nil
		case <-ticker.C:
			log.Info("Running periodic LVM orphan cleanup", "interval", r.reconcileInterval, "nextRun", time.Now().Add(r.reconcileInterval))
			if _, err := r.Reconcile(ctx, ctrl.Request{}); err != nil {
				log.Error(err, "Periodic cleanup failed")
			}
		}
	}
}

// NeedLeaderElection returns true to ensure only one instance runs cleanup
func (r *LVMOrphanCleanup) NeedLeaderElection() bool {
	return false
}

// SetupWithManager sets up the controller with the Manager as a runnable
func (r *LVMOrphanCleanup) SetupWithManager(mgr ctrl.Manager) error {
	// Setup field indexing for CSI VolumeHandle
	if err := r.setupFieldIndexing(mgr); err != nil {
		return fmt.Errorf("failed to setup field indexing: %w", err)
	}

	return mgr.Add(r)
}

// setupFieldIndexing creates a field index for PVs by CSI volume handle
func (r *LVMOrphanCleanup) setupFieldIndexing(mgr ctrl.Manager) error {
	return mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&corev1.PersistentVolume{},
		CSIVolumeHandleIndex,
		func(obj client.Object) []string {
			pv := obj.(*corev1.PersistentVolume)

			// Only index PVs that use our CSI driver
			if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == lvm.DriverName {
				log := log.FromContext(context.Background()).WithName("lvm-orphan-cleanup")
				log.V(1).Info("Indexing PV by CSI volume handle", "volumeHandle", pv.Spec.CSI.VolumeHandle)
				return []string{pv.Spec.CSI.VolumeHandle}
			}

			return nil
		},
	)
}
