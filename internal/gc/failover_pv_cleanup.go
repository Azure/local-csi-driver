// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package gc

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	kevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"local-csi-driver/internal/csi/core/lvm"
	"local-csi-driver/internal/csi/mounter"
	"local-csi-driver/internal/pkg/events"
	lvmMgr "local-csi-driver/internal/pkg/lvm"
)

// PVFailoverReconciler monitors PersistentVolumes for node annotation mismatches
// and cleans up associated LVM volumes when the volume is no longer on the correct node.
type PVFailoverReconciler struct {
	client.Client
	scheme                   *runtime.Scheme
	recorder                 kevents.EventRecorder
	nodeID                   string
	selectedNodeAnnotation   string
	selectedInitialNodeParam string
	lvmManager               LVMVolumeManager
}

// NewPVFailoverReconciler creates a new PVFailoverReconciler.
func NewPVFailoverReconciler(
	client client.Client,
	scheme *runtime.Scheme,
	recorder kevents.EventRecorder,
	nodeID string,
	selectedNodeAnnotation string,
	selectedInitialNodeParam string,
	lvmCore *lvm.LVM,
	lvmManager lvmMgr.Manager,
	mounter mounter.Interface,
) *PVFailoverReconciler {
	return &PVFailoverReconciler{
		Client:                   client,
		scheme:                   scheme,
		recorder:                 recorder,
		nodeID:                   nodeID,
		selectedNodeAnnotation:   selectedNodeAnnotation,
		selectedInitialNodeParam: selectedInitialNodeParam,
		lvmManager:               &lvmVolumeManagerAdapter{lvmCore: lvmCore, lvmManager: lvmManager, mounter: mounter},
	}
}

// Reconcile implements the controller-runtime Reconciler interface.
func (r *PVFailoverReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx).WithName("pv-failover-reconciler").WithValues("pv", req.Name)

	log.V(4).Info("PV failover reconciler triggered")

	// Get the PersistentVolume
	pv := &corev1.PersistentVolume{}
	if err := r.Get(ctx, req.NamespacedName, pv); err != nil {
		if errors.IsNotFound(err) {
			log.V(4).Info("PV not found, likely deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get PersistentVolume")
		return ctrl.Result{}, err
	}

	// Only process our CSI driver's volumes
	if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != lvm.DriverName {
		log.V(4).Info("PV is not managed by our CSI driver, skipping", "driver", pv.Spec.CSI.Driver)
		return ctrl.Result{}, nil
	}

	// Skip if PV is not bound or is being deleted
	if pv.Status.Phase != corev1.VolumeAvailable && pv.Status.Phase != corev1.VolumeBound {
		log.V(4).Info("PV is not in available or bound state, skipping", "phase", pv.Status.Phase)
		return ctrl.Result{}, nil
	}

	// Skip if PV has a deletion timestamp (being deleted)
	if pv.DeletionTimestamp != nil {
		log.V(4).Info("PV is being deleted, skipping")
		return ctrl.Result{}, nil
	}

	// Add events context for this PV
	ctx = events.WithObjectIntoContext(ctx, r.recorder, pv)

	// Check for node annotation mismatch
	if hasNodeAnnotationMismatch(pv, r.nodeID, r.selectedNodeAnnotation, r.selectedInitialNodeParam) {
		log.Info("Detected node annotation mismatch, checking if LVM volume should be cleaned up")

		// Check if the LVM volume actually exists on this node
		volumeID := pv.Spec.CSI.VolumeHandle
		devicePath, err := r.lvmManager.GetNodeDevicePath(volumeID)
		if err != nil {
			log.V(4).Info("Volume not found on this node, no cleanup needed", "error", err.Error())
			return ctrl.Result{}, nil
		}

		if devicePath == "" {
			log.V(4).Info("Volume device path is empty, volume likely not on this node")
			return ctrl.Result{}, nil
		}

		log.Info("Volume exists on this node but annotations indicate it should be elsewhere, cleaning up",
			"volumeID", volumeID, "devicePath", devicePath)

		// Record event before deletion
		r.recorder.Eventf(pv, nil, corev1.EventTypeNormal, "CleaningUpOrphanedVolume", "CleaningUpOrphanedVolume",
			"Cleaning up LVM volume %s from node %s due to node annotation mismatch", volumeID, r.nodeID)

		// Unmount the volume before deletion
		if devicePath != "" {
			log.V(2).Info("Unmounting volume before deletion", "devicePath", devicePath)
			if err := r.lvmManager.UnmountVolume(ctx, devicePath); err != nil {
				log.Error(err, "Failed to unmount device path, proceeding with deletion anyway", "devicePath", devicePath)
				// Continue with deletion even if unmount fails
			} else {
				log.V(2).Info("Successfully unmounted device", "devicePath", devicePath)
			}
		}

		// Delete the LVM volume
		if err := r.lvmManager.DeleteVolume(ctx, volumeID); err != nil {
			log.Error(err, "Failed to delete orphaned LVM volume", "volumeID", volumeID)
			r.recorder.Eventf(pv, nil, corev1.EventTypeWarning, "CleanupFailed", "CleanupFailed",
				"Failed to cleanup orphaned LVM volume %s from node %s: %v", volumeID, r.nodeID, err)
			// Requeue for retry
			return ctrl.Result{}, err
		}

		log.Info("Successfully cleaned up orphaned LVM volume", "volumeID", volumeID)
		r.recorder.Eventf(pv, nil, corev1.EventTypeNormal, "CleanedUpOrphanedVolume", "CleanedUpOrphanedVolume",
			"Successfully cleaned up orphaned LVM volume %s from node %s", volumeID, r.nodeID)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PVFailoverReconciler) SetupWithManager(mgr ctrl.Manager) error {
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

			// Add debug logging
			logger := log.Log.WithName("pv-gc-predicate").WithValues("pv", pv.Name)

			// Only process PVs managed by our CSI driver
			isOurDriver := pv.Spec.CSI != nil && pv.Spec.CSI.Driver == lvm.DriverName
			if isOurDriver {
				// Also check if this is an annotation change that we care about
				oldPV, oldOk := e.ObjectOld.(*corev1.PersistentVolume)
				if oldOk {
					oldSelectedNode, oldSelectedNodeAnnotationExists := oldPV.Annotations[r.selectedNodeAnnotation]
					newSelectedNode, newSelectedNodeAnnotationExists := pv.Annotations[r.selectedNodeAnnotation]
					if !newSelectedNodeAnnotationExists {
						// No new annotation - nothing to do
						return false
					}
					// If old annotation doesn't exist, check initial annotation
					if !oldSelectedNodeAnnotationExists {
						oldSelectedNode = oldPV.Spec.CSI.VolumeAttributes[r.selectedInitialNodeParam]
					}
					if oldSelectedNode != newSelectedNode && oldSelectedNode == r.nodeID {
						logger.V(1).Info("PV moved to a new node",
							"old", oldSelectedNode, "new", newSelectedNode)
						return true
					}
				}
			} else {
				logger.V(4).Info("Ignoring update event - not our CSI driver", "driver", func() string {
					if pv.Spec.CSI != nil {
						return pv.Spec.CSI.Driver
					}
					return "nil"
				}())
			}

			return false
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
