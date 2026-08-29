// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package capacitytemplate publishes one CSIStorageCapacity per
// (StorageClass x node group) so that the Kubernetes Cluster Autoscaler can
// observe non-zero capacity for the local-csi-driver on simulated template
// nodes and trigger scale-up.
//
// A StorageClass opts in by setting the annotation
// "localdisk.csi.acstor.io/template-capacity" to a parseable resource
// quantity (for example, "1800Gi"). The controller groups Nodes by a
// configurable label (default "node.kubernetes.io/instance-type", i.e. VM
// SKU) and publishes one CSIStorageCapacity object per (StorageClass x
// group) pair, scoped to the manager namespace.
//
// The grouping label is configurable: typical choices are
//   - node.kubernetes.io/instance-type (default; VM SKU, portable across
//     cloud providers and well-suited to local NVMe whose capacity is
//     determined by the SKU)
//   - kubernetes.azure.com/agentpool (AKS node pool)
//   - topology.kubernetes.io/zone (failure domain)
//
// Each managed CSIStorageCapacity selects nodes by both the grouping label
// and "cluster-autoscaler.kubernetes.io/template-node=true", which Cluster
// Autoscaler attaches to its simulated template nodes (see
// kubernetes/autoscaler#9702). This restricts these synthetic capacity
// objects to template nodes only, so they do not shadow the accurate
// per-node capacity that the external-provisioner sidecar publishes for
// real nodes.
package capacitytemplate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"local-csi-driver/internal/csi/core/lvm"
)

const (
	// CapacityAnnotation is the StorageClass annotation that opts a class in
	// to template capacity publishing and provides the per-pool capacity
	// value as a resource.Quantity (e.g. "1800Gi").
	CapacityAnnotation = "localdisk.csi.acstor.io/template-capacity"

	// ManagedByLabel marks CSIStorageCapacity objects owned by this
	// controller so that stale objects can be reclaimed without touching
	// capacity objects published by the external-provisioner sidecar.
	ManagedByLabel = "localdisk.csi.acstor.io/managed-by"
	ManagedByValue = "capacitytemplate"

	// StorageClassLabel and NodeGroupLabel are added to managed objects so
	// they can be identified without parsing the name.
	StorageClassLabel = "localdisk.csi.acstor.io/storageclass"
	NodeGroupLabel    = "localdisk.csi.acstor.io/node-group"

	// DefaultNodeGroupLabelKey is the well-known instance-type label and the
	// controller's default grouping key. Local NVMe capacity is determined
	// by the VM SKU, so SKU is the natural unit and works on any cloud
	// provider that sets the upstream label.
	DefaultNodeGroupLabelKey = "node.kubernetes.io/instance-type"

	// TemplateNodeLabelKey is the label Cluster Autoscaler attaches to its
	// simulated template nodes. Including it in NodeTopology restricts our
	// synthetic capacity objects to template nodes only, so they don't
	// shadow accurate per-node capacity reported for real nodes by the
	// external-provisioner sidecar. See kubernetes/autoscaler#9702.
	TemplateNodeLabelKey   = "cluster-autoscaler.kubernetes.io/template-node"
	TemplateNodeLabelValue = "true"

	// namePrefix is the prefix used for all CSIStorageCapacity objects
	// managed by this controller.
	namePrefix = "local-csi-template-"

	// resyncRequeue forces a full reconciliation periodically as a safety
	// net against missed events (e.g. StorageClass annotation edits that do
	// not change the resource version we are watching).
	resyncRequeue = 5 * time.Minute

	// dns1123MaxLen is the maximum length for an object name.
	dns1123MaxLen = 253
)

// syntheticRequest is the single key used for full-sync reconciliation. The
// controller does not have a meaningful per-object key because each
// reconciliation evaluates the entire (StorageClass x pool) matrix.
var syntheticRequest = reconcile.Request{
	NamespacedName: types.NamespacedName{Name: "capacitytemplate-sync"},
}

// Reconciler publishes per-node-group CSIStorageCapacity objects.
type Reconciler struct {
	client.Client

	// Namespace is the namespace where CSIStorageCapacity objects are
	// created. Typically the manager pod's namespace.
	Namespace string

	// NodeGroupLabelKey is the Node label whose values define a node group.
	// Defaults to DefaultNodeGroupLabelKey when empty.
	NodeGroupLabelKey string
}

// Reconcile performs a full sync of the desired CSIStorageCapacity set.
func (r *Reconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	groupKey := r.nodeGroupLabelKey()

	// 1. Gather StorageClasses that opt in.
	var scList storagev1.StorageClassList
	if err := r.List(ctx, &scList); err != nil {
		return ctrl.Result{}, fmt.Errorf("list storageclasses: %w", err)
	}
	type scEntry struct {
		name     string
		capacity resource.Quantity
	}
	var classes []scEntry
	for i := range scList.Items {
		sc := &scList.Items[i]
		if sc.Provisioner != lvm.DriverName {
			continue
		}
		raw, ok := sc.Annotations[CapacityAnnotation]
		if !ok {
			continue
		}
		qty, err := resource.ParseQuantity(raw)
		if err != nil {
			logger.Info("skipping StorageClass with invalid template-capacity annotation",
				"storageclass", sc.Name, "value", raw, "error", err.Error())
			continue
		}
		classes = append(classes, scEntry{name: sc.Name, capacity: qty})
	}

	// 2. Gather distinct node-group values from Nodes.
	var nodes corev1.NodeList
	if err := r.List(ctx, &nodes); err != nil {
		return ctrl.Result{}, fmt.Errorf("list nodes: %w", err)
	}
	groups := map[string]struct{}{}
	for i := range nodes.Items {
		v := nodes.Items[i].Labels[groupKey]
		if v == "" {
			continue
		}
		groups[v] = struct{}{}
	}

	// 3. Build desired set and reconcile each.
	type key struct{ sc, group string }
	desired := map[key]struct{}{}
	for _, sc := range classes {
		for group := range groups {
			desired[key{sc.name, group}] = struct{}{}
			if err := r.upsert(ctx, sc.name, sc.capacity, group, groupKey); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// 4. Garbage-collect managed objects that are no longer desired.
	var existing storagev1.CSIStorageCapacityList
	if err := r.List(ctx, &existing,
		client.InNamespace(r.Namespace),
		client.MatchingLabels{ManagedByLabel: ManagedByValue},
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("list managed CSIStorageCapacity: %w", err)
	}
	for i := range existing.Items {
		obj := &existing.Items[i]
		k := key{
			sc:    obj.Labels[StorageClassLabel],
			group: obj.Labels[NodeGroupLabel],
		}
		if _, want := desired[k]; want {
			continue
		}
		if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("delete stale CSIStorageCapacity %s: %w", obj.Name, err)
		}
		logger.Info("deleted stale CSIStorageCapacity", "name", obj.Name,
			"storageclass", k.sc, "nodeGroup", k.group)
	}

	return ctrl.Result{RequeueAfter: resyncRequeue}, nil
}

// upsert creates or updates the CSIStorageCapacity for (sc, group).
func (r *Reconciler) upsert(ctx context.Context, sc string, capacity resource.Quantity, group, groupKey string) error {
	name := objectName(sc, group)
	logger := log.FromContext(ctx).WithValues("name", name, "storageclass", sc, "nodeGroup", group)

	want := &storagev1.CSIStorageCapacity{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: r.Namespace,
			Labels: map[string]string{
				ManagedByLabel:    ManagedByValue,
				StorageClassLabel: sc,
				NodeGroupLabel:    group,
			},
		},
		StorageClassName: sc,
		Capacity:         &capacity,
		NodeTopology: &metav1.LabelSelector{
			MatchLabels: map[string]string{
				groupKey:             group,
				TemplateNodeLabelKey: TemplateNodeLabelValue,
			},
		},
	}

	existing := &storagev1.CSIStorageCapacity{}
	err := r.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: name}, existing)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, want); err != nil {
			return fmt.Errorf("create CSIStorageCapacity %s: %w", name, err)
		}
		logger.Info("created CSIStorageCapacity", "capacity", capacity.String())
		return nil
	}
	if err != nil {
		return fmt.Errorf("get CSIStorageCapacity %s: %w", name, err)
	}

	if capacityEqual(existing, want) && labelsEqual(existing.Labels, want.Labels) &&
		topologyEqual(existing.NodeTopology, want.NodeTopology) &&
		existing.StorageClassName == want.StorageClassName {
		return nil
	}
	existing.Labels = want.Labels
	existing.StorageClassName = want.StorageClassName
	existing.Capacity = want.Capacity
	existing.NodeTopology = want.NodeTopology
	if err := r.Update(ctx, existing); err != nil {
		return fmt.Errorf("update CSIStorageCapacity %s: %w", name, err)
	}
	logger.Info("updated CSIStorageCapacity", "capacity", capacity.String())
	return nil
}

func (r *Reconciler) nodeGroupLabelKey() string {
	if r.NodeGroupLabelKey != "" {
		return r.NodeGroupLabelKey
	}
	return DefaultNodeGroupLabelKey
}

// SetupWithManager wires the reconciler to relevant watches. All events
// collapse into a single synthetic request so the reconciliation is a full
// sync rather than a per-object update.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Namespace == "" {
		return fmt.Errorf("capacitytemplate: Namespace must be set")
	}

	enqueueAll := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{syntheticRequest}
	})

	groupKey := r.nodeGroupLabelKey()
	nodePred := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return e.Object.GetLabels()[groupKey] != ""
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return e.Object.GetLabels()[groupKey] != ""
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return e.ObjectOld.GetLabels()[groupKey] != e.ObjectNew.GetLabels()[groupKey]
		},
		GenericFunc: func(event.GenericEvent) bool { return false },
	}

	scPred := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		sc, ok := obj.(*storagev1.StorageClass)
		if !ok {
			return false
		}
		return sc.Provisioner == lvm.DriverName
	})

	managedPred := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetLabels()[ManagedByLabel] == ManagedByValue
	})

	return ctrl.NewControllerManagedBy(mgr).
		Named("capacitytemplate").
		For(&storagev1.StorageClass{}, builder.WithPredicates(scPred)).
		Watches(&corev1.Node{}, enqueueAll, builder.WithPredicates(nodePred)).
		Watches(&storagev1.CSIStorageCapacity{}, enqueueAll, builder.WithPredicates(managedPred)).
		Complete(r)
}

// objectName produces a deterministic, DNS-1123 compliant name for the
// CSIStorageCapacity object of a (storageClass, group) pair. Long inputs are
// truncated and disambiguated with a short hash.
func objectName(sc, group string) string {
	base := namePrefix + sanitize(sc) + "-" + sanitize(group)
	if len(base) <= dns1123MaxLen {
		return base
	}
	sum := sha256.Sum256([]byte(sc + "/" + group))
	suffix := "-" + hex.EncodeToString(sum[:4])
	return base[:dns1123MaxLen-len(suffix)] + suffix
}

// sanitize replaces characters not allowed in DNS-1123 labels with '-'.
func sanitize(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func capacityEqual(a, b *storagev1.CSIStorageCapacity) bool {
	if a.Capacity == nil || b.Capacity == nil {
		return a.Capacity == b.Capacity
	}
	return a.Capacity.Cmp(*b.Capacity) == 0
}

func labelsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func topologyEqual(a, b *metav1.LabelSelector) bool {
	if a == nil || b == nil {
		return a == b
	}
	if len(a.MatchLabels) != len(b.MatchLabels) || len(a.MatchExpressions) != len(b.MatchExpressions) {
		return false
	}
	for k, v := range a.MatchLabels {
		if b.MatchLabels[k] != v {
			return false
		}
	}
	return true
}
