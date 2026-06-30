// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package capacitytemplate

import (
	"context"
	"sort"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"local-csi-driver/internal/csi/core/lvm"
)

const testNamespace = "kube-system"

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return s
}

func newReconciler(t *testing.T, objs ...client.Object) (*Reconciler, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(newScheme(t)).
		WithObjects(objs...).
		Build()
	return &Reconciler{
		Client:    c,
		Namespace: testNamespace,
	}, c
}

func node(name, group string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{DefaultNodeGroupLabelKey: group},
		},
	}
}

func storageClass(name string, annotations map[string]string) *storagev1.StorageClass {
	return &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: annotations,
		},
		Provisioner: lvm.DriverName,
	}
}

func listCapacities(t *testing.T, c client.Client) []storagev1.CSIStorageCapacity {
	t.Helper()
	var list storagev1.CSIStorageCapacityList
	if err := c.List(context.Background(), &list, client.InNamespace(testNamespace)); err != nil {
		t.Fatalf("list capacities: %v", err)
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	return list.Items
}

func TestReconcile_CreatesOnePerNodeGroupAndStorageClass(t *testing.T) {
	sc := storageClass("local", map[string]string{CapacityAnnotation: "100Gi"})
	r, c := newReconciler(t,
		sc,
		node("n1", "groupA"),
		node("n2", "groupA"),
		node("n3", "groupB"),
	)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := listCapacities(t, c)
	if len(got) != 2 {
		t.Fatalf("expected 2 CSIStorageCapacity, got %d", len(got))
	}
	groups := map[string]string{}
	for _, o := range got {
		if o.Labels[ManagedByLabel] != ManagedByValue {
			t.Errorf("%s missing managed-by label", o.Name)
		}
		if o.StorageClassName != "local" {
			t.Errorf("%s storageClassName = %q, want local", o.Name, o.StorageClassName)
		}
		if o.Capacity == nil || o.Capacity.String() != "100Gi" {
			t.Errorf("%s capacity = %v, want 100Gi", o.Name, o.Capacity)
		}
		if o.NodeTopology == nil || o.NodeTopology.MatchLabels[DefaultNodeGroupLabelKey] == "" {
			t.Errorf("%s missing node-group topology selector", o.Name)
		}
		if o.NodeTopology != nil && o.NodeTopology.MatchLabels[TemplateNodeLabelKey] != TemplateNodeLabelValue {
			t.Errorf("%s missing template-node selector label", o.Name)
		}
		groups[o.NodeTopology.MatchLabels[DefaultNodeGroupLabelKey]] = o.Name
	}
	if _, ok := groups["groupA"]; !ok {
		t.Errorf("missing capacity for groupA: %v", groups)
	}
	if _, ok := groups["groupB"]; !ok {
		t.Errorf("missing capacity for groupB: %v", groups)
	}
}

func TestReconcile_SkipsStorageClassesWithoutAnnotation(t *testing.T) {
	r, c := newReconciler(t,
		storageClass("local", nil),
		storageClass("other", map[string]string{CapacityAnnotation: "1Gi"}),
		node("n1", "groupA"),
	)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := listCapacities(t, c)
	if len(got) != 1 {
		t.Fatalf("expected 1 CSIStorageCapacity, got %d (%v)", len(got), got)
	}
	if got[0].StorageClassName != "other" {
		t.Errorf("got %q, want other", got[0].StorageClassName)
	}
}

func TestReconcile_SkipsForeignProvisioners(t *testing.T) {
	foreign := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "foreign",
			Annotations: map[string]string{CapacityAnnotation: "1Gi"},
		},
		Provisioner: "some.other/provisioner",
	}
	r, c := newReconciler(t, foreign, node("n1", "groupA"))

	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if got := listCapacities(t, c); len(got) != 0 {
		t.Fatalf("expected 0 CSIStorageCapacity, got %d", len(got))
	}
}

func TestReconcile_DeletesStaleCapacities(t *testing.T) {
	// Pre-populate with a managed capacity for a (sc,group) that should no longer exist.
	stale := &storagev1.CSIStorageCapacity{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objectName("local", "gone"),
			Namespace: testNamespace,
			Labels: map[string]string{
				ManagedByLabel:    ManagedByValue,
				StorageClassLabel: "local",
				NodeGroupLabel:    "gone",
			},
		},
		StorageClassName: "local",
	}
	// Pre-populate with an UNMANAGED capacity that must be left alone.
	foreign := &storagev1.CSIStorageCapacity{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foreign-capacity",
			Namespace: testNamespace,
		},
		StorageClassName: "local",
	}
	r, c := newReconciler(t,
		storageClass("local", map[string]string{CapacityAnnotation: "100Gi"}),
		node("n1", "groupA"),
		stale,
		foreign,
	)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := listCapacities(t, c)
	names := map[string]bool{}
	for _, o := range got {
		names[o.Name] = true
	}
	if names[stale.Name] {
		t.Errorf("stale managed capacity %q should have been deleted", stale.Name)
	}
	if !names["foreign-capacity"] {
		t.Errorf("foreign-capacity should be preserved; got names: %v", names)
	}
}

func TestReconcile_UpdatesCapacityOnChange(t *testing.T) {
	sc := storageClass("local", map[string]string{CapacityAnnotation: "100Gi"})
	r, c := newReconciler(t, sc, node("n1", "groupA"))
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	// Mutate annotation and reconcile again.
	var fresh storagev1.StorageClass
	if err := c.Get(context.Background(), client.ObjectKey{Name: "local"}, &fresh); err != nil {
		t.Fatalf("get sc: %v", err)
	}
	fresh.Annotations[CapacityAnnotation] = "200Gi"
	if err := c.Update(context.Background(), &fresh); err != nil {
		t.Fatalf("update sc: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	got := listCapacities(t, c)
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d", len(got))
	}
	if got[0].Capacity.String() != "200Gi" {
		t.Errorf("capacity = %s, want 200Gi", got[0].Capacity.String())
	}
}

func TestObjectName_LongInputsHashed(t *testing.T) {
	long := strings.Repeat("a", 300)
	name := objectName(long, "group")
	if len(name) > dns1123MaxLen {
		t.Errorf("name length = %d, exceeds %d", len(name), dns1123MaxLen)
	}
	if !strings.HasPrefix(name, namePrefix) {
		t.Errorf("name %q missing prefix %q", name, namePrefix)
	}
}

func TestObjectName_SanitisesUppercaseAndSymbols(t *testing.T) {
	name := objectName("My_SC", "Group/01")
	for _, r := range name {
		if r >= 'A' && r <= 'Z' {
			t.Errorf("name %q contains uppercase", name)
			break
		}
		if r == '_' || r == '/' {
			t.Errorf("name %q contains invalid char %q", name, r)
			break
		}
	}
}

func TestCapacityEqual(t *testing.T) {
	q := resource.MustParse("1Gi")
	q2 := resource.MustParse("1024Mi")
	a := &storagev1.CSIStorageCapacity{Capacity: &q}
	b := &storagev1.CSIStorageCapacity{Capacity: &q2}
	if !capacityEqual(a, b) {
		t.Errorf("1Gi and 1024Mi should be equal capacities")
	}
}
