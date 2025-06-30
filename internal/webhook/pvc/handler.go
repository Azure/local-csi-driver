// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package pvc

import (
	"context"
	"net/http"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var log = ctrl.Log.WithName("ephemeral")

type Handler struct {
	driverName string
	client     client.Client
	decoder    admission.Decoder
	recorder   record.EventRecorder
}

// opType is they type of request being processed.
type opType string

const (
	unknown                             opType = "unknown"
	create                              opType = "create"
	acceptEphemeralStorageAnnotationKey        = "localdisk.csi.acstor.io/accept-ephemeral-storage"
)

// Handler implements admission.Handler.
var _ admission.Handler = &Handler{}

func NewHandler(driverName string, client client.Client, scheme *runtime.Scheme, recorder record.EventRecorder) (*Handler, error) {
	return &Handler{
		driverName: driverName,
		client:     client,
		decoder:    admission.NewDecoder(scheme),
		recorder:   recorder,
	}, nil
}

// Handler validates whether a pvc operation should be permitted.
func (h *Handler) Handle(ctx context.Context, req admission.Request) admission.Response {
	var (
		pvc   = &corev1.PersistentVolumeClaim{}
		sc    = &storagev1.StorageClass{}
		name  = req.Name
		start = time.Now()
	)

	// Update Handler latency metrics.
	defer func() {
		HandlerDuration.Observe(time.Since(start))
	}()

	// Whenever we return a response, wrap it in this func so we can capture it
	// in metrics.
	observeResponse := func(op opType, resp admission.Response) admission.Response {
		HandlerResponse.Increment(op, resp)
		return resp
	}

	log.V(1).Info("pvc handling request", "kind", req.Kind.Kind, "request", req.Name, "operation", req.Operation)

	// We shouldn't get requests for anything other than creating pvcs, but if
	// we do, allow them immediately.
	if req.Kind.Kind != "PersistentVolumeClaim" || req.Operation != admissionv1.Create {
		return observeResponse(unknown, admission.Allowed("unhandled request allowed"))
	}

	// Decode the request into a pvc object.
	err := h.decoder.Decode(req, pvc)
	if err != nil {
		log.Error(err, "failed to decode pvc")
		return observeResponse(create, admission.Errored(http.StatusBadRequest, err))
	}

	log := log.WithValues("pvc", name, "namespace", req.Namespace, "storageclass", pvc.Spec.StorageClassName, "operation", create)

	// Allow PVCs with an annotation indicating accepting ephemeral storage.
	if pvc.Annotations != nil && pvc.Annotations[acceptEphemeralStorageAnnotationKey] == "true" {
		log.Info("allowing annotated volume")
		return observeResponse(create, admission.Allowed("volume has replicas"))
	}

	// Ignore PVCs without a storage class.
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName == "" {
		log.Info("allowing pvc with no storage class")
		return observeResponse(create, admission.Allowed("no storage class"))
	}

	// Get the StorageClass, retrying on errors. Uses a low timeout so we don't
	// block the request for too long, but can still handle transient
	// errors.
	getStorageClass := func() error {
		return h.client.Get(ctx, client.ObjectKey{Name: *pvc.Spec.StorageClassName}, sc)
	}
	if err := retry.OnError(retry.DefaultRetry, isRetriableError, getStorageClass); err != nil {
		if apierrors.IsNotFound(err) {
			log.Error(err, "storage class not found")
			return observeResponse(create, admission.Allowed("storage class not found"))
		}
		log.Error(err, "failed to get storage class")
		return observeResponse(create, admission.Errored(http.StatusBadRequest, err))
	}

	// Ignore PVCs using non-local-csi-driver storage classes.
	if sc.Provisioner != h.driverName {
		log.Info("allowing pvc provisioned by other drivers", "driver", sc.Provisioner)
		return observeResponse(create, admission.Allowed("not a local-csi-driver storage class"))
	}

	// Reject PVCs that are not created as generic ephemeral volumes:
	// https://kubernetes.io/docs/concepts/storage/ephemeral-volumes/#generic-ephemeral-volumes
	// These volumes will have a Pod as the owner.
	for _, owner := range pvc.OwnerReferences {
		if owner.Kind == "Pod" {
			log.Info("allowing ephemeral volume", "owner", owner.Name)
			return observeResponse(create, admission.Allowed("volume is ephemeral"))
		}
	}

	log.Info("denying pv create", "reason", "only generic ephemeral volumes are allowed for localdisk.csi.acstor.io provisioner")
	return observeResponse(create, admission.Denied("only generic ephemeral volumes are allowed for localdisk.csi.acstor.io provisioner"))
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
