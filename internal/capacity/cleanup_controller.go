// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package capacity

import (
	"context"
	"strings"

	"github.com/gotidy/ptr"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	// Labels used to identify CSIStorageCapacity objects provisioner.
	driverNameLabel = "csi.storage.k8s.io/drivername"
	// Kind values for owner references.
	daemonSetKind = "DaemonSet"
	// Event type for cleanup.
	cleanupEvent = "CleanedUpStaleCSIStorageCapacity"
)

// cleanupController reconciles CSIStorageCapacity objects.
type cleanupController struct {
	client      client.Client
	scheme      *runtime.Scheme
	recorder    record.EventRecorder
	nodeName    string
	driverName  string
	topologyKey string
}

// NewCleanupController creates a new CSICapacityCleanupController.
func NewCleanupController(mgr manager.Manager, nodeName, driverName, topologyKey string) (*cleanupController, error) {
	return &cleanupController{
		client:      mgr.GetClient(),
		scheme:      mgr.GetScheme(),
		recorder:    mgr.GetEventRecorderFor("csicapacitycleanupcontroller"),
		nodeName:    nodeName,
		driverName:  driverName,
		topologyKey: topologyKey,
	}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *cleanupController) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&storagev1.CSIStorageCapacity{}).
		WithEventFilter(r.definePredicate()).
		WithOptions(controller.Options{
			NeedLeaderElection: ptr.Of(false),
		}).
		Complete(r)
}

// Reconcile processes CSIStorageCapacity objects and deletes those that match our criteria.
func (r *cleanupController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx).WithValues("csistoragecapacity", req.NamespacedName)
	capacity := &storagev1.CSIStorageCapacity{}
	if err := r.client.Get(ctx, req.NamespacedName, capacity); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if capacity.DeletionTimestamp != nil {
		log.V(3).Info("CSIStorageCapacity already marked for deletion", "name", req.NamespacedName)
		return ctrl.Result{}, nil
	}

	labels := capacity.GetLabels()
	if labels == nil || labels[driverNameLabel] != r.driverName {
		log.V(3).Info("CSIStorageCapacity does not match driver name", "name", req.NamespacedName, "driverName", r.driverName)
		return ctrl.Result{}, nil
	}

	if capacity.NodeTopology == nil || capacity.NodeTopology.MatchLabels == nil || capacity.NodeTopology.MatchLabels[r.topologyKey] != r.nodeName {
		log.V(3).Info("CSIStorageCapacity does not match node topology", "name", req.NamespacedName, "nodeName", r.nodeName)
		return ctrl.Result{}, nil
	}

	owners := capacity.GetOwnerReferences()
	for _, owner := range owners {
		if strings.EqualFold(owner.Kind, daemonSetKind) && ptr.ToBool(owner.Controller) {
			return r.deleteCapacity(ctx, capacity, req)
		}
	}
	return ctrl.Result{}, nil
}

func (r *cleanupController) deleteCapacity(ctx context.Context, capacity *storagev1.CSIStorageCapacity, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx).WithValues("csistoragecapacity", req.NamespacedName)
	if err := r.client.Delete(ctx, capacity); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.V(1).Info("Stale CSIStorageCapacity owned by DaemonSet marked for deletion", "name", req.NamespacedName)
	r.recorder.Event(capacity, corev1.EventTypeNormal, cleanupEvent, "Stale CSIStorageCapacity owned by DaemonSet marked for deletion")
	return ctrl.Result{}, nil
}

func (r *cleanupController) definePredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			labels := e.Object.GetLabels()
			if labels == nil || labels[driverNameLabel] != r.driverName {
				return false
			}
			capacity, ok := e.Object.(*storagev1.CSIStorageCapacity)
			if !ok || capacity.NodeTopology == nil || capacity.NodeTopology.MatchLabels == nil {
				return false
			}
			return capacity.NodeTopology.MatchLabels[r.topologyKey] == r.nodeName
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false
		},
	}
}
