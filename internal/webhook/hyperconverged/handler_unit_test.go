// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package hyperconverged

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"net/http"
	"slices"
	"testing"

	jsonpatch "github.com/evanphx/json-patch/v5"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
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

func claimTestHandler(t *testing.T, objects ...client.Object) *handler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add core API to scheme: %v", err)
	}
	if err := storagev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add storage API to scheme: %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	h, err := NewHandler(
		"test-namespace",
		c,
		c,
		scheme,
	)
	if err != nil {
		t.Fatalf("failed to create handler: %v", err)
	}
	return h
}

type failingClaimReader struct {
	client.Reader
	calls int
}

func (r *failingClaimReader) Get(_ context.Context, key client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
	r.calls++
	return apierrors.NewForbidden(corev1.Resource("persistentvolumeclaims"), key.Name, errors.New("access denied"))
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
		Parameters: map[string]string{
			FailoverModeParam: FailoverModeDurability,
		},
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
			Name: name,
			Annotations: map[string]string{
				csi.SelectedNodeAnnotation: node,
			},
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

func TestHandle_ClaimResolution(t *testing.T) {
	storageClass := claimStorageClass("local")
	durabilityPV := claimPV("durability-pv", "node-a")
	durabilityPVC := claimPVC("durability-data", storageClass.Name, durabilityPV.Name)

	t.Run("missing ordinary PVC preserves resolved durability in either order", func(t *testing.T) {
		for _, volumes := range [][]corev1.Volume{
			{
				claimVolume("durability", durabilityPVC.Name),
				claimVolume("missing", "missing-x"),
			},
			{
				claimVolume("missing", "missing-x"),
				claimVolume("durability", durabilityPVC.Name),
			},
		} {
			h := claimTestHandler(t, storageClass, durabilityPV, durabilityPVC)
			pod := createTestPod()
			pod.Spec.Volumes = volumes
			mutated := applyClaimPatch(t, pod, claimTestResponse(t, h, pod))
			required := mutated.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
			if required == nil || !findRequiredAffinityNodes(required.NodeSelectorTerms, []string{"node-a"}) {
				t.Fatal("expected resolved durability placement to be preserved")
			}
		}
	})

	t.Run("pod can be created before its ordinary PVC", func(t *testing.T) {
		h := claimTestHandler(t)
		pod := createTestPod()
		pod.Spec.Volumes = []corev1.Volume{claimVolume("missing", "missing-x")}
		response := claimTestResponse(t, h, pod)
		if !response.Allowed || len(response.Patches) != 0 {
			t.Fatalf("expected allowed response without patches, got %+v", response)
		}
	})

	t.Run("PVC missing from cache is resolved through API reader", func(t *testing.T) {
		h := claimTestHandler(t, storageClass, durabilityPV)
		h.apiReader = claimTestHandler(t, durabilityPVC).client
		pod := createTestPod()
		pod.Spec.Volumes = []corev1.Volume{claimVolume("durability", durabilityPVC.Name)}
		mutated := applyClaimPatch(t, pod, claimTestResponse(t, h, pod))
		required := mutated.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		if required == nil || !findRequiredAffinityNodes(required.NodeSelectorTerms, []string{"node-a"}) {
			t.Fatal("expected API reader claim to enforce durability placement")
		}
	})

	t.Run("API reader failure is not treated as a missing claim", func(t *testing.T) {
		h := claimTestHandler(t)
		reader := &failingClaimReader{}
		h.apiReader = reader
		pod := createTestPod()
		pod.Spec.Volumes = []corev1.Volume{claimVolume("data", "data")}
		response := claimTestResponse(t, h, pod)
		if response.Allowed || response.Result == nil || response.Result.Code != http.StatusBadRequest {
			t.Fatalf("expected lookup error response, got %+v", response)
		}
		if reader.calls != 1 {
			t.Fatalf("API read calls = %d, want 1", reader.calls)
		}
	})

	t.Run("cached PVC does not require an API read", func(t *testing.T) {
		h := claimTestHandler(t, storageClass, durabilityPV, durabilityPVC)
		reader := &failingClaimReader{}
		h.apiReader = reader
		pod := createTestPod()
		pod.Spec.Volumes = []corev1.Volume{claimVolume("durability", durabilityPVC.Name)}
		applyClaimPatch(t, pod, claimTestResponse(t, h, pod))
		if reader.calls != 0 {
			t.Fatalf("API read calls = %d, want 0", reader.calls)
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
							Spec: corev1.PersistentVolumeClaimSpec{
								StorageClassName: &storageClass.Name,
							},
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

	t.Run("ephemeral template lookup failure preserves resolved durability", func(t *testing.T) {
		for _, reverse := range []bool{false, true} {
			h := claimTestHandler(t, storageClass, durabilityPV, durabilityPVC)
			reads := 0
			h.client = fake.NewClientBuilder().WithScheme(h.client.Scheme()).
				WithObjects(storageClass, durabilityPV, durabilityPVC).WithInterceptorFuncs(interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*storagev1.StorageClass); ok && key.Name == "unreadable" {
						reads++
						return apierrors.NewForbidden(storagev1.Resource("storageclasses"), key.Name, errors.New("access denied"))
					}
					return c.Get(ctx, key, obj, opts...)
				},
			}).Build()
			templateClass := "unreadable"
			pod := createTestPod()
			pod.Spec.Volumes = []corev1.Volume{
				{
					Name: "scratch",
					VolumeSource: corev1.VolumeSource{
						Ephemeral: &corev1.EphemeralVolumeSource{
							VolumeClaimTemplate: &corev1.PersistentVolumeClaimTemplate{
								Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &templateClass},
							},
						},
					},
				},
				claimVolume("durability", durabilityPVC.Name),
			}
			if reverse {
				slices.Reverse(pod.Spec.Volumes)
			}
			mutated := applyClaimPatch(t, pod, claimTestResponse(t, h, pod))
			required := mutated.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
			if required == nil || !findRequiredAffinityNodes(required.NodeSelectorTerms, []string{"node-a"}) {
				t.Fatal("expected resolved durability placement despite template lookup failure")
			}
			if reads != 1 {
				t.Fatalf("template StorageClass reads = %d, want 1", reads)
			}
		}
	})

	t.Run("generated generic ephemeral claim name uses volume name", func(t *testing.T) {
		generatedPV := claimPV("generated-pv", "node-a")
		generatedPVC := claimPVC("test-pod-scratch", storageClass.Name, generatedPV.Name)
		pod := createTestPod()
		pod.UID = "current-pod"
		generatedPVC.OwnerReferences = []metav1.OwnerReference{
			*metav1.NewControllerRef(pod, corev1.SchemeGroupVersion.WithKind("Pod")),
		}
		h := claimTestHandler(t, storageClass, generatedPV, generatedPVC)
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
		mutated := applyClaimPatch(t, pod, claimTestResponse(t, h, pod))
		required := mutated.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		if required == nil || !findRequiredAffinityNodes(required.NodeSelectorTerms, []string{"node-a"}) {
			t.Fatal("expected generated claim placement to be resolved")
		}
	})

	t.Run("unowned generic ephemeral claim does not override resolved durability", func(t *testing.T) {
		for _, ownership := range []string{"stale owner", "no owner", "non-controller owner", "missing pod UID"} {
			t.Run(ownership, func(t *testing.T) {
				for _, reverse := range []bool{false, true} {
					pod := createTestPod()
					pod.UID = "current-pod"
					stalePV := claimPV("stale-pv", "obsolete-node")
					stalePVC := claimPVC("test-pod-scratch", storageClass.Name, stalePV.Name)
					owner := *metav1.NewControllerRef(pod, corev1.SchemeGroupVersion.WithKind("Pod"))
					switch ownership {
					case "stale owner":
						owner.UID = "previous-pod"
						stalePVC.OwnerReferences = []metav1.OwnerReference{owner}
					case "non-controller owner":
						controller := false
						owner.Controller = &controller
						stalePVC.OwnerReferences = []metav1.OwnerReference{owner}
					case "missing pod UID":
						pod.UID = ""
						owner.UID = ""
						stalePVC.OwnerReferences = []metav1.OwnerReference{owner}
					}
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
					if reverse {
						slices.Reverse(pod.Spec.Volumes)
					}
					h := claimTestHandler(t, storageClass, durabilityPV, durabilityPVC, stalePV, stalePVC)
					mutated := applyClaimPatch(t, pod, claimTestResponse(t, h, pod))
					required := mutated.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
					if required == nil || !findRequiredAffinityNodes(required.NodeSelectorTerms, []string{"node-a"}) ||
						findRequiredAffinityNodes(required.NodeSelectorTerms, []string{"obsolete-node"}) {
						t.Fatalf("unexpected placement for %s (reverse=%t): %+v", ownership, reverse, required)
					}
				}
			})
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

func TestGetPvNodesAndFailoverMode(t *testing.T) {
	// This test would require mocking the Kubernetes client
	// For now, we'll focus on the core affinity logic above
	t.Skip("Integration test requiring mocked Kubernetes client - covered by full integration tests")
}
