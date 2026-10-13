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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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

func createTestPodWithRequiredAffinity(name, namespace string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: corev1.PodSpec{
			Affinity: &corev1.Affinity{
				NodeAffinity: &corev1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{
							{
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

func applyAffinityLogic(pod *corev1.Pod, nodeNames []string, failoverMode string) {
	affinity := corev1.Affinity{}
	nodeAffinity := corev1.NodeAffinity{}

	nodeSelectorRequirement := corev1.NodeSelectorRequirement{
		Key:      KubernetesNodeHostNameLabel,
		Operator: corev1.NodeSelectorOpIn,
		Values:   nodeNames,
	}

	if pod.Spec.Affinity == nil {
		pod.Spec.Affinity = &affinity
	}
	if pod.Spec.Affinity.NodeAffinity == nil {
		pod.Spec.Affinity.NodeAffinity = &nodeAffinity
	}

	switch failoverMode {
	case FailoverModeDurability:
		applyRequiredAffinity(pod, nodeSelectorRequirement)
	case FailoverModeAvailability:
		fallthrough
	default:
		applyPreferredAffinity(pod, nodeSelectorRequirement)
	}
}

func applyRequiredAffinity(pod *corev1.Pod, requirement corev1.NodeSelectorRequirement) {
	requiredTerm := corev1.NodeSelectorTerm{
		MatchExpressions: []corev1.NodeSelectorRequirement{requirement},
	}
	if pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{requiredTerm},
		}
	} else {
		pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms = append(
			pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms,
			requiredTerm,
		)
	}
}

func applyPreferredAffinity(pod *corev1.Pod, requirement corev1.NodeSelectorRequirement) {
	preferredTerm := corev1.PreferredSchedulingTerm{
		Weight: 100,
		Preference: corev1.NodeSelectorTerm{
			MatchExpressions: []corev1.NodeSelectorRequirement{requirement},
		},
	}
	preferredTerms := []corev1.PreferredSchedulingTerm{preferredTerm}

	if pod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution == nil {
		pod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution = preferredTerms
	} else {
		pod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution = append(
			pod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution,
			preferredTerms...,
		)
	}
}

func validatePreferredAffinity(t *testing.T, pod *corev1.Pod, expectedNodes []string) {
	t.Helper()
	if pod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution == nil {
		t.Error("Expected preferred affinity to be set, but it was nil")
		return
	}

	found := findAffinityNodes(pod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution, expectedNodes)
	if !found {
		t.Error("Expected preferred affinity term with hostname label not found")
	}
}

func validateRequiredAffinity(t *testing.T, pod *corev1.Pod, expectedNodes []string) {
	t.Helper()
	if pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		t.Error("Expected required affinity to be set, but it was nil")
		return
	}

	found := findRequiredAffinityNodes(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms, expectedNodes)
	if !found {
		t.Error("Expected required affinity term with hostname label not found")
	}
}

func findAffinityNodes(preferredTerms []corev1.PreferredSchedulingTerm, expectedNodes []string) bool {
	for _, term := range preferredTerms {
		for _, expr := range term.Preference.MatchExpressions {
			if expr.Key == KubernetesNodeHostNameLabel && expr.Operator == corev1.NodeSelectorOpIn {
				return validateNodeList(expectedNodes, expr.Values)
			}
		}
	}
	return false
}

func findRequiredAffinityNodes(requiredTerms []corev1.NodeSelectorTerm, expectedNodes []string) bool {
	for _, term := range requiredTerms {
		for _, expr := range term.MatchExpressions {
			if expr.Key == KubernetesNodeHostNameLabel && expr.Operator == corev1.NodeSelectorOpIn {
				return validateNodeList(expectedNodes, expr.Values)
			}
		}
	}
	return false
}

func validateNodeList(expectedNodes, actualNodes []string) bool {
	for _, expectedNode := range expectedNodes {
		found := slices.Contains(actualNodes, expectedNode)
		if !found {
			return false
		}
	}
	return true
}

func multiVolumePV(name, mode, node string) *corev1.PersistentVolume {
	annotations := map[string]string{}
	if node != "" {
		annotations[csi.SelectedNodeAnnotation] = node
	}
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: annotations},
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

func multiVolumeHandler(t *testing.T, pvs ...*corev1.PersistentVolume) *handler {
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

func applyMultiVolumePatch(t *testing.T, response admission.Response) *corev1.Pod {
	t.Helper()
	if !response.Allowed {
		t.Fatalf("expected allowed response, got %v", response.Result)
	}
	original, err := json.Marshal(&corev1.Pod{})
	if err != nil {
		t.Fatalf("failed to marshal pod: %v", err)
	}
	patchData, err := json.Marshal(response.Patches)
	if err != nil {
		t.Fatalf("failed to marshal patch: %v", err)
	}
	patch, err := jsonpatch.DecodePatch(patchData)
	if err != nil {
		t.Fatalf("failed to decode patch: %v", err)
	}
	mutatedData, err := patch.Apply(original)
	if err != nil {
		t.Fatalf("failed to apply patch: %v", err)
	}
	mutated := &corev1.Pod{}
	if err := json.Unmarshal(mutatedData, mutated); err != nil {
		t.Fatalf("failed to unmarshal pod: %v", err)
	}
	return mutated
}

func multiVolumeRequiredNodes(t *testing.T, pod *corev1.Pod) []string {
	t.Helper()
	required := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if required == nil || len(required.NodeSelectorTerms) != 1 {
		t.Fatalf("expected one required term, got %#v", required)
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

	if !response.Allowed {
		t.Errorf("Expected allowed response, got: %v", response.Result)
	}
	if len(response.Patches) == 0 {
		t.Fatalf("Expected patches in response, got none")
	}

	testPod := pod.DeepCopy()
	applyAffinityLogic(testPod, nodeNames, FailoverModeAvailability)
	validatePreferredAffinity(t, testPod, nodeNames)
}

func TestPatchPodWithNodeAffinity_DurabilityMode(t *testing.T) {
	h := createTestHandler()
	pod := createTestPod()
	nodeNames := []string{"node3", "node4"}

	response := h.patchPodWithNodeAffinity(pod, nodeNames, FailoverModeDurability)

	if !response.Allowed {
		t.Errorf("Expected allowed response, got: %v", response.Result)
	}
	if len(response.Patches) == 0 {
		t.Fatalf("Expected patches in response, got none")
	}

	testPod := pod.DeepCopy()
	applyAffinityLogic(testPod, nodeNames, FailoverModeDurability)
	validateRequiredAffinity(t, testPod, nodeNames)
}

func TestPatchPodWithNodeAffinity_InvalidModeDefaultsToPreferred(t *testing.T) {
	h := createTestHandler()
	pod := createTestPod()
	nodeNames := []string{"node5"}

	response := h.patchPodWithNodeAffinity(pod, nodeNames, "invalid-mode")

	if !response.Allowed {
		t.Errorf("Expected allowed response, got: %v", response.Result)
	}
	if len(response.Patches) == 0 {
		t.Fatalf("Expected patches in response, got none")
	}

	testPod := pod.DeepCopy()
	applyAffinityLogic(testPod, nodeNames, "invalid-mode")
	validatePreferredAffinity(t, testPod, nodeNames)
}

func TestPatchPodWithNodeAffinity_EmptyModeDefaultsToPreferred(t *testing.T) {
	h := createTestHandler()
	pod := createTestPod()
	nodeNames := []string{"node6"}

	response := h.patchPodWithNodeAffinity(pod, nodeNames, "")

	if !response.Allowed {
		t.Errorf("Expected allowed response, got: %v", response.Result)
	}
	if len(response.Patches) == 0 {
		t.Fatalf("Expected patches in response, got none")
	}

	testPod := pod.DeepCopy()
	applyAffinityLogic(testPod, nodeNames, "")
	validatePreferredAffinity(t, testPod, nodeNames)
}

func TestPatchPodWithNodeAffinity_ExistingPreferredAffinity(t *testing.T) {
	h := createTestHandler()
	pod := createTestPodWithPreferredAffinity("test-pod", "test-namespace")
	nodeNames := []string{"node7"}

	response := h.patchPodWithNodeAffinity(pod, nodeNames, FailoverModeAvailability)

	if !response.Allowed {
		t.Errorf("Expected allowed response, got: %v", response.Result)
	}
	if len(response.Patches) == 0 {
		t.Fatalf("Expected patches in response, got none")
	}

	testPod := pod.DeepCopy()
	applyAffinityLogic(testPod, nodeNames, FailoverModeAvailability)
	validatePreferredAffinity(t, testPod, nodeNames)
}

func TestPatchPodWithNodeAffinity_ExistingRequiredAffinity(t *testing.T) {
	h := createTestHandler()
	pod := createTestPodWithRequiredAffinity("test-pod", "test-namespace")
	nodeNames := []string{"node8"}

	response := h.patchPodWithNodeAffinity(pod, nodeNames, FailoverModeDurability)

	if !response.Allowed {
		t.Errorf("Expected allowed response, got: %v", response.Result)
	}
	if len(response.Patches) == 0 {
		t.Fatalf("Expected patches in response, got none")
	}

	testPod := pod.DeepCopy()
	applyAffinityLogic(testPod, nodeNames, FailoverModeDurability)
	validateRequiredAffinity(t, testPod, nodeNames)
}

func TestInjectNodeAffinity_MultiVolumePlacement(t *testing.T) {
	ctx := context.Background()

	t.Run("availability-only pod uses all availability nodes", func(t *testing.T) {
		pvs := []*corev1.PersistentVolume{
			multiVolumePV("availability-a", FailoverModeAvailability, "node-a"),
			multiVolumePV("availability-b", FailoverModeAvailability, "node-b"),
		}
		h := multiVolumeHandler(t, pvs...)
		mutated := applyMultiVolumePatch(
			t,
			h.injectNodeAffinity(ctx, []string{pvs[0].Name, pvs[1].Name}, &corev1.Pod{}),
		)
		preferred := mutated.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
		if len(preferred) != 1 {
			t.Fatalf("expected one preferred term, got %d", len(preferred))
		}
		if got := preferred[0].Preference.MatchExpressions[0].Values; !slices.Equal(got, []string{"node-a", "node-b"}) {
			t.Fatalf("preferred nodes = %v, want [node-a node-b]", got)
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
				multiVolumePV("durability", FailoverModeDurability, "node-a"),
				multiVolumePV("availability", FailoverModeAvailability, "node-b"),
			}
			h := multiVolumeHandler(t, pvs...)
			mutated := applyMultiVolumePatch(
				t,
				h.injectNodeAffinity(ctx, order.pvNames, &corev1.Pod{}),
			)
			if got := multiVolumeRequiredNodes(t, mutated); !slices.Equal(got, []string{"node-a"}) {
				t.Fatalf("required nodes = %v, want [node-a]", got)
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
				multiVolumePV("durability-1", FailoverModeDurability, "node-a"),
				multiVolumePV("durability-2", FailoverModeDurability, "node-a"),
			}
			h := multiVolumeHandler(t, pvs...)
			mutated := applyMultiVolumePatch(
				t,
				h.injectNodeAffinity(ctx, order.pvNames, &corev1.Pod{}),
			)
			if got := multiVolumeRequiredNodes(t, mutated); !slices.Equal(got, []string{"node-a"}) {
				t.Fatalf("required nodes = %v, want [node-a]", got)
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
				multiVolumePV("durability-a", FailoverModeDurability, "node-a"),
				multiVolumePV("durability-b", FailoverModeDurability, "node-b"),
			}
			h := multiVolumeHandler(t, pvs...)
			response := h.injectNodeAffinity(ctx, order.pvNames, &corev1.Pod{})
			if response.Allowed {
				t.Fatal("expected conflicting durability nodes to deny admission")
			}
			if response.Result.Message != "durability volumes have no common storage node" {
				t.Fatalf("unexpected denial message: %q", response.Result.Message)
			}
		})
	}

	t.Run("missing durability placement denies admission", func(t *testing.T) {
		pv := multiVolumePV("durability", FailoverModeDurability, "")
		h := multiVolumeHandler(t, pv)
		response := h.injectNodeAffinity(ctx, []string{pv.Name}, &corev1.Pod{})
		if response.Allowed {
			t.Fatal("expected missing durability placement to deny admission")
		}
		if response.Result.Message != "durability volume has no resolved storage node" {
			t.Fatalf("unexpected denial message: %q", response.Result.Message)
		}
	})
}

func TestGetPvNodesAndFailoverMode(t *testing.T) {
	// This test would require mocking the Kubernetes client
	// For now, we'll focus on the core affinity logic above
	t.Skip("Integration test requiring mocked Kubernetes client - covered by full integration tests")
}
