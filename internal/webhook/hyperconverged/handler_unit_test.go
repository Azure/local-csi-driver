// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package hyperconverged

import (
	"context"
	"encoding/json"
	"math/rand"
	"slices"
	"testing"

	jsonpatch "github.com/evanphx/json-patch/v5"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"local-csi-driver/internal/csi"
)

// Helper functions for test setup and validation

func createTestPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Spec: corev1.PodSpec{},
	}
}

func createTestPodWithPreferredAffinity(name, namespace string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: corev1.PodSpec{
			Affinity: &corev1.Affinity{
				NodeAffinity: &corev1.NodeAffinity{
					PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{
						{
							Weight: 50,
							Preference: corev1.NodeSelectorTerm{
								MatchExpressions: []corev1.NodeSelectorRequirement{
									{
										Key:      "existing-label",
										Operator: corev1.NodeSelectorOpIn,
										Values:   []string{"existing-value"},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func createTestHandler() *handler {
	rng := rand.New(rand.NewSource(1)) //nolint:gosec // test code
	return &handler{
		rng: rng,
	}
}

func applyAdmissionPatch(t *testing.T, pod *corev1.Pod, response admission.Response) *corev1.Pod {
	t.Helper()
	if !response.Allowed {
		t.Fatalf("expected allowed response, got: %v", response.Result)
	}
	if len(response.Patches) == 0 {
		return pod.DeepCopy()
	}

	original, err := json.Marshal(pod)
	if err != nil {
		t.Fatalf("failed to marshal original pod: %v", err)
	}
	patchData, err := json.Marshal(response.Patches)
	if err != nil {
		t.Fatalf("failed to marshal admission patches: %v", err)
	}
	patch, err := jsonpatch.DecodePatch(patchData)
	if err != nil {
		t.Fatalf("failed to decode admission patches: %v", err)
	}
	mutatedData, err := patch.Apply(original)
	if err != nil {
		t.Fatalf("failed to apply admission patches: %v", err)
	}

	mutatedPod := &corev1.Pod{}
	if err := json.Unmarshal(mutatedData, mutatedPod); err != nil {
		t.Fatalf("failed to unmarshal patched pod: %v", err)
	}
	return mutatedPod
}

func requiredSelectorMatches(t *testing.T, selector *corev1.NodeSelector, node *corev1.Node) bool {
	t.Helper()
	matcher, err := nodeaffinity.NewNodeSelector(selector)
	if err != nil {
		t.Fatalf("failed to create node selector: %v", err)
	}
	return matcher.Match(node)
}

func testNode(name string, labels map[string]string) *corev1.Node {
	nodeLabels := map[string]string{
		KubernetesNodeHostNameLabel: name,
	}
	for key, value := range labels {
		nodeLabels[key] = value
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: nodeLabels,
		},
	}
}

func requiredAffinityPod(selector *corev1.NodeSelector) *corev1.Pod {
	pod := createTestPod()
	pod.Spec.Affinity = &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: selector,
		},
	}
	return pod
}

func countStorageRequirements(term corev1.NodeSelectorTerm) int {
	count := 0
	for _, requirement := range term.MatchExpressions {
		if requirement.Key == KubernetesNodeHostNameLabel &&
			requirement.Operator == corev1.NodeSelectorOpIn {
			count++
		}
	}
	return count
}

func createAggregationHandler(t *testing.T, pvs ...*corev1.PersistentVolume) *handler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add core API to scheme: %v", err)
	}
	objects := make([]client.Object, 0, len(pvs))
	for _, pv := range pvs {
		objects = append(objects, pv)
	}
	h := createTestHandler()
	h.client = fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	return h
}

func createHandleTestHandler(t *testing.T, objects ...client.Object) *handler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add core API to scheme: %v", err)
	}
	if err := storagev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add storage API to scheme: %v", err)
	}
	h, err := NewHandler(
		"test-namespace",
		fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(),
		scheme,
	)
	if err != nil {
		t.Fatalf("failed to create handler: %v", err)
	}
	return h
}

func handleTestPod(t *testing.T, h *handler, pod *corev1.Pod) admission.Response {
	t.Helper()
	raw, err := json.Marshal(pod)
	if err != nil {
		t.Fatalf("failed to marshal pod: %v", err)
	}
	return h.Handle(context.Background(), admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Namespace: pod.Namespace,
			Kind:      metav1.GroupVersionKind{Kind: "Pod"},
			Operation: admissionv1.Create,
			Object:    runtime.RawExtension{Raw: raw},
		},
	})
}

func handleTestStorageClass(name, mode string) *storagev1.StorageClass {
	return &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: name},
		Provisioner: "localdisk.csi.acstor.io",
		Parameters: map[string]string{
			FailoverModeParam: mode,
		},
	}
}

func handleTestPVC(name, storageClass, pvName string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test-namespace"},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &storageClass,
			VolumeName:       pvName,
		},
	}
}

func claimVolume(name, claimName string) corev1.Volume {
	return corev1.Volume{
		Name: name,
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: claimName,
			},
		},
	}
}

func aggregationPV(name, mode, node string) *corev1.PersistentVolume {
	annotations := map[string]string{}
	if node != "" {
		annotations[csi.SelectedNodeAnnotation] = node
	}
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: annotations,
		},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       "localdisk.csi.acstor.io",
					VolumeHandle: name,
					VolumeAttributes: map[string]string{
						FailoverModeParam: mode,
					},
				},
			},
		},
	}
}

func requiredStorageNodes(t *testing.T, pod *corev1.Pod) []string {
	t.Helper()
	required := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if required == nil || len(required.NodeSelectorTerms) != 1 {
		t.Fatalf("expected one required node selector term, got %#v", required)
	}
	for _, requirement := range required.NodeSelectorTerms[0].MatchExpressions {
		if requirement.Key == KubernetesNodeHostNameLabel {
			return requirement.Values
		}
	}
	t.Fatal("required storage-node requirement not found")
	return nil
}

// Test functions split by scenario

func TestPatchPodWithNodeAffinity_AvailabilityMode(t *testing.T) {
	h := createTestHandler()
	pod := createTestPod()
	nodeNames := []string{"node1", "node2"}

	response := h.patchPodWithNodeAffinity(pod, nodeNames, FailoverModeAvailability)
	mutatedPod := applyAdmissionPatch(t, pod, response)

	preferred := mutatedPod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	if len(preferred) != 1 || preferred[0].Weight != 100 {
		t.Fatalf("expected one preferred term with weight 100, got: %#v", preferred)
	}
	if got := countStorageRequirements(preferred[0].Preference); got != 1 {
		t.Fatalf("expected one storage requirement, got %d", got)
	}
}

func TestPatchPodWithNodeAffinity_DurabilityMode(t *testing.T) {
	h := createTestHandler()
	pod := createTestPod()
	nodeNames := []string{"node3", "node4"}

	response := h.patchPodWithNodeAffinity(pod, nodeNames, FailoverModeDurability)
	mutatedPod := applyAdmissionPatch(t, pod, response)
	required := mutatedPod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution

	if len(required.NodeSelectorTerms) != 1 {
		t.Fatalf("expected one required term, got %d", len(required.NodeSelectorTerms))
	}
	if !requiredSelectorMatches(t, required, testNode("node3", nil)) {
		t.Error("expected storage node node3 to match")
	}
	if requiredSelectorMatches(t, required, testNode("other-node", nil)) {
		t.Error("expected a non-storage node not to match")
	}
}

func TestPatchPodWithNodeAffinity_InvalidModeDefaultsToPreferred(t *testing.T) {
	h := createTestHandler()
	pod := createTestPod()
	nodeNames := []string{"node5"}

	response := h.patchPodWithNodeAffinity(pod, nodeNames, "invalid-mode")
	mutatedPod := applyAdmissionPatch(t, pod, response)

	if len(mutatedPod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution) != 1 {
		t.Fatal("expected invalid mode to add preferred affinity")
	}
}

func TestPatchPodWithNodeAffinity_EmptyModeDefaultsToPreferred(t *testing.T) {
	h := createTestHandler()
	pod := createTestPod()
	nodeNames := []string{"node6"}

	response := h.patchPodWithNodeAffinity(pod, nodeNames, "")
	mutatedPod := applyAdmissionPatch(t, pod, response)

	if len(mutatedPod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution) != 1 {
		t.Fatal("expected empty mode to add preferred affinity")
	}
}

func TestPatchPodWithNodeAffinity_ExistingPreferredAffinity(t *testing.T) {
	h := createTestHandler()
	pod := createTestPodWithPreferredAffinity("test-pod", "test-namespace")
	nodeNames := []string{"node7"}

	response := h.patchPodWithNodeAffinity(pod, nodeNames, FailoverModeAvailability)
	mutatedPod := applyAdmissionPatch(t, pod, response)
	preferred := mutatedPod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution

	if len(preferred) != 2 {
		t.Fatalf("expected existing and storage preferred terms, got %d", len(preferred))
	}
	if preferred[0].Weight != 50 || preferred[0].Preference.MatchExpressions[0].Key != "existing-label" {
		t.Errorf("existing preferred term changed: %#v", preferred[0])
	}
	if got := countStorageRequirements(preferred[1].Preference); got != 1 {
		t.Fatalf("expected one storage requirement in appended term, got %d", got)
	}
}

func TestPatchPodWithNodeAffinity_ExistingRequiredAffinity(t *testing.T) {
	h := createTestHandler()
	storageNode := "node-a"

	t.Run("conflicting hostname matches neither node", func(t *testing.T) {
		pod := requiredAffinityPod(&corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{{
					Key:      KubernetesNodeHostNameLabel,
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{"node-b"},
				}},
			}},
		})

		mutatedPod := applyAdmissionPatch(
			t,
			pod,
			h.patchPodWithNodeAffinity(pod, []string{storageNode}, FailoverModeDurability),
		)
		required := mutatedPod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution

		if requiredSelectorMatches(t, required, testNode(storageNode, nil)) {
			t.Error("storage node must not bypass the caller's conflicting hostname requirement")
		}
		if requiredSelectorMatches(t, required, testNode("node-b", nil)) {
			t.Error("caller node must not bypass the storage hostname requirement")
		}
	})

	t.Run("compatible constraint allows only storage node", func(t *testing.T) {
		pod := requiredAffinityPod(&corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{{
					Key:      "topology.kubernetes.io/zone",
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{"zone-1"},
				}},
			}},
		})

		mutatedPod := applyAdmissionPatch(
			t,
			pod,
			h.patchPodWithNodeAffinity(pod, []string{storageNode}, FailoverModeDurability),
		)
		required := mutatedPod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution

		if !requiredSelectorMatches(t, required, testNode(storageNode, map[string]string{
			"topology.kubernetes.io/zone": "zone-1",
		})) {
			t.Error("expected compatible storage node to match")
		}
		if requiredSelectorMatches(t, required, testNode("node-b", map[string]string{
			"topology.kubernetes.io/zone": "zone-1",
		})) {
			t.Error("expected compatible non-storage node not to match")
		}
	})

	t.Run("every existing OR term receives storage restriction", func(t *testing.T) {
		pod := requiredAffinityPod(&corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{
					MatchExpressions: []corev1.NodeSelectorRequirement{{
						Key:      "region",
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{"east"},
					}},
				},
				{
					MatchExpressions: []corev1.NodeSelectorRequirement{{
						Key:      "disk",
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{"nvme"},
					}},
				},
			},
		})

		mutatedPod := applyAdmissionPatch(
			t,
			pod,
			h.patchPodWithNodeAffinity(pod, []string{storageNode}, FailoverModeDurability),
		)
		required := mutatedPod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution

		if len(required.NodeSelectorTerms) != 2 {
			t.Fatalf("expected two existing terms, got %d", len(required.NodeSelectorTerms))
		}
		for i, term := range required.NodeSelectorTerms {
			if got := countStorageRequirements(term); got != 1 {
				t.Errorf("term %d has %d storage requirements, expected 1", i, got)
			}
		}
		if requiredSelectorMatches(t, required, testNode("node-b", map[string]string{
			"region": "east",
			"disk":   "nvme",
		})) {
			t.Error("non-storage node must not match any existing OR term")
		}
	})
}

func TestPatchPodWithNodeAffinity_RequiredSelectorEdgeCases(t *testing.T) {
	h := createTestHandler()
	storageNode := "node-a"

	tests := []struct {
		name            string
		selector        *corev1.NodeSelector
		wantTerms       int
		wantStorageReqs []int
		wantMatch       bool
	}{
		{
			name:      "nil selector creates storage term",
			selector:  nil,
			wantTerms: 1,
			wantStorageReqs: []int{
				1,
			},
			wantMatch: true,
		},
		{
			name:            "zero terms remain unsatisfiable",
			selector:        &corev1.NodeSelector{},
			wantTerms:       0,
			wantStorageReqs: nil,
			wantMatch:       false,
		},
		{
			name: "empty term remains unsatisfiable",
			selector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{}},
			},
			wantTerms:       1,
			wantStorageReqs: []int{0},
			wantMatch:       false,
		},
		{
			name: "match fields term receives storage restriction",
			selector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchFields: []corev1.NodeSelectorRequirement{{
						Key:      "metadata.name",
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{storageNode},
					}},
				}},
			},
			wantTerms:       1,
			wantStorageReqs: []int{1},
			wantMatch:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := requiredAffinityPod(tt.selector)
			mutatedPod := applyAdmissionPatch(
				t,
				pod,
				h.patchPodWithNodeAffinity(pod, []string{storageNode}, FailoverModeDurability),
			)
			required := mutatedPod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution

			if len(required.NodeSelectorTerms) != tt.wantTerms {
				t.Fatalf("got %d terms, want %d", len(required.NodeSelectorTerms), tt.wantTerms)
			}
			for i, want := range tt.wantStorageReqs {
				if got := countStorageRequirements(required.NodeSelectorTerms[i]); got != want {
					t.Errorf("term %d has %d storage requirements, want %d", i, got, want)
				}
			}
			if got := requiredSelectorMatches(t, required, testNode(storageNode, nil)); got != tt.wantMatch {
				t.Errorf("selector match = %t, want %t", got, tt.wantMatch)
			}
		})
	}
}

func TestInjectNodeAffinity_Aggregation(t *testing.T) {
	ctx := context.Background()

	t.Run("availability only uses every availability node", func(t *testing.T) {
		pvs := []*corev1.PersistentVolume{
			aggregationPV("availability-a", FailoverModeAvailability, "node-a"),
			aggregationPV("availability-b", FailoverModeAvailability, "node-b"),
		}

		h := createAggregationHandler(t, pvs...)
		pod := createTestPod()
		response := h.injectNodeAffinity(ctx, []string{pvs[0].Name, pvs[1].Name}, pod)
		mutatedPod := applyAdmissionPatch(t, pod, response)
		preferred := mutatedPod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution

		if len(preferred) != 1 {
			t.Fatalf("expected one preferred term, got %d", len(preferred))
		}
		nodes := preferred[0].Preference.MatchExpressions[0].Values
		if !slices.Equal(nodes, []string{"node-a", "node-b"}) {
			t.Fatalf("preferred nodes = %v, want [node-a node-b]", nodes)
		}
	})

	t.Run("durability only uses required affinity", func(t *testing.T) {
		pv := aggregationPV("durability", FailoverModeDurability, "node-a")
		h := createAggregationHandler(t, pv)
		pod := createTestPod()
		response := h.injectNodeAffinity(ctx, []string{pv.Name}, pod)
		mutatedPod := applyAdmissionPatch(t, pod, response)

		if got := requiredStorageNodes(t, mutatedPod); !slices.Equal(got, []string{"node-a"}) {
			t.Fatalf("required nodes = %v, want [node-a]", got)
		}
	})

	t.Run("durability falls back to retained PV topology", func(t *testing.T) {
		pv := aggregationPV("durability", FailoverModeDurability, "")
		pv.Spec.NodeAffinity = &corev1.VolumeNodeAffinity{
			Required: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchExpressions: []corev1.NodeSelectorRequirement{{
						Key:      KubernetesNodeHostNameLabel,
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{"node-a"},
					}},
				}},
			},
		}
		h := createAggregationHandler(t, pv)
		pod := createTestPod()
		mutatedPod := applyAdmissionPatch(
			t,
			pod,
			h.injectNodeAffinity(ctx, []string{pv.Name}, pod),
		)

		if got := requiredStorageNodes(t, mutatedPod); !slices.Equal(got, []string{"node-a"}) {
			t.Fatalf("required nodes = %v, want [node-a]", got)
		}
	})

	for _, order := range []struct {
		name    string
		pvNames []string
	}{
		{name: "durability then availability", pvNames: []string{"durability", "availability"}},
		{name: "availability then durability", pvNames: []string{"availability", "durability"}},
	} {
		t.Run(order.name, func(t *testing.T) {
			pvs := []*corev1.PersistentVolume{
				aggregationPV("durability", FailoverModeDurability, "node-a"),
				aggregationPV("availability", FailoverModeAvailability, "node-b"),
			}
			h := createAggregationHandler(t, pvs...)
			pod := createTestPod()
			response := h.injectNodeAffinity(ctx, order.pvNames, pod)
			mutatedPod := applyAdmissionPatch(t, pod, response)

			if got := requiredStorageNodes(t, mutatedPod); !slices.Equal(got, []string{"node-a"}) {
				t.Fatalf("required nodes = %v, want [node-a]", got)
			}
			if requiredSelectorMatches(
				t,
				mutatedPod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution,
				testNode("node-b", nil),
			) {
				t.Error("availability node must not satisfy mixed-mode required affinity")
			}
		})
	}

	for _, order := range []struct {
		name    string
		pvNames []string
	}{
		{name: "first then second", pvNames: []string{"durability-1", "durability-2"}},
		{name: "second then first", pvNames: []string{"durability-2", "durability-1"}},
	} {
		t.Run("matching durability nodes/"+order.name, func(t *testing.T) {
			pvs := []*corev1.PersistentVolume{
				aggregationPV("durability-1", FailoverModeDurability, "node-a"),
				aggregationPV("durability-2", FailoverModeDurability, "node-a"),
			}
			h := createAggregationHandler(t, pvs...)
			pod := createTestPod()
			response := h.injectNodeAffinity(ctx, order.pvNames, pod)
			mutatedPod := applyAdmissionPatch(t, pod, response)
			required := mutatedPod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution

			if got := requiredStorageNodes(t, mutatedPod); !slices.Equal(got, []string{"node-a"}) {
				t.Fatalf("required nodes = %v, want [node-a]", got)
			}
			if !requiredSelectorMatches(t, required, testNode("node-a", nil)) {
				t.Error("common durability node must satisfy required affinity")
			}
			if requiredSelectorMatches(t, required, testNode("node-b", nil)) {
				t.Error("non-common node must not satisfy required affinity")
			}
		})
	}

	for _, order := range []struct {
		name    string
		pvNames []string
	}{
		{name: "a then b", pvNames: []string{"durability-a", "durability-b"}},
		{name: "b then a", pvNames: []string{"durability-b", "durability-a"}},
	} {
		t.Run("conflicting durability nodes/"+order.name, func(t *testing.T) {
			pvs := []*corev1.PersistentVolume{
				aggregationPV("durability-a", FailoverModeDurability, "node-a"),
				aggregationPV("durability-b", FailoverModeDurability, "node-b"),
			}
			h := createAggregationHandler(t, pvs...)
			response := h.injectNodeAffinity(ctx, order.pvNames, createTestPod())

			if response.Allowed {
				t.Fatal("expected conflicting durability nodes to deny admission")
			}
			if response.Result.Message != "durability volumes have no common storage node" {
				t.Fatalf("unexpected denial message: %q", response.Result.Message)
			}
		})
	}

	t.Run("missing durability placement denies admission", func(t *testing.T) {
		pv := aggregationPV("durability", FailoverModeDurability, "")
		h := createAggregationHandler(t, pv)
		response := h.injectNodeAffinity(ctx, []string{pv.Name}, createTestPod())

		if response.Allowed {
			t.Fatal("expected missing durability placement to deny admission")
		}
		if response.Result.Message != "durability volume has no resolved storage node" {
			t.Fatalf("unexpected denial message: %q", response.Result.Message)
		}
	})

	t.Run("conflicting caller affinity cannot permit another node", func(t *testing.T) {
		pv := aggregationPV("durability", FailoverModeDurability, "node-a")
		h := createAggregationHandler(t, pv)
		pod := requiredAffinityPod(&corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{{
					Key:      KubernetesNodeHostNameLabel,
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{"node-b"},
				}},
			}},
		})
		response := h.injectNodeAffinity(ctx, []string{pv.Name}, pod)
		mutatedPod := applyAdmissionPatch(t, pod, response)
		required := mutatedPod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution

		if requiredSelectorMatches(t, required, testNode("node-a", nil)) {
			t.Error("storage node must not bypass conflicting caller affinity")
		}
		if requiredSelectorMatches(t, required, testNode("node-b", nil)) {
			t.Error("caller node must not bypass durability placement")
		}
	})
}

func TestHandle_MissingClaims(t *testing.T) {
	storageClass := handleTestStorageClass("local", FailoverModeDurability)
	durabilityPV := aggregationPV("durability-pv", FailoverModeDurability, "node-a")
	durabilityPVC := handleTestPVC("durability-data", storageClass.Name, durabilityPV.Name)

	for _, volumes := range []struct {
		name    string
		volumes []corev1.Volume
	}{
		{
			name: "durability then missing",
			volumes: []corev1.Volume{
				claimVolume("durability", durabilityPVC.Name),
				claimVolume("missing", "missing-x"),
			},
		},
		{
			name: "missing then durability",
			volumes: []corev1.Volume{
				claimVolume("missing", "missing-x"),
				claimVolume("durability", durabilityPVC.Name),
			},
		},
	} {
		t.Run(volumes.name, func(t *testing.T) {
			h := createHandleTestHandler(t, storageClass, durabilityPV, durabilityPVC)
			pod := createTestPod()
			pod.Spec.Volumes = volumes.volumes
			response := handleTestPod(t, h, pod)

			if response.Allowed {
				t.Fatal("expected missing ordinary PVC to deny admission")
			}
			want := "PVC test-namespace/missing-x does not exist; create it before retrying Pod creation"
			if response.Result.Message != want {
				t.Fatalf("denial message = %q, want %q", response.Result.Message, want)
			}
		})
	}
}

func TestHandle_MissingGenericEphemeralClaimPreservesResolvedDurability(t *testing.T) {
	storageClass := handleTestStorageClass("local", FailoverModeDurability)
	durabilityPV := aggregationPV("durability-pv", FailoverModeDurability, "node-a")
	durabilityPVC := handleTestPVC("durability-data", storageClass.Name, durabilityPV.Name)
	ephemeralVolume := corev1.Volume{
		Name: "scratch",
		VolumeSource: corev1.VolumeSource{
			Ephemeral: &corev1.EphemeralVolumeSource{
				VolumeClaimTemplate: &corev1.PersistentVolumeClaimTemplate{
					Spec: corev1.PersistentVolumeClaimSpec{
						StorageClassName: &storageClass.Name,
					},
				},
			},
		},
	}

	for _, volumes := range [][]corev1.Volume{
		{claimVolume("durability", durabilityPVC.Name), ephemeralVolume},
		{ephemeralVolume, claimVolume("durability", durabilityPVC.Name)},
	} {
		h := createHandleTestHandler(t, storageClass, durabilityPV, durabilityPVC)
		pod := createTestPod()
		pod.Spec.Volumes = volumes
		response := handleTestPod(t, h, pod)
		mutatedPod := applyAdmissionPatch(t, pod, response)

		if got := requiredStorageNodes(t, mutatedPod); !slices.Equal(got, []string{"node-a"}) {
			t.Fatalf("required nodes = %v, want [node-a]", got)
		}
	}
}

func TestHandle_UsesGeneratedGenericEphemeralClaimName(t *testing.T) {
	storageClass := handleTestStorageClass("local", FailoverModeDurability)
	pv := aggregationPV("ephemeral-pv", FailoverModeDurability, "node-a")
	pvc := handleTestPVC("test-pod-scratch", storageClass.Name, pv.Name)
	h := createHandleTestHandler(t, storageClass, pv, pvc)
	pod := createTestPod()
	pod.Spec.Volumes = []corev1.Volume{{
		Name: "scratch",
		VolumeSource: corev1.VolumeSource{
			Ephemeral: &corev1.EphemeralVolumeSource{
				VolumeClaimTemplate: &corev1.PersistentVolumeClaimTemplate{
					Spec: corev1.PersistentVolumeClaimSpec{
						StorageClassName: &storageClass.Name,
					},
				},
			},
		},
	}}

	mutatedPod := applyAdmissionPatch(t, pod, handleTestPod(t, h, pod))
	if got := requiredStorageNodes(t, mutatedPod); !slices.Equal(got, []string{"node-a"}) {
		t.Fatalf("required nodes = %v, want [node-a]", got)
	}
}

func TestHandle_UnboundClaimDoesNotDiscardResolvedDurability(t *testing.T) {
	storageClass := handleTestStorageClass("local", FailoverModeDurability)
	durabilityPV := aggregationPV("durability-pv", FailoverModeDurability, "node-a")
	durabilityPVC := handleTestPVC("durability-data", storageClass.Name, durabilityPV.Name)
	unboundPVC := handleTestPVC("unbound-data", storageClass.Name, "")
	h := createHandleTestHandler(t, storageClass, durabilityPV, durabilityPVC, unboundPVC)
	pod := createTestPod()
	pod.Spec.Volumes = []corev1.Volume{
		claimVolume("unbound", unboundPVC.Name),
		claimVolume("durability", durabilityPVC.Name),
	}

	mutatedPod := applyAdmissionPatch(t, pod, handleTestPod(t, h, pod))
	if got := requiredStorageNodes(t, mutatedPod); !slices.Equal(got, []string{"node-a"}) {
		t.Fatalf("required nodes = %v, want [node-a]", got)
	}
}
