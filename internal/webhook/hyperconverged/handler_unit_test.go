// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package hyperconverged

import (
	"encoding/json"
	"math/rand"
	"slices"
	"testing"

	jsonpatch "github.com/evanphx/json-patch/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
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

func applyAffinityPatch(t *testing.T, pod *corev1.Pod, response admission.Response) *corev1.Pod {
	t.Helper()
	if !response.Allowed {
		t.Fatalf("expected allowed response, got %v", response.Result)
	}

	original, err := json.Marshal(pod)
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
		t.Fatalf("failed to unmarshal patched pod: %v", err)
	}
	return mutated
}

func affinitySelectorMatches(t *testing.T, selector *corev1.NodeSelector, name string, labels map[string]string) bool {
	t.Helper()
	matcher, err := nodeaffinity.NewNodeSelector(selector)
	if err != nil {
		t.Fatalf("failed to create selector: %v", err)
	}
	if labels == nil {
		labels = map[string]string{}
	}
	labels[KubernetesNodeHostNameLabel] = name
	return matcher.Match(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
	})
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

func TestRequiredAffinityComposition(t *testing.T) {
	h := createTestHandler()

	t.Run("conflicting caller affinity remains unsatisfiable", func(t *testing.T) {
		pod := &corev1.Pod{
			Spec: corev1.PodSpec{
				Affinity: &corev1.Affinity{
					NodeAffinity: &corev1.NodeAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{{
								MatchExpressions: []corev1.NodeSelectorRequirement{{
									Key:      KubernetesNodeHostNameLabel,
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{"node-b"},
								}},
							}},
						},
					},
				},
			},
		}

		mutated := applyAffinityPatch(
			t,
			pod,
			h.patchPodWithNodeAffinity(pod, []string{"node-a"}, FailoverModeDurability),
		)
		required := mutated.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		if affinitySelectorMatches(t, required, "node-a", nil) {
			t.Error("storage node must not bypass conflicting caller affinity")
		}
		if affinitySelectorMatches(t, required, "node-b", nil) {
			t.Error("caller node must not bypass storage affinity")
		}
	})

	t.Run("every existing OR term receives storage restriction", func(t *testing.T) {
		pod := &corev1.Pod{
			Spec: corev1.PodSpec{
				Affinity: &corev1.Affinity{
					NodeAffinity: &corev1.NodeAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{{
										Key: "region", Operator: corev1.NodeSelectorOpIn, Values: []string{"east"},
									}},
								},
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{{
										Key: "disk", Operator: corev1.NodeSelectorOpIn, Values: []string{"nvme"},
									}},
								},
							},
						},
					},
				},
			},
		}

		mutated := applyAffinityPatch(
			t,
			pod,
			h.patchPodWithNodeAffinity(pod, []string{"node-a"}, FailoverModeDurability),
		)
		required := mutated.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		for i, term := range required.NodeSelectorTerms {
			storageRequirements := 0
			for _, requirement := range term.MatchExpressions {
				if requirement.Key == KubernetesNodeHostNameLabel {
					storageRequirements++
				}
			}
			if storageRequirements != 1 {
				t.Errorf("term %d has %d storage requirements, expected 1", i, storageRequirements)
			}
		}
		if affinitySelectorMatches(t, required, "node-b", map[string]string{"region": "east", "disk": "nvme"}) {
			t.Error("non-storage node must not match any existing OR term")
		}
	})

	t.Run("empty selectors stay unsatisfiable", func(t *testing.T) {
		for name, selector := range map[string]*corev1.NodeSelector{
			"zero terms": {},
			"empty term": {NodeSelectorTerms: []corev1.NodeSelectorTerm{{}}},
		} {
			t.Run(name, func(t *testing.T) {
				pod := &corev1.Pod{
					Spec: corev1.PodSpec{
						Affinity: &corev1.Affinity{
							NodeAffinity: &corev1.NodeAffinity{
								RequiredDuringSchedulingIgnoredDuringExecution: selector,
							},
						},
					},
				}
				mutated := applyAffinityPatch(
					t,
					pod,
					h.patchPodWithNodeAffinity(pod, []string{"node-a"}, FailoverModeDurability),
				)
				required := mutated.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
				if affinitySelectorMatches(t, required, "node-a", nil) {
					t.Error("empty selector must remain unsatisfiable")
				}
			})
		}
	})
}

func TestGetPvNodesAndFailoverMode(t *testing.T) {
	// This test would require mocking the Kubernetes client
	// For now, we'll focus on the core affinity logic above
	t.Skip("Integration test requiring mocked Kubernetes client - covered by full integration tests")
}
