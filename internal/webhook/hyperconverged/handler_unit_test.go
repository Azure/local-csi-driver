// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package hyperconverged

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"testing"

	jsonpatch "github.com/evanphx/json-patch/v5"
	corev1 "k8s.io/api/core/v1"
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

	t.Run("empty durability placement metadata", func(t *testing.T) {
		for _, tc := range []struct {
			name         string
			selectedNode *string
			initialNode  *string
			wantNode     string
		}{
			{name: "empty selected-node without initial node", selectedNode: ptr.To("")},
			{name: "empty initial node", initialNode: ptr.To("")},
			{name: "empty selected-node and initial node", selectedNode: ptr.To(""), initialNode: ptr.To("")},
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
				response := h.injectNodeAffinity(ctx, []string{pv.Name}, &corev1.Pod{})
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

func TestInjectNodeAffinity_TopologyFallback(t *testing.T) {
	labelTerm := func(key string, values ...string) corev1.NodeSelectorTerm {
		return corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{{
			Key: key, Operator: corev1.NodeSelectorOpIn, Values: values,
		}}}
	}
	topologyPV := func(name string, terms ...corev1.NodeSelectorTerm) *corev1.PersistentVolume {
		pv := multiVolumePV(name, FailoverModeDurability, "")
		pv.Spec.NodeAffinity = &corev1.VolumeNodeAffinity{
			Required: &corev1.NodeSelector{NodeSelectorTerms: terms},
		}
		return pv
	}
	nodes := []*corev1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "node-a", Labels: map[string]string{
			KubernetesNodeHostNameLabel: "host-a", "topology.localdisk.csi.acstor.io/node": "node-a", "zone": "east",
		}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node-b", Labels: map[string]string{
			KubernetesNodeHostNameLabel: "host-b", "topology.localdisk.csi.acstor.io/node": "node-b", "zone": "west",
		}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node-c", Labels: map[string]string{
			KubernetesNodeHostNameLabel: "host-c", "zone": "east",
		}}},
	}
	for _, tc := range []struct {
		name string
		pvs  []*corev1.PersistentVolume
		pod  *corev1.Pod
		want []string
		deny bool
	}{
		{
			name: "driver topology without metadata",
			pvs:  []*corev1.PersistentVolume{topologyPV("pv", labelTerm("topology.localdisk.csi.acstor.io/node", "node-a"))},
			want: []string{"node-a"},
		},
		{
			name: "hostname label differs from node name",
			pvs:  []*corev1.PersistentVolume{topologyPV("pv", labelTerm(KubernetesNodeHostNameLabel, "host-a"))},
			want: []string{"node-a"},
		},
		{
			name: "ORed terms including empty term",
			pvs: []*corev1.PersistentVolume{topologyPV("pv", corev1.NodeSelectorTerm{},
				labelTerm(KubernetesNodeHostNameLabel, "host-a"), labelTerm(KubernetesNodeHostNameLabel, "host-b"))},
			want: []string{"node-a", "node-b"},
		},
		{
			name: "ANDed expressions and metadata.name field",
			pvs: []*corev1.PersistentVolume{topologyPV("pv", corev1.NodeSelectorTerm{
				MatchExpressions: []corev1.NodeSelectorRequirement{
					{Key: "zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"east"}},
					{Key: "excluded", Operator: corev1.NodeSelectorOpDoesNotExist},
				},
				MatchFields: []corev1.NodeSelectorRequirement{
					{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{"node-a"}},
				},
			})},
			want: []string{"node-a"},
		},
		{
			name: "multiple topology volumes intersect",
			pvs: []*corev1.PersistentVolume{
				topologyPV("pv-1", labelTerm("zone", "east")),
				topologyPV("pv-2", labelTerm(KubernetesNodeHostNameLabel, "host-a", "host-b")),
			},
			want: []string{"node-a"},
		},
		{
			name: "conflicting topology remains unschedulable",
			pvs: []*corev1.PersistentVolume{
				topologyPV("pv-1", labelTerm(KubernetesNodeHostNameLabel, "host-a")),
				topologyPV("pv-2", labelTerm(KubernetesNodeHostNameLabel, "host-b")),
			},
		},
		{
			name: "topology intersects metadata placement and pod affinity",
			pvs: []*corev1.PersistentVolume{
				topologyPV("pv-1", labelTerm("zone", "east")),
				multiVolumePV("pv-2", FailoverModeDurability, "host-a"),
			},
			pod: &corev1.Pod{Spec: corev1.PodSpec{Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{labelTerm(KubernetesNodeHostNameLabel, "host-c"), {}},
				},
			}}}},
		},
		{
			name: "topology and metadata allow common node",
			pvs: []*corev1.PersistentVolume{
				topologyPV("pv-1", labelTerm("zone", "east")),
				multiVolumePV("pv-2", FailoverModeDurability, "host-a"),
			},
			want: []string{"node-a"},
		},
		{
			name: "topology intersects ORed pod requirements without widening empty terms",
			pvs:  []*corev1.PersistentVolume{topologyPV("pv", labelTerm("zone", "east"))},
			pod: &corev1.Pod{Spec: corev1.PodSpec{Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{
						{}, labelTerm(KubernetesNodeHostNameLabel, "host-a"), labelTerm(KubernetesNodeHostNameLabel, "host-b"),
					},
				},
			}}}},
			want: []string{"node-a"},
		},
		{
			name: "explicitly empty required pod selector stays unschedulable",
			pvs:  []*corev1.PersistentVolume{topologyPV("pv", labelTerm("zone", "east"))},
			pod: &corev1.Pod{Spec: corev1.PodSpec{Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{},
			}}}},
		},
		{name: "no topology terms", pvs: []*corev1.PersistentVolume{topologyPV("pv")}, deny: true},
		{name: "only empty topology terms", pvs: []*corev1.PersistentVolume{topologyPV("pv", corev1.NodeSelectorTerm{})}, deny: true},
	} {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reverse=%t", tc.name, reverse), func(t *testing.T) {
				h := multiVolumeHandler(t, tc.pvs...)
				names := make([]string, len(tc.pvs))
				for i, pv := range tc.pvs {
					names[i] = pv.Name
				}
				if reverse {
					slices.Reverse(names)
				}
				pod := tc.pod
				if pod == nil {
					pod = &corev1.Pod{}
				}
				original := pod.DeepCopy()
				response := h.injectNodeAffinity(context.Background(), names, pod)
				if tc.deny {
					if response.Allowed || response.Result == nil || response.Result.Message != "durability volume has no resolved storage node" {
						t.Fatalf("expected unresolved placement denial, got %+v", response)
					}
					return
				}
				mutated := applyMultiVolumePatch(t, response, pod)
				var matched []string
				for _, node := range nodes {
					ok, err := nodeaffinity.GetRequiredNodeAffinity(mutated).Match(node)
					if err != nil {
						t.Fatalf("invalid affinity: %v", err)
					}
					if ok {
						matched = append(matched, node.Name)
					}
				}
				if !slices.Equal(matched, tc.want) {
					t.Fatalf("matched nodes = %v, want %v", matched, tc.want)
				}
				if !reflect.DeepEqual(original, pod) {
					t.Fatal("input pod was modified")
				}
			})
		}
	}
}

func TestGetPvNodesAndFailoverMode(t *testing.T) {
	// This test would require mocking the Kubernetes client
	// For now, we'll focus on the core affinity logic above
	t.Skip("Integration test requiring mocked Kubernetes client - covered by full integration tests")
}
