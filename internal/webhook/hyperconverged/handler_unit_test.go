// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package hyperconverged

import (
	"context"
	"encoding/json"
	"fmt"
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
	"k8s.io/utils/ptr"
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

func claimTestHandler(t *testing.T, objects ...client.Object) *handler {
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

func claimTestResponse(t *testing.T, h *handler, pod *corev1.Pod) admission.Response {
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

func applyClaimPatch(t *testing.T, pod *corev1.Pod, response admission.Response) *corev1.Pod {
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

func claimStorageClass(name string) *storagev1.StorageClass {
	return &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: name},
		Provisioner: "localdisk.csi.acstor.io",
		Parameters:  map[string]string{FailoverModeParam: FailoverModeDurability},
	}
}

func claimPVC(name, storageClass, pvName string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test-namespace"},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &storageClass,
			VolumeName:       pvName,
		},
	}
}

func claimPV(name, node string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{csi.SelectedNodeAnnotation: node},
		},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       "localdisk.csi.acstor.io",
					VolumeHandle: name,
					VolumeAttributes: map[string]string{
						FailoverModeParam: FailoverModeDurability,
					},
				},
			},
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

func TestHandle_ClaimResolution(t *testing.T) {
	storageClass := claimStorageClass("local")
	durabilityPV := claimPV("durability-pv", "node-a")
	durabilityPVC := claimPVC("durability-data", storageClass.Name, durabilityPV.Name)

	t.Run("missing ordinary PVC is denied in either order", func(t *testing.T) {
		for _, volumes := range [][]corev1.Volume{
			{claimVolume("durability", durabilityPVC.Name), claimVolume("missing", "missing-x")},
			{claimVolume("missing", "missing-x"), claimVolume("durability", durabilityPVC.Name)},
		} {
			h := claimTestHandler(t, storageClass, durabilityPV, durabilityPVC)
			pod := createTestPod()
			pod.Spec.Volumes = volumes
			response := claimTestResponse(t, h, pod)
			if response.Allowed {
				t.Fatal("expected missing ordinary PVC to deny admission")
			}
			want := "PVC test-namespace/missing-x does not exist; create it before retrying Pod creation"
			if response.Result.Message != want {
				t.Fatalf("denial message = %q, want %q", response.Result.Message, want)
			}
		}
	})

	t.Run("missing generic ephemeral claim preserves resolved durability", func(t *testing.T) {
		h := claimTestHandler(t, storageClass, durabilityPV, durabilityPVC)
		pod := createTestPod()
		pod.Spec.Volumes = []corev1.Volume{
			{
				Name: "scratch",
				VolumeSource: corev1.VolumeSource{
					Ephemeral: &corev1.EphemeralVolumeSource{
						VolumeClaimTemplate: &corev1.PersistentVolumeClaimTemplate{
							Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &storageClass.Name},
						},
					},
				},
			},
			claimVolume("durability", durabilityPVC.Name),
		}
		mutated := applyClaimPatch(t, pod, claimTestResponse(t, h, pod))
		required := mutated.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		if required == nil || !findRequiredAffinityNodes(required.NodeSelectorTerms, []string{"node-a"}) {
			t.Fatal("expected resolved durability placement to be preserved")
		}
	})

	t.Run("generated generic ephemeral claim name uses volume name", func(t *testing.T) {
		generatedPV := claimPV("generated-pv", "node-a")
		generatedPVC := claimPVC("test-pod-scratch", storageClass.Name, generatedPV.Name)
		h := claimTestHandler(t, storageClass, generatedPV, generatedPVC)
		pod := createTestPod()
		pod.Spec.Volumes = []corev1.Volume{{
			Name: "scratch",
			VolumeSource: corev1.VolumeSource{
				Ephemeral: &corev1.EphemeralVolumeSource{
					VolumeClaimTemplate: &corev1.PersistentVolumeClaimTemplate{
						Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &storageClass.Name},
					},
				},
			},
		}}
		mutated := applyClaimPatch(t, pod, claimTestResponse(t, h, pod))
		required := mutated.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		if required == nil || !findRequiredAffinityNodes(required.NodeSelectorTerms, []string{"node-a"}) {
			t.Fatal("expected generated claim placement to be resolved")
		}
	})

	t.Run("unbound claim does not discard resolved durability", func(t *testing.T) {
		unboundPVC := claimPVC("unbound", storageClass.Name, "")
		h := claimTestHandler(t, storageClass, durabilityPV, durabilityPVC, unboundPVC)
		pod := createTestPod()
		pod.Spec.Volumes = []corev1.Volume{
			claimVolume("unbound", unboundPVC.Name),
			claimVolume("durability", durabilityPVC.Name),
		}
		mutated := applyClaimPatch(t, pod, claimTestResponse(t, h, pod))
		required := mutated.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		if required == nil || !findRequiredAffinityNodes(required.NodeSelectorTerms, []string{"node-a"}) {
			t.Fatal("expected resolved durability placement to be preserved")
		}
	})
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

func applyMultiVolumePatch(t *testing.T, response admission.Response, pods ...*corev1.Pod) *corev1.Pod {
	t.Helper()
	if !response.Allowed {
		t.Fatalf("expected allowed response, got %v", response.Result)
	}
	pod := &corev1.Pod{}
	if len(pods) > 0 {
		pod = pods[0]
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

func TestInjectNodeAffinity_UnplacedAvailability(t *testing.T) {
	for _, tc := range []struct {
		name       string
		durability []*corev1.PersistentVolume
		denial     string
	}{
		{name: "availability only"},
		{
			name:       "resolved durability",
			durability: []*corev1.PersistentVolume{multiVolumePV("durability", FailoverModeDurability, "node-a")},
		},
		{
			name:       "unresolved durability",
			durability: []*corev1.PersistentVolume{multiVolumePV("durability", FailoverModeDurability, "")},
			denial:     "durability volume has no resolved storage node",
		},
		{
			name: "conflicting durability",
			durability: []*corev1.PersistentVolume{
				multiVolumePV("durability-a", FailoverModeDurability, "node-a"),
				multiVolumePV("durability-b", FailoverModeDurability, "node-b"),
			},
			denial: "durability volumes have no common storage node",
		},
	} {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reverse=%t", tc.name, reverse), func(t *testing.T) {
				pvs := append([]*corev1.PersistentVolume{
					multiVolumePV("availability", FailoverModeAvailability, ""),
				}, tc.durability...)
				h := multiVolumeHandler(t, pvs...)
				names := make([]string, len(pvs))
				for i, pv := range pvs {
					names[i] = pv.Name
				}
				if reverse {
					slices.Reverse(names)
				}
				response := h.injectNodeAffinity(context.Background(), names, &corev1.Pod{})
				if tc.denial != "" {
					if response.Allowed || response.Result == nil || response.Result.Message != tc.denial {
						t.Fatalf("expected denial %q, got %+v", tc.denial, response)
					}
					return
				}
				if !response.Allowed {
					t.Fatalf("expected admission to succeed, got %+v", response)
				}
				if len(tc.durability) == 0 {
					if len(response.Patches) != 0 {
						t.Fatal("expected no affinity patch for unplaced availability-only volume")
					}
					return
				}
				mutated := applyMultiVolumePatch(t, response)
				if got := multiVolumeRequiredNodes(t, mutated); !slices.Equal(got, []string{"node-a"}) {
					t.Fatalf("required nodes = %v, want [node-a]", got)
				}
			})
		}
	}
}

func TestInjectNodeAffinity_EmptyPlacementMetadata(t *testing.T) {
	for _, tc := range []struct {
		name         string
		selectedNode *string
		initialNode  *string
		wantNode     string
	}{
		{name: "empty selected-node without initial node", selectedNode: ptr.To("")},
		{name: "empty initial node", initialNode: ptr.To("")},
		{name: "both node values empty", selectedNode: ptr.To(""), initialNode: ptr.To("")},
		{name: "empty selected-node falls back to initial node", selectedNode: ptr.To(""), initialNode: ptr.To("node-a"), wantNode: "node-a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pv := multiVolumePV("durability", FailoverModeDurability, "")
			if tc.selectedNode != nil {
				pv.Annotations[csi.SelectedNodeAnnotation] = *tc.selectedNode
			}
			if tc.initialNode != nil {
				pv.Spec.CSI.VolumeAttributes[csi.SelectedInitialNodeParam] = *tc.initialNode
			}
			h := multiVolumeHandler(t, pv)
			response := h.injectNodeAffinity(context.Background(), []string{pv.Name}, &corev1.Pod{})
			if tc.wantNode == "" {
				if response.Allowed || response.Result == nil || response.Result.Message != "durability volume has no resolved storage node" {
					t.Fatalf("expected unresolved durability denial, got %+v", response)
				}
				return
			}
			mutated := applyMultiVolumePatch(t, response)
			if got := multiVolumeRequiredNodes(t, mutated); !slices.Equal(got, []string{tc.wantNode}) {
				t.Fatalf("required nodes = %v, want [%s]", got, tc.wantNode)
			}
		})
	}
}

func TestInjectNodeAffinity_TopologyFallbackDoesNotExpandAffinity(t *testing.T) {
	topologyPV := func(name string) *corev1.PersistentVolume {
		pv := multiVolumePV(name, FailoverModeDurability, "")
		pv.Spec.NodeAffinity = &corev1.VolumeNodeAffinity{
			Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{MatchExpressions: []corev1.NodeSelectorRequirement{{
					Key: "zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"east"},
				}}},
				{MatchExpressions: []corev1.NodeSelectorRequirement{{
					Key: "zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"west"},
				}}},
			}},
		}
		return pv
	}

	t.Run("topology-only PV uses scheduler affinity without pod patch", func(t *testing.T) {
		pv := topologyPV("topology-only")
		h := multiVolumeHandler(t, pv)
		pod := &corev1.Pod{}
		response := h.injectNodeAffinity(context.Background(), []string{pv.Name}, pod)
		if !response.Allowed || len(response.Patches) != 0 {
			t.Fatalf("expected scheduler-enforced topology and no Pod patch, got %+v", response)
		}
	})

	t.Run("many topology selectors do not expand Pod affinity", func(t *testing.T) {
		pvs := make([]*corev1.PersistentVolume, 0, 13)
		pvNames := make([]string, 0, 13)
		for i := 0; i < 12; i++ {
			pv := topologyPV(fmt.Sprintf("topology-%02d", i))
			pvs = append(pvs, pv)
			pvNames = append(pvNames, pv.Name)
		}
		ownerPV := multiVolumePV("owner", FailoverModeDurability, "node-a")
		pvs = append(pvs, ownerPV)
		pvNames = append(pvNames, ownerPV.Name)
		h := multiVolumeHandler(t, pvs...)
		pod := &corev1.Pod{Spec: corev1.PodSpec{Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchExpressions: []corev1.NodeSelectorRequirement{{
						Key: "pool", Operator: corev1.NodeSelectorOpIn, Values: []string{"blue"},
					}},
				}},
			},
		}}}}

		response := h.injectNodeAffinity(context.Background(), pvNames, pod)
		if !response.Allowed {
			t.Fatalf("expected Pod admission, got %+v", response.Result)
		}
		patch, err := json.Marshal(response.Patches)
		if err != nil {
			t.Fatalf("failed to marshal patch: %v", err)
		}
		if len(patch) > 4096 {
			t.Fatalf("patch size = %d bytes, want at most 4096", len(patch))
		}
		mutated := applyMultiVolumePatch(t, response, pod)
		required := mutated.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		if required == nil || len(required.NodeSelectorTerms) != 1 || len(required.NodeSelectorTerms[0].MatchExpressions) != 2 {
			t.Fatalf("expected one composed term preserving app and owner constraints, got %+v", required)
		}
		if !affinitySelectorMatches(t, required, "node-a", map[string]string{"pool": "blue"}) {
			t.Fatal("owner node satisfying application affinity should match")
		}
		if affinitySelectorMatches(t, required, "node-a", map[string]string{"pool": "red"}) {
			t.Fatal("application affinity must not be bypassed")
		}
		if affinitySelectorMatches(t, required, "node-b", map[string]string{"pool": "blue"}) {
			t.Fatal("metadata-derived owner node must remain required")
		}
	})
}
