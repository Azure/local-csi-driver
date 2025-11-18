// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package pvcleanup

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"local-csi-driver/internal/csi/core/lvm"
)

const (
	// PVProtectionFinalizer is the standard Kubernetes finalizer that prevents PV deletion.
	PVProtectionFinalizer = "kubernetes.io/pv-protection"

	// ExternalProvisionerFinalizer is the finalizer set by the external provisioner.
	ExternalProvisionerFinalizer = "external-provisioner.volume.kubernetes.io/finalizer"
)

// PVCleanupReconciler watches PV delete events and removes finalizer if node doesn't exist.
type PVCleanupReconciler struct {
	client.Client
	Recorder record.EventRecorder
}

// Reconcile handles PV reconciliation.
func (r *PVCleanupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the PV
	var pv corev1.PersistentVolume
	if err := r.Get(ctx, req.NamespacedName, &pv); err != nil {
		if apierrors.IsNotFound(err) {
			// PV was deleted, nothing to do
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Only process PVs from our CSI driver
	if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != lvm.DriverName {
		logger.V(4).Info("PV is not from our CSI driver, skipping", "pv", pv.Name, "driver", pv.Spec.CSI)
		return ctrl.Result{}, nil
	}

	// Only process PVs that are Released with Delete reclaim policy
	if pv.Status.Phase != corev1.VolumeReleased {
		// PV is not released, nothing to do
		return ctrl.Result{}, nil
	}

	if pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimDelete {
		// PV doesn't have Delete reclaim policy, nothing to do
		logger.V(4).Info("PV doesn't have Delete reclaim policy, skipping", "pv", pv.Name, "reclaimPolicy", pv.Spec.PersistentVolumeReclaimPolicy)
		return ctrl.Result{}, nil
	}

	hasPVProtection := ctrlutil.ContainsFinalizer(&pv, PVProtectionFinalizer)
	hasExternalProvisioner := ctrlutil.ContainsFinalizer(&pv, ExternalProvisionerFinalizer)

	if !hasPVProtection && !hasExternalProvisioner {
		// PV doesn't have any finalizers we care about, nothing to do
		return ctrl.Result{}, nil
	}

	logger.V(2).Info("PV is released with delete reclaim policy", "pv", pv.Name, "driver", pv.Spec.CSI.Driver)

	// Extract hostnames from PV's node affinity
	hostnames := extractHostnamesFromPV(&pv)
	if len(hostnames) == 0 {
		logger.Info("PV has no hostname topology constraints, skipping finalizer removal", "pv", pv.Name)
		return ctrl.Result{}, nil
	}

	logger.V(2).Info("PV has hostname topology", "pv", pv.Name, "hostnames", hostnames)

	// Check if any of the hostnames are available as nodes in the cluster
	hasAvailableNode := false
	for _, hostname := range hostnames {
		var node corev1.Node
		if err := r.Get(ctx, client.ObjectKey{Name: hostname}, &node); err == nil {
			// Node exists, check if it's ready
			if isNodeReady(&node) {
				hasAvailableNode = true
				logger.V(2).Info("Found available node for PV", "pv", pv.Name, "node", hostname)
				break
			}
		}
	}

	if hasAvailableNode {
		logger.Info("PV has at least one available node, keeping finalizer", "pv", pv.Name)
		return ctrl.Result{}, nil
	}

	logger.V(2).Info("None of the PV's hostname nodes are available, removing finalizers", "pv", pv.Name, "hostnames", hostnames)

	// If the PV deletion timestamp is not set, issue a delete request first
	if pv.DeletionTimestamp.IsZero() {
		logger.Info("PV deletion timestamp not set, issuing delete request", "pv", pv.Name)
		if err := r.Delete(ctx, &pv); err != nil {
			r.Recorder.Eventf(&pv, corev1.EventTypeWarning, "DeleteFailed",
				"Failed to delete PV: %v", err)
			return ctrl.Result{}, err
		}
		logger.V(2).Info("Successfully issued PV delete request", "pv", pv.Name)
		r.Recorder.Event(&pv, corev1.EventTypeNormal, "DeleteIssued",
			"Issued PV delete request because no hostname nodes are available")

		// Requeue to handle finalizer removal after deletion timestamp is set
		return ctrl.Result{Requeue: true}, nil
	}

	logger.V(2).Info("PV has deletion timestamp set, proceeding to remove finalizers", "pv", pv.Name)

	// Remove the finalizers
	patch := client.MergeFrom(pv.DeepCopy())
	finalizersToRemove := []string{}
	if hasPVProtection {
		finalizersToRemove = append(finalizersToRemove, PVProtectionFinalizer)
	}
	if hasExternalProvisioner {
		finalizersToRemove = append(finalizersToRemove, ExternalProvisionerFinalizer)
	}

	for _, finalizer := range finalizersToRemove {
		_ = ctrlutil.RemoveFinalizer(&pv, finalizer)
	}

	if err := r.Patch(ctx, &pv, patch); err != nil {
		// Treat NotFound as success - PV was already deleted
		if apierrors.IsNotFound(err) {
			logger.Info("PV not found during finalizer removal, assuming already deleted", "pv", pv.Name)
			return ctrl.Result{}, nil
		}
		r.Recorder.Eventf(&pv, corev1.EventTypeWarning, "FinalizerRemovalFailed",
			"Failed to remove finalizers: %v", err)
		return ctrl.Result{}, err
	}

	logger.Info("Successfully removed PV finalizers", "pv", pv.Name, "finalizers", finalizersToRemove)
	r.Recorder.Event(&pv, corev1.EventTypeNormal, "FinalizersRemoved",
		"Removed PV finalizers because no hostname nodes are available")

	return ctrl.Result{}, nil
}

// extractHostnamesFromPV extracts hostnames from PV's node affinity
// Specifically looks for topology.localdisk.csi.acstor.io/node topology constraints.
func extractHostnamesFromPV(pv *corev1.PersistentVolume) []string {
	if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
		return nil
	}

	var hostnames []string
	for _, term := range pv.Spec.NodeAffinity.Required.NodeSelectorTerms {
		for _, expr := range term.MatchExpressions {
			if expr.Key == lvm.TopologyKey && expr.Operator == corev1.NodeSelectorOpIn {
				hostnames = append(hostnames, expr.Values...)
			}
		}
	}

	return hostnames
}

// isNodeReady checks if a node is in Ready condition.
func isNodeReady(node *corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *PVCleanupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Create predicate to filter events
	pvPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			pv, ok := e.Object.(*corev1.PersistentVolume)
			if !ok {
				return false
			}
			return pv.Spec.CSI != nil &&
				pv.Spec.CSI.Driver == lvm.DriverName &&
				pv.Status.Phase == corev1.VolumeReleased &&
				pv.Spec.PersistentVolumeReclaimPolicy == corev1.PersistentVolumeReclaimDelete
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			pv, ok := e.ObjectNew.(*corev1.PersistentVolume)
			if !ok {
				return false
			}
			// Only watch PVs from our CSI driver that are Released with Delete reclaim policy
			return pv.Spec.CSI != nil &&
				pv.Spec.CSI.Driver == lvm.DriverName &&
				pv.Status.Phase == corev1.VolumeReleased &&
				pv.Spec.PersistentVolumeReclaimPolicy == corev1.PersistentVolumeReclaimDelete
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false
		},
		GenericFunc: func(e event.GenericEvent) bool {
			// Don't care about generic events
			return false
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.PersistentVolume{}).
		WithEventFilter(pvPredicate).
		Complete(r)
}
