// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package hyperconverged

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"slices"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	"k8s.io/component-helpers/storage/ephemeral"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"local-csi-driver/internal/csi"
	"local-csi-driver/internal/csi/core/lvm"
)

var log = ctrl.Log.WithName("hyperconverged")

type handler struct {
	namespace string
	rng       *rand.Rand
	client    client.Client
	apiReader client.Reader
	decoder   admission.Decoder
}

const (
	// Well known label used by Kubernetes to identify the node name.
	KubernetesNodeHostNameLabel = "kubernetes.io/hostname"

	// Failover mode parameter values.
	FailoverModeAvailability = "availability"
	FailoverModeDurability   = "durability"
	FailoverModeParam        = "localdisk.csi.acstor.io/failover-mode"
)

// handler implements admission.Handler.
var _ admission.Handler = &handler{}

func NewHandler(namespace string, client client.Client, apiReader client.Reader, scheme *runtime.Scheme) (*handler, error) {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	return &handler{
		namespace: namespace,
		rng:       rng,
		client:    client,
		apiReader: apiReader,
		decoder:   admission.NewDecoder(scheme),
	}, nil
}

// Handler updates pods with node affinity to available diskpool if pod has hyperconverged volumes.
func (h *handler) Handle(ctx context.Context, req admission.Request) admission.Response {
	var (
		pod     = &corev1.Pod{}
		pvNames = make([]string, 0)
	)
	log.V(1).Info("pod handling request", "kind", req.Kind.Kind, "request", req.Name, "operation", req.Operation)

	// We shouldn't get requests for anything other than creating pods, but if
	// we do, allow them immediately.
	if req.Kind.Kind != "Pod" || req.Operation != admissionv1.Create {
		return admission.Allowed("unhandled request allowed")
	}

	// Decode the request into a pod object.
	err := h.decoder.Decode(req, pod)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	// Check if the pod has volumes attached to it.
	if len(pod.Spec.Volumes) == 0 {
		log.Info("allowed pod with no volumes", "pod", pod.Name, "namespace", pod.Namespace)
		return admission.Allowed("pod has no volumes")
	}

	// Get the storage classes for the volumes attached to the pod.
	for _, volume := range pod.Spec.Volumes {
		log.V(4).Info("getting pv for volume", "volume", volume.Name)
		var (
			pvcName          string
			storageClassName *string
		)
		isEphemeral := volume.Ephemeral != nil
		if volume.PersistentVolumeClaim != nil {
			pvcName = volume.PersistentVolumeClaim.ClaimName
		} else if isEphemeral {
			// Kubernetes names generated ephemeral claims <pod-name>-<volume-name>.
			pvcName = ephemeral.VolumeClaimName(pod, &volume)
			storageClassName = volume.Ephemeral.VolumeClaimTemplate.Spec.StorageClassName
		} else {
			continue
		}

		pvc := &corev1.PersistentVolumeClaim{}
		getPVC := func() error {
			key := client.ObjectKey{Name: pvcName, Namespace: req.Namespace}
			if err := h.client.Get(ctx, key, pvc); !apierrors.IsNotFound(err) {
				return err
			}
			// Confirm cache misses against the API before treating a claim as unresolved.
			return h.apiReader.Get(ctx, key, pvc)
		}

		if err := retry.OnError(retry.DefaultRetry, isRetriableError, getPVC); err != nil {
			if apierrors.IsNotFound(err) {
				if !isEphemeral {
					klog.Warningf("pvc %s/%s not found for pod %s; continuing with other volumes", pod.Namespace, pvcName, pod.Name)
					continue
				}
				h.validateEphemeralTemplatePolicy(ctx, storageClassName, pvcName, pod)
				// Kubernetes creates the generic-ephemeral claim after Pod admission.
				continue
			}
			log.Error(err, "failed to get persistent volume claim", "pvc", pvcName, "namespace", req.Namespace)
			return admission.Errored(http.StatusBadRequest, err)
		}

		if isEphemeral && (pod.UID == "" || ephemeral.VolumeIsForPod(pod, pvc) != nil) {
			log.V(2).Info("generic ephemeral pvc is not owned by this pod; continuing with other volumes",
				"pvc", pvcName, "pod", pod.Name, "namespace", pod.Namespace)
			h.validateEphemeralTemplatePolicy(ctx, storageClassName, pvcName, pod)
			continue
		}

		storageClassName = pvc.Spec.StorageClassName

		if storageClassName == nil {
			continue
		}

		storageClass := &storagev1.StorageClass{}
		getStorageClass := func() error {
			return h.client.Get(ctx, client.ObjectKey{Name: *storageClassName}, storageClass)
		}
		if err := retry.OnError(retry.DefaultRetry, isRetriableError, getStorageClass); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			log.Error(err, "failed to get storage class", "storageClassName", *storageClassName)
			return admission.Errored(http.StatusBadRequest, err)
		}

		// Ignore non-acstor storage classes.
		if storageClass.Provisioner != lvm.DriverName {
			continue
		}

		if pvc.Spec.VolumeName == "" {
			// WFFC claims are resolved later by Kubernetes and must not block other claims.
			log.Info("local pvc is not bound yet", "pvc", pvcName, "pod", pod.Name, "namespace", pod.Namespace)
			continue
		}
		pvNames = append(pvNames, pvc.Spec.VolumeName)
	}

	if len(pvNames) != 0 {
		return h.injectNodeAffinity(ctx, pvNames, pod)
	}

	log.Info("allowed pod with no hyperconverged volumes", "pod", pod.Name, "namespace", pod.Namespace)
	return admission.Allowed("pod has no hyperconverged volumes")
}

func (h *handler) validateEphemeralTemplatePolicy(
	ctx context.Context,
	storageClassName *string,
	pvcName string,
	pod *corev1.Pod,
) {
	// Use the template StorageClass because the generated PVC does not exist yet.
	if storageClassName == nil || *storageClassName == "" {
		return
	}

	storageClass := &storagev1.StorageClass{}
	getStorageClass := func() error {
		return h.client.Get(ctx, client.ObjectKey{Name: *storageClassName}, storageClass)
	}
	if err := retry.OnError(retry.DefaultRetry, isRetriableError, getStorageClass); err != nil {
		if apierrors.IsNotFound(err) {
			return
		}
		log.Error(err, "failed to get storage class", "storageClassName", *storageClassName)
		return
	}
	if storageClass.Provisioner != lvm.DriverName {
		return
	}

	mode := storageClass.Parameters[FailoverModeParam]
	if mode != FailoverModeDurability {
		mode = FailoverModeAvailability
	}
	log.Info(
		"generic ephemeral pvc is not created yet",
		"pvc", pvcName,
		"pod", pod.Name,
		"namespace", pod.Namespace,
		"failoverMode", mode,
	)
}

func (h *handler) injectNodeAffinity(ctx context.Context, pvNames []string, pod *corev1.Pod) admission.Response {
	log.Info("starting to inject storage affinity for pod", "pod", pod.Name)

	var pvNodes []string
	var durabilityNodes []string
	hasDurability := false
	hasMetadataPlacement := false
	hasTopologyPlacement := false

	for _, pvName := range pvNames {
		log.Info("getting nodes for volume", "volume name", pvName)
		// if volume has existing replicas, get the nodes of the online replicas
		pvNodeList, mode, hasTopology, response := h.getPvNodesAndFailoverMode(ctx, pvName)
		if response != nil {
			return *response
		}
		if mode != FailoverModeDurability {
			pvNodes = append(pvNodes, pvNodeList...)
			continue
		}
		if len(pvNodeList) == 0 {
			if hasTopology {
				hasDurability = true
				hasTopologyPlacement = true
				continue
			}
			return admission.Denied("durability volume has no resolved storage node")
		}
		if !hasDurability {
			// Start the intersection with the first durability volume.
			durabilityNodes = slices.Clone(pvNodeList)
			hasDurability = true
			hasMetadataPlacement = true
			continue
		}
		if !hasMetadataPlacement {
			durabilityNodes = slices.Clone(pvNodeList)
			hasMetadataPlacement = true
			continue
		}
		var commonNodes []string
		// Every durability volume must already exist on the selected node.
		for _, node := range durabilityNodes {
			if slices.Contains(pvNodeList, node) {
				commonNodes = append(commonNodes, node)
			}
		}
		durabilityNodes = commonNodes
	}

	if hasDurability {
		if hasMetadataPlacement && len(durabilityNodes) == 0 {
			return admission.Denied("durability volumes have no common storage node")
		}
		if !hasMetadataPlacement && hasTopologyPlacement {
			return admission.Allowed("durability PV node affinity will be enforced by the scheduler")
		}
		slices.Sort(durabilityNodes)
		durabilityNodes = slices.Compact(durabilityNodes)
		return h.patchPodWithNodeAffinity(pod, durabilityNodes, FailoverModeDurability)
	}

	if len(pvNodes) == 0 {
		log.Info("no nodes found for volume", "volume name", pvNames[0])
		return admission.Allowed("no nodes found for volume")
	}

	// Availability-only volumes use preferred node affinity.
	return h.patchPodWithNodeAffinity(pod, pvNodes, FailoverModeAvailability)
}

func (h *handler) patchPodWithNodeAffinity(pod *corev1.Pod, nodeNames []string, failoverMode string) admission.Response {
	log.Info("list of nodes available for pod", "pod", pod.Name, "nodes", nodeNames, "failoverMode", failoverMode)

	// add the node affinity to the pod based on failover mode
	newPod := pod.DeepCopy()
	affinity := corev1.Affinity{}
	nodeAffinity := corev1.NodeAffinity{}

	// Create node selector requirement
	nodeSelectorRequirement := corev1.NodeSelectorRequirement{
		Key:      KubernetesNodeHostNameLabel,
		Operator: corev1.NodeSelectorOpIn,
		Values:   nodeNames,
	}

	if newPod.Spec.Affinity == nil {
		newPod.Spec.Affinity = &affinity
	}
	if newPod.Spec.Affinity.NodeAffinity == nil {
		newPod.Spec.Affinity.NodeAffinity = &nodeAffinity
	}

	// Apply affinity based on failover mode
	switch failoverMode {
	case FailoverModeDurability:
		// Required affinity - pod must be scheduled on one of these nodes
		log.Info("applying required node affinity for durability mode", "pod", pod.Name)
		requiredTerm := corev1.NodeSelectorTerm{
			MatchExpressions: []corev1.NodeSelectorRequirement{nodeSelectorRequirement},
		}
		if newPod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
			newPod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{requiredTerm},
			}
		} else {
			required := newPod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
			for i := range required.NodeSelectorTerms {
				term := &required.NodeSelectorTerms[i]
				// An empty term matches no nodes. Appending the storage
				// requirement would make it satisfiable and widen the ORed
				// selector, letting the Pod bypass its other required terms,
				// so leave it unchanged.
				if len(term.MatchExpressions) == 0 && len(term.MatchFields) == 0 {
					continue
				}
				// Expressions inside one term are ANDed by the scheduler.
				term.MatchExpressions = append(term.MatchExpressions, nodeSelectorRequirement)
			}
		}
	case FailoverModeAvailability:
		fallthrough
	default:
		// Preferred affinity - pod prefers to be scheduled on these nodes but can be scheduled elsewhere
		log.Info("applying preferred node affinity for availability mode", "pod", pod.Name)
		preferredTerm := corev1.PreferredSchedulingTerm{
			Weight: 100,
			Preference: corev1.NodeSelectorTerm{
				MatchExpressions: []corev1.NodeSelectorRequirement{nodeSelectorRequirement},
			},
		}
		preferredTerms := []corev1.PreferredSchedulingTerm{preferredTerm}
		shuffleObjects(*h.rng, preferredTerms)

		if newPod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution == nil {
			newPod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution = preferredTerms
		} else {
			// Add to existing preferred terms
			newPod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution = append(
				newPod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution,
				preferredTerms...,
			)
		}
	}

	marshalledPod, err := json.Marshal(pod)
	if err != nil {
		log.Error(err, "failed to marshal pod", "pod", pod.Name)
		return admission.Errored(http.StatusInternalServerError, err)
	}
	marshaledNewPod, err := json.Marshal(newPod)
	if err != nil {
		log.Error(err, "failed to marshal new pod with node affinity", "pod", pod.Name, "newPod", newPod.Name)
		return admission.Errored(http.StatusInternalServerError, err)
	}
	log.Info("patching pod with node affinity", "pod", pod.Name, "failoverMode", failoverMode)
	return admission.PatchResponseFromRaw(marshalledPod, marshaledNewPod)
}

// shuffleObjects shuffles an array of objects of any type.
func shuffleObjects[T any](rng rand.Rand, objectList []T) []T {
	rng.Shuffle(len(objectList), func(i, j int) {
		objectList[i], objectList[j] = objectList[j], objectList[i]
	})
	return objectList
}

// isRetriableError returns false if the error is not retriable.
func isRetriableError(err error) bool {
	switch apierrors.ReasonForError(err) {
	case
		metav1.StatusReasonNotFound,
		metav1.StatusReasonUnauthorized,
		metav1.StatusReasonForbidden,
		metav1.StatusReasonAlreadyExists,
		metav1.StatusReasonGone,
		metav1.StatusReasonInvalid,
		metav1.StatusReasonBadRequest,
		metav1.StatusReasonMethodNotAllowed,
		metav1.StatusReasonNotAcceptable,
		metav1.StatusReasonRequestEntityTooLarge,
		metav1.StatusReasonUnsupportedMediaType,
		metav1.StatusReasonExpired:
		return false
	default:
		return true
	}
}

func (h *handler) getPvNodesAndFailoverMode(ctx context.Context, pvName string) ([]string, string, bool, *admission.Response) {
	var response admission.Response
	var nodeNamesList []string
	var failoverMode = FailoverModeAvailability // default to availability mode

	// Get the PV.
	var pv = &corev1.PersistentVolume{}
	getPV := func() error {
		return h.client.Get(ctx, client.ObjectKey{Name: pvName}, pv)
	}
	if err := retry.OnError(retry.DefaultRetry, isRetriableError, getPV); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, failoverMode, false, nil
		}
		log.Error(err, "failed to get persistent volume", "pvName", pvName)
		response = admission.Errored(http.StatusBadRequest, err)
		return nil, failoverMode, false, &response
	}

	if pv != nil && pv.Spec.CSI != nil && pv.Spec.CSI.VolumeHandle != "" {
		// Get failover mode from volume context
		if mode, ok := pv.Spec.CSI.VolumeAttributes[FailoverModeParam]; ok {
			if mode == FailoverModeAvailability || mode == FailoverModeDurability {
				failoverMode = mode
				log.Info("found failover mode in PV volume attributes", "pvName", pvName, "failoverMode", failoverMode)
			} else {
				log.Info("invalid failover mode in PV, using default", "pvName", pvName, "invalidMode", mode, "defaultMode", failoverMode)
			}
		}

		// Get PV-s selected node annotation
		nodeName := pv.Annotations[csi.SelectedNodeAnnotation]
		if nodeName == "" {
			// Get the node name from volume context
			nodeName = pv.Spec.CSI.VolumeAttributes[csi.SelectedInitialNodeParam]
			if nodeName == "" {
				hasTopology := false
				if failoverMode == FailoverModeDurability &&
					pv.Spec.NodeAffinity != nil &&
					pv.Spec.NodeAffinity.Required != nil {
					for _, term := range pv.Spec.NodeAffinity.Required.NodeSelectorTerms {
						if len(term.MatchExpressions) != 0 || len(term.MatchFields) != 0 {
							hasTopology = true
							break
						}
					}
				}
				if !hasTopology {
					log.Error(fmt.Errorf("pv is not assigned to a node"), "no node name found", "pod", pvName)
				}
				return nil, failoverMode, hasTopology, nil
			}
		}
		nodeNamesList = append(nodeNamesList, nodeName)
	}

	return nodeNamesList, failoverMode, false, nil
}
