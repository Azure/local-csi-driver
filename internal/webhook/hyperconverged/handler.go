// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package hyperconverged

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
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
	decoder   admission.Decoder
}

const (
	// Well known label used by Kubernetes to identify the node name.
	KubernetesNodeHostNameLabel = "kubernetes.io/hostname"
)

// handler implements admission.Handler.
var _ admission.Handler = &handler{}

func NewHandler(namespace string, client client.Client, scheme *runtime.Scheme) (*handler, error) {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	return &handler{
		namespace: namespace,
		rng:       rng,
		client:    client,
		decoder:   admission.NewDecoder(scheme),
	}, nil
}

// Handler updates pods with node affinity to available diskpool if pod has hyperconverged volumes.
func (h *handler) Handle(ctx context.Context, req admission.Request) admission.Response {
	var (
		pod              = &corev1.Pod{}
		pvc              = &corev1.PersistentVolumeClaim{}
		pvcName          string
		storageClassName *string
		storageClass     = &storagev1.StorageClass{}
		pvNames          = make([]string, 0)
	)
	log.V(1).Info("pod handling request", "kind", req.Kind.Kind, "request", req.Name, "operation", req.Operation)

	getPVC := func() error {
		return h.client.Get(ctx, client.ObjectKey{Name: pvcName, Namespace: req.Namespace}, pvc)
	}

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
		// Get the storage class name
		pvName := ""
		if volume.PersistentVolumeClaim != nil {
			// Get the pvc
			pvcName = volume.PersistentVolumeClaim.ClaimName

		} else if volume.Ephemeral != nil {
			pvcName = pod.Name + "-" + volume.Ephemeral.VolumeClaimTemplate.Spec.VolumeName
			storageClassName = volume.Ephemeral.VolumeClaimTemplate.Spec.StorageClassName
		}

		if err := retry.OnError(retry.DefaultRetry, isRetriableError, getPVC); err != nil {
			if apierrors.IsNotFound(err) {
				log.Info("allowed pod with pvc not found", "pvc", pvcName, "pod", pod.Name, "namespace", pod.Namespace)
				return admission.Allowed("pvc not found")
			}
			return admission.Errored(http.StatusBadRequest, err)
		}

		if volume.PersistentVolumeClaim != nil {
			// Get the storage class name from the pvc
			storageClassName = pvc.Spec.StorageClassName
		}

		pvName = pvc.Spec.VolumeName

		if storageClassName == nil {
			continue
		}

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

		pvNames = append(pvNames, pvName)
	}

	if len(pvNames) != 0 {
		return h.injectNodeAffinity(ctx, pvNames, pod)
	}

	log.Info("allowed pod with no hyperconverged volumes", "pod", pod.Name, "namespace", pod.Namespace)
	return admission.Allowed("pod has no hyperconverged volumes")
}

func (h *handler) injectNodeAffinity(ctx context.Context, pvNames []string, pod *corev1.Pod) admission.Response {
	log.Info("starting inject diskpool affinity for pod", "pod", pod.Name)

	var pvNodes []string
	var response *admission.Response

	for _, pvName := range pvNames {
		log.Info("getting nodes for volume", "volume name", pvName)
		// if volume has existing replicas, get the nodes of the online replicas
		pvNodes, response = h.getPvNodes(ctx, pvName)
		if response != nil {
			return *response
		}
	}

	if len(pvNodes) == 0 {
		log.Info("no nodes found for volume", "volume name", pvNames[0])
		return admission.Allowed("no nodes found for volume")
	}

	// add the preferred node affinity to the pod
	return h.patchPodWithPreferredNodeAffinity(pod, pvNodes)
}

func (h *handler) patchPodWithPreferredNodeAffinity(pod *corev1.Pod, nodeNames []string) admission.Response {
	log.Info("list of nodes available for pod", "pod", pod.Name, "nodes", nodeNames)

	// add the node affinity to worker label name to the pod
	newPod := pod.DeepCopy()
	affinity := corev1.Affinity{}
	nodeAffinity := corev1.NodeAffinity{}
	preferredSchedulingTerm := []corev1.PreferredSchedulingTerm{}
	schedulingTerm := corev1.PreferredSchedulingTerm{
		Weight: 100,
		Preference: corev1.NodeSelectorTerm{
			MatchExpressions: []corev1.NodeSelectorRequirement{
				{
					Key:      KubernetesNodeHostNameLabel,
					Operator: corev1.NodeSelectorOpIn,
					Values:   nodeNames,
				},
			},
		},
	}
	preferredSchedulingTerm = append(preferredSchedulingTerm, schedulingTerm)

	shuffleObjects(*h.rng, preferredSchedulingTerm)

	if newPod.Spec.Affinity == nil {
		newPod.Spec.Affinity = &affinity
	}
	if newPod.Spec.Affinity.NodeAffinity == nil {
		newPod.Spec.Affinity.NodeAffinity = &nodeAffinity
	}
	if newPod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution == nil {
		newPod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution = preferredSchedulingTerm
	} else {
		// add the preferred scheduling term to the existing preferred scheduling terms
		newPod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution = append(newPod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution, preferredSchedulingTerm...)
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
	log.Info("patching pod with node affinity", "pod", pod.Name, "newPod", newPod.Name)
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

func (h *handler) getPvNodes(ctx context.Context, pvName string) ([]string, *admission.Response) {
	var response admission.Response
	var nodeNamesList []string
	// Get the PV.
	var pv = &corev1.PersistentVolume{}
	getPV := func() error {
		return h.client.Get(ctx, client.ObjectKey{Name: pvName}, pv)
	}
	if err := retry.OnError(retry.DefaultRetry, isRetriableError, getPV); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		log.Error(err, "failed to get persistent volume", "pvName", pvName)
		response = admission.Errored(http.StatusBadRequest, err)
		return nil, &response
	}

	if pv != nil && pv.Spec.CSI != nil && pv.Spec.CSI.VolumeHandle != "" {
		// Get PV-s selected node annotation
		nodeName, ok := pv.Annotations[csi.SelectedNodeAnnotation]
		if !ok {
			// Get the node name from volume context
			nodeName, ok = pv.Spec.CSI.VolumeAttributes[csi.SelectedInitialNodeParam]
			if !ok {
				log.Error(fmt.Errorf("pv is not assigned to a node"), "no node name found", "pod", pvName)
				response = admission.Allowed("pv is not assigned to a node")
				return nil, &response
			}
		}
		nodeNamesList = append(nodeNamesList, nodeName)
	}

	return nodeNamesList, nil
}
