// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"local-csi-driver/internal/csi/core/lvm"
	"local-csi-driver/internal/pkg/events"
	lvmMgr "local-csi-driver/internal/pkg/lvm"
)

// PVGarbageCollector monitors PersistentVolumes for node annotation mismatches
// and cleans up associated LVM volumes when the volume is no longer on the correct node.
type PVGarbageCollector struct {
	client.Client
	scheme                   *runtime.Scheme
	recorder                 record.EventRecorder
	nodeID                   string
	selectedNodeAnnotation   string
	selectedInitialNodeParam string
	lvmManager               LVMVolumeManager
}

// LVMVolumeManager provides an interface for LVM volume operations
type LVMVolumeManager interface {
	// DeleteVolume deletes an LVM logical volume by volume ID
	DeleteVolume(ctx context.Context, volumeID string) error
	// GetVolumeName extracts the volume name from a volume ID
	GetVolumeName(volumeID string) (string, error)
	// GetNodeDevicePath returns the device path for a volume ID
	GetNodeDevicePath(volumeID string) (string, error)
}

// lvmVolumeManagerAdapter adapts the LVM core interface to our controller needs
type lvmVolumeManagerAdapter struct {
	lvmCore    *lvm.LVM
	lvmManager lvmMgr.Manager
}

func (a *lvmVolumeManagerAdapter) DeleteVolume(ctx context.Context, volumeID string) error {
	// Parse the volume ID to get volume group and logical volume names
	// Volume ID format is: <volume-group>#<logical-volume>
	vgName, lvName, err := parseVolumeID(volumeID)
	if err != nil {
		return fmt.Errorf("failed to parse volume ID %s: %w", volumeID, err)
	}

	// Format volume ID for LVM operations (vg/lv format)
	lvmVolumeID := fmt.Sprintf("%s/%s", vgName, lvName)

	// Use the LVM manager to remove the logical volume directly
	removeOpts := lvmMgr.RemoveLVOptions{Name: lvmVolumeID}

	if err := a.lvmManager.RemoveLogicalVolume(ctx, removeOpts); err != nil {
		// If the volume doesn't exist, consider it a success
		if lvmMgr.IgnoreNotFound(err) == nil {
			return nil
		}
		return fmt.Errorf("failed to remove logical volume %s: %w", lvmVolumeID, err)
	}

	return nil
}

func (a *lvmVolumeManagerAdapter) GetVolumeName(volumeID string) (string, error) {
	return a.lvmCore.GetVolumeName(volumeID)
}

func (a *lvmVolumeManagerAdapter) GetNodeDevicePath(volumeID string) (string, error) {
	return a.lvmCore.GetNodeDevicePath(volumeID)
}

// NewPVGarbageCollector creates a new PVGarbageCollector
func NewPVGarbageCollector(
	client client.Client,
	scheme *runtime.Scheme,
	recorder record.EventRecorder,
	nodeID string,
	selectedNodeAnnotation string,
	selectedInitialNodeParam string,
	lvmCore *lvm.LVM,
	lvmManager lvmMgr.Manager,
) *PVGarbageCollector {
	return &PVGarbageCollector{
		Client:                   client,
		scheme:                   scheme,
		recorder:                 recorder,
		nodeID:                   nodeID,
		selectedNodeAnnotation:   selectedNodeAnnotation,
		selectedInitialNodeParam: selectedInitialNodeParam,
		lvmManager:               &lvmVolumeManagerAdapter{lvmCore: lvmCore, lvmManager: lvmManager},
	}
}

// Reconcile implements the controller-runtime Reconciler interface
func (r *PVGarbageCollector) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx).WithName("pv-garbage-collector").WithValues("pv", req.NamespacedName.Name)

	// Get the PersistentVolume
	pv := &corev1.PersistentVolume{}
	if err := r.Get(ctx, req.NamespacedName, pv); err != nil {
		if errors.IsNotFound(err) {
			log.V(2).Info("PV not found, likely deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get PersistentVolume")
		return ctrl.Result{}, err
	}

	// Only process our CSI driver's volumes
	if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != lvm.DriverName {
		log.V(2).Info("PV is not managed by our CSI driver, skipping", "driver", pv.Spec.CSI.Driver)
		return ctrl.Result{}, nil
	}

	// Skip if PV is not bound or is being deleted
	if pv.Status.Phase != corev1.VolumeAvailable && pv.Status.Phase != corev1.VolumeBound {
		log.V(2).Info("PV is not in available or bound state, skipping", "phase", pv.Status.Phase)
		return ctrl.Result{}, nil
	}

	// Skip if PV has a deletion timestamp (being deleted)
	if pv.DeletionTimestamp != nil {
		log.V(2).Info("PV is being deleted, skipping")
		return ctrl.Result{}, nil
	}

	// Add events context for this PV
	ctx = events.WithObjectIntoContext(ctx, r.recorder, pv)

	// Check for node annotation mismatch
	if r.hasNodeAnnotationMismatch(pv) {
		log.Info("Detected node annotation mismatch, checking if LVM volume should be cleaned up")

		// Check if the LVM volume actually exists on this node
		volumeID := pv.Spec.CSI.VolumeHandle
		devicePath, err := r.lvmManager.GetNodeDevicePath(volumeID)
		if err != nil {
			log.V(2).Info("Volume not found on this node, no cleanup needed", "error", err.Error())
			return ctrl.Result{}, nil
		}

		if devicePath == "" {
			log.V(2).Info("Volume device path is empty, volume likely not on this node")
			return ctrl.Result{}, nil
		}

		log.Info("Volume exists on this node but annotations indicate it should be elsewhere, cleaning up",
			"volumeID", volumeID, "devicePath", devicePath)

		// Record event before deletion
		r.recorder.Eventf(pv, corev1.EventTypeNormal, "CleaningUpOrphanedVolume",
			"Cleaning up LVM volume %s from node %s due to node annotation mismatch", volumeID, r.nodeID)

		// Delete the LVM volume
		if err := r.lvmManager.DeleteVolume(ctx, volumeID); err != nil {
			log.Error(err, "Failed to delete orphaned LVM volume", "volumeID", volumeID)
			r.recorder.Eventf(pv, corev1.EventTypeWarning, "CleanupFailed",
				"Failed to cleanup orphaned LVM volume %s from node %s: %v", volumeID, r.nodeID, err)
			// Requeue for retry
			return ctrl.Result{RequeueAfter: time.Minute * 5}, err
		}

		log.Info("Successfully cleaned up orphaned LVM volume", "volumeID", volumeID)
		r.recorder.Eventf(pv, corev1.EventTypeNormal, "CleanedUpOrphanedVolume",
			"Successfully cleaned up orphaned LVM volume %s from node %s", volumeID, r.nodeID)
	}

	return ctrl.Result{}, nil
}

// hasNodeAnnotationMismatch checks if the PV's node annotations don't match the current node
func (r *PVGarbageCollector) hasNodeAnnotationMismatch(pv *corev1.PersistentVolume) bool {
	// Check selected-node annotation
	if selectedNode, exists := pv.Annotations[r.selectedNodeAnnotation]; exists {
		if !strings.EqualFold(selectedNode, r.nodeID) {
			return true
		}
	}

	// Check initial node parameter in CSI volume attributes
	if pv.Spec.CSI != nil && pv.Spec.CSI.VolumeAttributes != nil {
		if initialNode, exists := pv.Spec.CSI.VolumeAttributes[r.selectedInitialNodeParam]; exists {
			if !strings.EqualFold(initialNode, r.nodeID) {
				return true
			}
		}
	}

	return false
}

// parseVolumeID parses a volume ID in the format <volume-group>#<logical-volume>
// and returns the volume group name and logical volume name
func parseVolumeID(volumeID string) (vgName, lvName string, err error) {
	const separator = "#"
	segments := strings.Split(volumeID, separator)
	if len(segments) != 2 {
		return "", "", fmt.Errorf("error parsing volume id: %q, expected 2 segments, got %d", volumeID, len(segments))
	}

	vgName = segments[0]
	lvName = segments[1]

	if len(vgName) == 0 {
		return "", "", fmt.Errorf("error parsing volume id: %q, volume group name is empty", volumeID)
	}
	if len(lvName) == 0 {
		return "", "", fmt.Errorf("error parsing volume id: %q, logical volume name is empty", volumeID)
	}

	return vgName, lvName, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *PVGarbageCollector) SetupWithManager(mgr ctrl.Manager) error {
	// Create a predicate to only watch PVs managed by our CSI driver and only on update events
	driverPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			// Don't reconcile on create events - new PVs won't have orphaned volumes
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			pv, ok := e.ObjectNew.(*corev1.PersistentVolume)
			if !ok {
				return false
			}
			// Only process PVs managed by our CSI driver
			return pv.Spec.CSI != nil && pv.Spec.CSI.Driver == lvm.DriverName
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			// Don't reconcile on delete events - PV is being removed anyway
			return false
		},
		GenericFunc: func(e event.GenericEvent) bool {
			pv, ok := e.Object.(*corev1.PersistentVolume)
			if !ok {
				return false
			}
			// Only process PVs managed by our CSI driver
			return pv.Spec.CSI != nil && pv.Spec.CSI.Driver == lvm.DriverName
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.PersistentVolume{}).
		WithEventFilter(driverPredicate).
		Complete(r)
}
