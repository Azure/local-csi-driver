// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"go.opentelemetry.io/otel/attribute"
	otcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"local-csi-driver/internal/csi/capability"
	"local-csi-driver/internal/csi/core"
	"local-csi-driver/internal/csi/mounter"
	"local-csi-driver/internal/pkg/events"
)

type Server struct {
	caps                     []*csi.ControllerServiceCapability
	modes                    []*csi.VolumeCapability_AccessMode
	volume                   core.ControllerInterface
	mounter                  mounter.Interface
	k8sClient                client.Client
	nodeId                   string
	selectedNodeAnnotation   string
	selectedInitialNodeParam string
	removePvNodeAffinity     bool
	recorder                 record.EventRecorder
	tracer                   trace.Tracer

	// Embed for forward compatibility.
	csi.UnimplementedControllerServer
}

// Server must implement the csi.ControllerServer interface.
var _ csi.ControllerServer = &Server{}

func New(volume core.ControllerInterface, caps []*csi.ControllerServiceCapability, modes []*csi.VolumeCapability_AccessMode, mounter mounter.Interface, k8sClient client.Client, nodeID, selectedNodeAnnotation string, selectedInitialNodeParam string, removePvNodeAffinity bool, recorder record.EventRecorder, tp trace.TracerProvider) *Server {
	return &Server{
		caps:                     caps,
		modes:                    modes,
		volume:                   volume,
		mounter:                  mounter,
		k8sClient:                k8sClient,
		nodeId:                   nodeID,
		selectedNodeAnnotation:   selectedNodeAnnotation,
		selectedInitialNodeParam: selectedInitialNodeParam,
		removePvNodeAffinity:     removePvNodeAffinity,
		recorder:                 recorder,
		tracer:                   tp.Tracer("localdisk.csi.acstor.io/internal/csi/controller"),
	}
}

// addThrottlingParamsToVolumeContext copies IO throttling parameters to volume context
// so they're available during NodePublishVolume for pod-level cgroup configuration
func (cs *Server) addThrottlingParamsToVolumeContext(vol *csi.Volume, parameters map[string]string) error {
	// Check if there are any IO throttling parameters
	throttleParams, err := ValidateIOThrottleParams(parameters)
	if err != nil {
		return fmt.Errorf("invalid IO throttling parameters: %w", err)
	}

	// If no throttling parameters, nothing to add
	if throttleParams == nil {
		return nil
	}

	// Initialize VolumeContext if it doesn't exist
	if vol.VolumeContext == nil {
		vol.VolumeContext = make(map[string]string)
	}

	// Copy throttling parameters to volume context with a prefix to avoid conflicts
	if throttleParams.RBPS != nil {
		vol.VolumeContext["csi.storage.k8s.io/throttle.rbps"] = fmt.Sprintf("%d", *throttleParams.RBPS)
	}
	if throttleParams.WBPS != nil {
		vol.VolumeContext["csi.storage.k8s.io/throttle.wbps"] = fmt.Sprintf("%d", *throttleParams.WBPS)
	}
	if throttleParams.RIOPS != nil {
		vol.VolumeContext["csi.storage.k8s.io/throttle.riops"] = fmt.Sprintf("%d", *throttleParams.RIOPS)
	}
	if throttleParams.WIOPS != nil {
		vol.VolumeContext["csi.storage.k8s.io/throttle.wiops"] = fmt.Sprintf("%d", *throttleParams.WIOPS)
	}

	return nil
}

// updateVolumeContextWithThrottlingParams updates the PersistentVolume's annotations
// with new throttling parameters from VolumeAttributesClass since spec.csi.volumeAttributes is immutable
func (cs *Server) updateVolumeContextWithThrottlingParams(ctx context.Context, volumeID string, throttleParams *IOThrottleParams) error {
	// Get the volume name from volume ID
	pvName, err := cs.volume.GetVolumeName(volumeID)
	if err != nil {
		return fmt.Errorf("failed to get volume name for volume ID %s: %w", volumeID, err)
	}

	// Use retry logic to handle resource conflicts
	const maxRetries = 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		// Get the latest PersistentVolume object
		pv := &corev1.PersistentVolume{}
		if err := cs.k8sClient.Get(ctx, client.ObjectKey{Name: pvName}, pv); err != nil {
			return fmt.Errorf("failed to get PersistentVolume %s: %w", pvName, err)
		}

		// Store original for patch operation
		original := pv.DeepCopy()

		// Initialize annotations if they don't exist
		if pv.Annotations == nil {
			pv.Annotations = make(map[string]string)
		}

		// Update throttling parameters in annotations (mutable unlike spec)
		if throttleParams.RBPS != nil {
			pv.Annotations["csi.storage.k8s.io/throttle.rbps"] = fmt.Sprintf("%d", *throttleParams.RBPS)
		}
		if throttleParams.WBPS != nil {
			pv.Annotations["csi.storage.k8s.io/throttle.wbps"] = fmt.Sprintf("%d", *throttleParams.WBPS)
		}
		if throttleParams.RIOPS != nil {
			pv.Annotations["csi.storage.k8s.io/throttle.riops"] = fmt.Sprintf("%d", *throttleParams.RIOPS)
		}
		if throttleParams.WIOPS != nil {
			pv.Annotations["csi.storage.k8s.io/throttle.wiops"] = fmt.Sprintf("%d", *throttleParams.WIOPS)
		}

		// Use Patch to avoid resource conflicts
		if err := cs.k8sClient.Patch(ctx, pv, client.MergeFrom(original)); err != nil {
			// If it's a conflict error and we haven't reached max retries, try again
			if strings.Contains(err.Error(), "the object has been modified") && attempt < maxRetries-1 {
				continue
			}
			return fmt.Errorf("failed to patch PersistentVolume %s after %d attempts: %w", pvName, attempt+1, err)
		}

		// Success - break out of retry loop
		return nil
	}

	return fmt.Errorf("failed to update PersistentVolume %s after %d attempts due to conflicts", pvName, maxRetries)
}

func (cs *Server) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	ctx, span := cs.tracer.Start(ctx, "csi.v1.Controller/CreateVolume", trace.WithAttributes(
		attribute.String("pv.name", req.GetName()),
		attribute.String("pvc.name", req.Parameters[core.PVCNameParam]),
		attribute.String("pvc.namespace", req.Parameters[core.PVCNamespaceParam]),
	))
	defer span.End()

	log := log.FromContext(ctx)

	// Validate controller capabilities.
	if err := capability.ValidateController(csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME, cs.caps); err != nil {
		span.SetStatus(otcodes.Error, "controller validation failed")
		span.RecordError(err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// Validate volume capabilities.
	if err := capability.ValidateVolume(req.GetVolumeCapabilities(), cs.modes); err != nil {
		span.SetStatus(otcodes.Error, "volume validation failed")
		span.RecordError(err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// Validate capacity.
	capacity := req.GetCapacityRange().GetRequiredBytes()
	limit := req.GetCapacityRange().GetLimitBytes()
	if capacity < 0 || limit < 0 {
		return nil, status.Error(codes.InvalidArgument, "cannot have negative capacity")
	}
	if limit > 0 && capacity > limit {
		return nil, status.Error(codes.InvalidArgument, "capacity cannot exceed limit")
	}

	// Fetch PVC to be used as reference object in events.
	var pvc *corev1.PersistentVolumeClaim
	if req.Parameters[core.PVCNameParam] != "" && req.Parameters[core.PVCNamespaceParam] != "" {
		pvc = &corev1.PersistentVolumeClaim{}
		if err := cs.k8sClient.Get(ctx, client.ObjectKey{Namespace: req.Parameters[core.PVCNamespaceParam], Name: req.Parameters[core.PVCNameParam]}, pvc); err != nil {
			// We error in this scenario because we need the PVC to be able to
			// be able to check the owner references and set the volume to be
			// accessible from all nodes if it is not a generic ephemeral volume.
			log.Error(err, "failed to get pvc", "name", req.Parameters[core.PVCNameParam], "namespace", req.Parameters[core.PVCNamespaceParam])
			return nil, status.Error(codes.Internal, err.Error())
		}
		ctx = events.WithObjectIntoContext(ctx, cs.recorder, pvc)
	}

	// Create using the volume api.
	vol, err := cs.volume.Create(ctx, req)
	if err != nil {
		log.Error(err, "failed to create volume", "name", req.GetName())
		span.SetStatus(otcodes.Error, "CreateVolume failed")
		span.RecordError(err)
		return nil, fromCoreError(err)
	}

	// Copy IO throttling parameters to VolumeContext so they're available in NodePublishVolume
	// Check both regular parameters (StorageClass) and mutable parameters (VolumeAttributesClass)
	allParams := make(map[string]string)

	// Start with regular parameters
	for k, v := range req.GetParameters() {
		allParams[k] = v
	}

	// Add mutable parameters (these take precedence if there are conflicts)
	for k, v := range req.GetMutableParameters() {
		allParams[k] = v
	}

	log.V(2).Info("CreateVolume checking for throttling parameters",
		"regularParameters", req.GetParameters(),
		"mutableParameters", req.GetMutableParameters(),
		"combinedParameters", allParams)

	if err := cs.addThrottlingParamsToVolumeContext(vol, allParams); err != nil {
		log.Error(err, "Failed to add throttling parameters to volume context", "volumeId", vol.GetVolumeId())
		// Don't fail volume creation if we can't add throttling params
		span.AddEvent("throttling parameters not added to volume context", trace.WithAttributes(
			attribute.String("error", err.Error()),
		))
	} else {
		log.V(2).Info("CreateVolume final volume context", "volumeId", vol.GetVolumeId(), "volumeContext", vol.VolumeContext)
	}

	// Remove node affinity from non-generic ephemeral volumes if the flag is enabled.
	// This makes the persistent volume accessible from all nodes and eliminates
	// the need for manual recovery during cluster restarts, where node names might change.
	// This approach works effectively when a webhook enforces hyperconvergence of workloads
	// and volumes; otherwise, it may not be suitable.
	if cs.removePvNodeAffinity {
		if pvc == nil {
			// We get the pvc from the request above, if we skip it will be nil
			// and we will not be able to set the volume to be accessible from all nodes.
			log.V(2).Info("CreateVolume succeeded but pvc namespace or name not found")
			span.SetStatus(otcodes.Ok, "CreateVolume succeeded but pvc namespace or name not found")
			return &csi.CreateVolumeResponse{Volume: vol}, nil
		}

		// if removePvNodeAffinity is set to true in favor of handling affinity
		// through a webhook, we need to set the volume to be accessible
		// from all nodes. We still keep a reference to the selected initial
		// node in the volume context for the webhook to use.
		if vol.VolumeContext == nil {
			vol.VolumeContext = make(map[string]string)
		}
		vol.VolumeContext[cs.selectedInitialNodeParam] = cs.nodeId
		vol.AccessibleTopology = nil
	}

	span.SetStatus(otcodes.Ok, "volume created")
	return &csi.CreateVolumeResponse{Volume: vol}, nil
}

func (cs *Server) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	ctx, span := cs.tracer.Start(ctx, "csi.v1.Controller/DeleteVolume", trace.WithAttributes(
		attribute.String("vol.id", req.GetVolumeId()),
	))
	defer span.End()

	log := log.FromContext(ctx)

	if req.GetVolumeId() == "" {
		span.SetStatus(otcodes.Error, "volume id missing")
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}

	// If we cannot retrieve the volume name from the volume id, then is it invalid.
	// In this condition, the sanity tests expect us to return OK
	pvName, err := cs.volume.GetVolumeName(req.GetVolumeId())
	if err != nil {
		log.Error(err, "failed to get volume name", "volumeID", req.GetVolumeId())
		span.SetStatus(otcodes.Ok, "volume not found")
		return &csi.DeleteVolumeResponse{}, nil
	}
	span.SetAttributes(attribute.String("pv.name", pvName))

	// If pv node affinity is removed, every instance of the controller server
	// will receive the delete volume request. We need to check if the volume
	// belongs to the current node and if not, we need to return an error.
	var pv *corev1.PersistentVolume
	if cs.removePvNodeAffinity {
		pv = &corev1.PersistentVolume{}
		// Get the pv and check selected node annotation
		if err := cs.k8sClient.Get(ctx, client.ObjectKey{Name: pvName}, pv); err != nil {
			if client.IgnoreNotFound(err) != nil {
				log.Error(err, "failed to get pv", "name", pvName)
				span.SetStatus(otcodes.Error, "DeleteVolume failed")
				span.RecordError(err)
				return nil, status.Error(codes.Internal, err.Error())
			}
			// If the pv is not found, it means it has already been deleted.
			// This is not an error, so we return success.
			log.V(2).Info("pv not found, assuming it has already been deleted", "name", pvName)
			span.SetStatus(otcodes.Ok, "volume not found")
			return &csi.DeleteVolumeResponse{}, nil
		}

		// Check the selected node annotation and if it exists, only allow deletion
		// if the node name matches the selected node.
		node, ok := pv.Annotations[cs.selectedNodeAnnotation]
		if !ok {
			// Get the node name from volume context
			if pv.Spec.CSI != nil {
				node = pv.Spec.CSI.VolumeAttributes[cs.selectedInitialNodeParam]
			}
		}
		if cs.nodeId != "" && node != "" {
			// Check if the node exists in the cluster.
			// If it does not exist, we can return success without deleting the
			// volume.
			if err := cs.k8sClient.Get(ctx, client.ObjectKey{Name: node}, &corev1.Node{}); err != nil {
				if client.IgnoreNotFound(err) == nil { // node not found
					log.V(2).Info("node not found, assuming it has been deleted", "name", node)
					span.SetStatus(otcodes.Ok, "node not found")
					return &csi.DeleteVolumeResponse{}, nil
				}
				log.Error(err, "failed to get node", "name", node)
			}

			// If the nodeName does not match the selected node, we cannot
			// delete the volume.
			if !strings.EqualFold(node, cs.nodeId) {
				span.SetStatus(otcodes.Error, "DeleteVolume failed")
				return nil, status.Error(codes.FailedPrecondition, "Volume is on a different node "+node)
			}
		}
	}

	if pv == nil {
		// If the pv is nil, it means we are not using the removePvNodeAffinity
		// flag. Avoid the extra GET call to get the pv if we are not using the
		// flag.
		pv = &corev1.PersistentVolume{}
		if err := cs.k8sClient.Get(ctx, client.ObjectKey{Name: pvName}, pv); err != nil {
			log.Error(err, "failed to get persistent volume")
			// Getting PV for events recording will be best effort, we could be
			// running outside of a k8s cluster, so we don't want to fail the
			// delete volume request if we can't get the PV.
		}
	}

	if pv != nil {
		// The PVC is deleted by this point, so use events on PV.
		ctx = events.WithObjectIntoContext(ctx, cs.recorder, pv)
	}

	// Since NodeUnstageVolume is a no-op to preserve the page cache between
	// pods using the same volume, we need to unmount the device here if it is
	// mounted. This is because the device will be removed from the node and the
	// mount will not be cleaned up.
	devicePath, err := cs.volume.GetNodeDevicePath(req.GetVolumeId())
	if err != nil {
		span.SetStatus(otcodes.Error, "failed to get node device path")
		span.RecordError(err)
		return nil, status.Errorf(codes.Internal, "failed to get node device path: %v", err)
	}
	span.SetAttributes(attribute.String("device.path", devicePath))

	if devicePath != "" {
		log.V(2).Info("unmounting volume before deletion", "devicePath", devicePath)
		if err := cs.mounter.CleanupStagingDir(ctx, devicePath); err != nil {
			span.SetStatus(otcodes.Error, "failed to unmount before volume deletion")
			span.RecordError(err)
			return nil, status.Errorf(codes.Internal, "failed to unmount device path %q: %v", devicePath, err)
		}
		span.AddEvent("unmounted device", trace.WithAttributes(attribute.String("device.path", devicePath)))
	}

	if err := cs.volume.Delete(ctx, req); err != nil {
		log.Error(err, "failed to delete volume", "id", req.GetVolumeId())
		span.SetStatus(otcodes.Error, "DeleteVolume failed")
		span.RecordError(err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	span.AddEvent("volume deleted")
	span.SetStatus(otcodes.Ok, "volume deleted")
	return &csi.DeleteVolumeResponse{}, nil
}

func (cs *Server) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (cs *Server) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (cs *Server) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	ctx, span := cs.tracer.Start(ctx, "csi.v1.Controller/ValidateVolumeCapabilities", trace.WithAttributes(
		attribute.String("vol.id", req.GetVolumeId()),
	))
	defer span.End()

	if len(req.GetVolumeId()) == 0 {
		span.SetStatus(otcodes.Error, "volume id missing")
		span.RecordError(status.Error(codes.InvalidArgument, "Volume ID missing in request"))
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}

	if err := capability.ValidateVolume(req.GetVolumeCapabilities(), cs.modes); err != nil {
		span.SetStatus(otcodes.Error, "volume validation failed")
		span.RecordError(err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	resp, err := cs.volume.ValidateCapabilities(ctx, req)
	if err != nil {
		if errors.Is(err, core.ErrVolumeNotFound) {
			span.SetStatus(otcodes.Error, "volume not found")
			span.RecordError(err)
			return nil, status.Error(codes.NotFound, err.Error())
		}
		span.SetStatus(otcodes.Error, "ValidateVolumeCapabilities failed")
		span.RecordError(err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	span.SetStatus(otcodes.Ok, "volume capabilities validated")
	return resp, nil
}

func (cs *Server) ListVolumes(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	ctx, span := cs.tracer.Start(ctx, "csi.v1.Controller/ListVolumes", trace.WithAttributes(
		attribute.String("token.start", req.StartingToken),
		attribute.Int("max.entries", int(req.MaxEntries)),
	))
	defer span.End()

	log := log.FromContext(ctx)

	start := 0
	if req.StartingToken != "" {
		var err error
		start, err = strconv.Atoi(req.StartingToken)
		if err != nil {
			span.SetStatus(otcodes.Error, "ListVolumes starting token parsing failed")
			span.RecordError(err)
			return nil, status.Errorf(codes.Aborted, "ListVolumes starting token(%s) parsing with error: %v", req.StartingToken, err)
		}
		if start < 0 {
			span.SetStatus(otcodes.Error, "ListVolumes starting token negative")
			return nil, status.Errorf(codes.Aborted, "ListVolumes starting token(%d) can not be negative", start)
		}
	}

	resp, err := cs.volume.List(ctx, req)
	if err != nil {
		log.Error(err, "failed to list volumes")
		span.SetStatus(otcodes.Error, "DeleteVolume failed")
		span.RecordError(err)
		return nil, status.Error(codes.Internal, err.Error())
	}
	span.SetStatus(otcodes.Ok, "volumes listed")
	return resp, nil
}

func (cs *Server) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	paramSlice := []string{}
	for k, v := range req.Parameters {
		paramSlice = append(paramSlice, k+"="+v)
	}
	ctx, span := cs.tracer.Start(ctx, "csi.v1.Controller/GetCapacity", trace.WithAttributes(
		attribute.StringSlice("parameters", paramSlice),
	))
	defer span.End()

	log := log.FromContext(ctx)

	resp, err := cs.volume.GetCapacity(ctx, req)
	if err != nil {
		log.Error(err, "failed to get capacity")
		span.SetStatus(otcodes.Error, "GetCapacity failed")
		span.RecordError(err)
		return nil, status.Error(codes.Internal, err.Error())
	}
	span.SetStatus(otcodes.Ok, "capacity retrieved")
	return resp, nil
}

func (cs *Server) ControllerModifyVolume(ctx context.Context, req *csi.ControllerModifyVolumeRequest) (*csi.ControllerModifyVolumeResponse, error) {
	ctx, span := cs.tracer.Start(ctx, "csi.v1.Controller/ControllerModifyVolume", trace.WithAttributes(
		attribute.String("vol.id", req.GetVolumeId()),
	))
	defer span.End()

	log := log.FromContext(ctx)
	log.Info("ControllerModifyVolume called", "volumeId", req.GetVolumeId())

	// Validate controller capabilities.
	if err := capability.ValidateController(csi.ControllerServiceCapability_RPC_MODIFY_VOLUME, cs.caps); err != nil {
		span.SetStatus(otcodes.Error, "controller validation failed")
		span.RecordError(err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if len(req.GetVolumeId()) == 0 {
		span.SetStatus(otcodes.Error, "volume id missing")
		span.RecordError(status.Error(codes.InvalidArgument, "Volume ID missing in request"))
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}

	// Get mutable parameters from the request
	mutableParams := req.GetMutableParameters()
	if len(mutableParams) == 0 {
		log.V(2).Info("No mutable parameters provided, nothing to modify")
		span.SetStatus(otcodes.Ok, "no modifications needed")
		return &csi.ControllerModifyVolumeResponse{}, nil
	}

	log.V(3).Info("Processing mutable parameters", "mutableParameters", mutableParams)

	// Validate IO throttling parameters
	throttleParams, err := ValidateIOThrottleParams(mutableParams)
	if err != nil {
		log.Error(err, "Invalid IO throttling parameters", "mutableParameters", mutableParams)
		span.SetStatus(otcodes.Error, "parameter validation failed")
		span.RecordError(err)
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("Invalid mutable parameters: %v", err))
	}

	// If no throttling parameters were provided, we're done
	if throttleParams == nil {
		log.V(2).Info("No IO throttling parameters found in mutable parameters")
		span.SetStatus(otcodes.Ok, "no throttling parameters to configure")
		return &csi.ControllerModifyVolumeResponse{}, nil
	}

	// Get the volume name from volume ID for node targeting validation
	pvName, err := cs.volume.GetVolumeName(req.GetVolumeId())
	if err != nil {
		log.Error(err, "failed to get volume name", "volumeID", req.GetVolumeId())
		span.SetStatus(otcodes.Error, "failed to get volume name")
		return nil, status.Error(codes.Internal, fmt.Sprintf("Failed to get volume name: %v", err))
	}
	span.SetAttributes(attribute.String("pv.name", pvName))

	// If pv node affinity is removed, every instance of the controller server
	// will receive the modify volume request. We need to check if the volume
	// belongs to the current node and if not, we need to return an error.
	var pv *corev1.PersistentVolume
	if cs.removePvNodeAffinity {
		pv = &corev1.PersistentVolume{}
		// Get the pv and check selected node annotation
		if err := cs.k8sClient.Get(ctx, client.ObjectKey{Name: pvName}, pv); err != nil {
			if client.IgnoreNotFound(err) != nil {
				log.Error(err, "failed to get pv", "name", pvName)
				span.SetStatus(otcodes.Error, "ControllerModifyVolume failed")
				span.RecordError(err)
				return nil, status.Error(codes.Internal, err.Error())
			}
			// If the pv is not found, it means it has already been deleted.
			log.V(2).Info("pv not found, volume may have been deleted", "name", pvName)
			span.SetStatus(otcodes.Ok, "volume not found")
			return &csi.ControllerModifyVolumeResponse{}, nil
		}

		// Check the selected node annotation and if it exists, only allow modification
		// if the node name matches the selected node.
		node, ok := pv.Annotations[cs.selectedNodeAnnotation]
		if !ok {
			// Get the node name from volume context
			if pv.Spec.CSI != nil {
				node = pv.Spec.CSI.VolumeAttributes[cs.selectedInitialNodeParam]
			}
		}
		if cs.nodeId != "" && node != "" {
			// Check if the node exists in the cluster.
			// If it does not exist, we can return an error as we cannot modify
			// a volume on a non-existent node.
			if err := cs.k8sClient.Get(ctx, client.ObjectKey{Name: node}, &corev1.Node{}); err != nil {
				if client.IgnoreNotFound(err) == nil { // node not found
					log.V(2).Info("node not found, cannot modify volume on deleted node", "name", node)
					span.SetStatus(otcodes.Error, "target node not found")
					return nil, status.Error(codes.FailedPrecondition, "Cannot modify volume on deleted node "+node)
				}
				log.Error(err, "failed to get node", "name", node)
			}

			// If the nodeName does not match the selected node, we cannot
			// modify the volume. Return FailedPrecondition to let Kubernetes retry on the correct node.
			// Include target node information to help with debugging and potential smart routing
			if !strings.EqualFold(node, cs.nodeId) {
				log.V(2).Info("volume is on a different node, rejecting modification request",
					"volumeId", req.GetVolumeId(), "targetNode", node, "currentNode", cs.nodeId)
				span.SetStatus(otcodes.Error, "ControllerModifyVolume failed")
				// Enhanced error message with target node info for potential smart routing
				return nil, status.Error(codes.FailedPrecondition,
					fmt.Sprintf("Volume is on node %s, current controller is on node %s. CSI-resizer should retry on correct node.",
						node, cs.nodeId))
			}
		}
	}

	log.V(2).Info("ControllerModifyVolume validated node targeting",
		"volumeId", req.GetVolumeId(), "node", cs.nodeId)

	// For ControllerModifyVolume, we need to update the PersistentVolume's VolumeContext
	// so the new throttling parameters are available during the next NodePublishVolume call
	// This requires updating the PV object in the Kubernetes API
	log.V(2).Info("ControllerModifyVolume updating PV with throttling parameters",
		"volumeId", req.GetVolumeId(),
		"throttleParams", throttleParams)
	err = cs.updateVolumeContextWithThrottlingParams(ctx, req.GetVolumeId(), throttleParams)
	if err != nil {
		log.Error(err, "Failed to update volume context with throttling parameters", "volumeId", req.GetVolumeId())
		span.SetStatus(otcodes.Error, "failed to update volume context")
		span.RecordError(err)
		return nil, status.Error(codes.Internal, fmt.Sprintf("Failed to update volume context: %v", err))
	}

	log.V(2).Info("ControllerModifyVolume successfully updated PV with throttling parameters", "volumeId", req.GetVolumeId())

	log.Info("Successfully updated volume with IO throttling parameters",
		"volumeId", req.GetVolumeId(),
		"rbps", throttleParams.RBPS,
		"wbps", throttleParams.WBPS,
		"riops", throttleParams.RIOPS,
		"wiops", throttleParams.WIOPS,
	)

	span.AddEvent("volume context updated with throttling parameters")
	span.SetStatus(otcodes.Ok, "volume modification completed")
	return &csi.ControllerModifyVolumeResponse{}, nil
}

// Default supports all capabilities.
func (cs *Server) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	ctx, span := cs.tracer.Start(ctx, "csi.v1.Controller/ControllerGetCapabilities")
	defer span.End()

	log := log.FromContext(ctx)

	// Log all capabilities being advertised
	capabilityNames := make([]string, len(cs.caps))
	for i, cap := range cs.caps {
		if cap.GetRpc() != nil {
			capabilityNames[i] = cap.GetRpc().GetType().String()
		}
	}

	log.Info("ControllerGetCapabilities called",
		"capabilities", capabilityNames,
		"capabilityCount", len(cs.caps))

	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: cs.caps,
	}, nil
}

func (cs *Server) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (cs *Server) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (cs *Server) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

// fromCoreError converts core errors to gRPC status errors.
func fromCoreError(err error) error {
	switch {
	case errors.Is(err, core.ErrResourceExhausted):
		return status.Error(codes.ResourceExhausted, err.Error())
	case errors.Is(err, core.ErrVolumeSizeMismatch):
		return status.Error(codes.AlreadyExists, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
