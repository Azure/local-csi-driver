// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
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
	"local-csi-driver/internal/csi/core/lvm"
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
			// check the owner references and set the volume to be
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

	// Get the PV to check node affinity topology and for event recording.
	// Every instance of the controller server will receive the delete volume request
	// when node affinity is removed from PVs. We need to check if the volume belongs
	// to the current node based on its topology or selected node annotation.
	pv := &corev1.PersistentVolume{}
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

	// Check if PV has hostname topology. If it does, the CSI provisioner already
	// ensured this request went to the correct node, so we can proceed.
	// If it doesn't have hostname topology (removed in favor of using webhooks), we need
	// to check the selected node annotation or volume context to ensure only the
	// correct node handles the deletion.
	hasHostnameTopology := pvHasHostnameTopology(pv)

	if !hasHostnameTopology {
		// PV doesn't have hostname topology, so we need to check if this is the
		// correct node to handle the deletion based on annotations or volume context.
		selectedNode := ""

		// Check the selected node annotation first
		if pv.Annotations != nil {
			selectedNode = pv.Annotations[cs.selectedNodeAnnotation]
		}

		// Fall back to volume context if annotation is not set
		if selectedNode == "" && pv.Spec.CSI != nil && pv.Spec.CSI.VolumeAttributes != nil {
			selectedNode = pv.Spec.CSI.VolumeAttributes[cs.selectedInitialNodeParam]
		}

		if cs.nodeId != "" && selectedNode != "" {
			// Check if the node exists in the cluster.
			// If it does not exist, we can return success without deleting the volume
			// since the PV cleanup controller will handle finalizer removal.
			if err := cs.k8sClient.Get(ctx, client.ObjectKey{Name: selectedNode}, &corev1.Node{}); err != nil {
				if client.IgnoreNotFound(err) == nil { // node not found
					log.V(2).Info("node not found, assuming it has been deleted", "name", selectedNode)
					span.SetStatus(otcodes.Ok, "node not found")
					return &csi.DeleteVolumeResponse{}, nil
				}
				log.Error(err, "failed to get node", "name", selectedNode)
			}

			// If the nodeName does not match the selected node, we cannot delete the volume.
			if !strings.EqualFold(selectedNode, cs.nodeId) {
				span.SetStatus(otcodes.Error, "DeleteVolume failed - wrong node")
				return nil, status.Error(codes.FailedPrecondition, "Volume is on a different node "+selectedNode)
			}
		}
	}

	// The PVC is deleted by this point, so use events on PV.
	ctx = events.WithObjectIntoContext(ctx, cs.recorder, pv)

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

func (cs *Server) ControllerGetVolume(context.Context, *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (cs *Server) ControllerModifyVolume(context.Context, *csi.ControllerModifyVolumeRequest) (*csi.ControllerModifyVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

// Default supports all capabilities.
func (cs *Server) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
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

// pvHasHostnameTopology checks if a PV has hostname-based node affinity topology.
// Returns true if the PV has node affinity with the driver's topology key.
func pvHasHostnameTopology(pv *corev1.PersistentVolume) bool {
	if pv == nil || pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
		return false
	}

	// Import the topology key from the lvm package
	const topologyKey = lvm.TopologyKey

	for _, term := range pv.Spec.NodeAffinity.Required.NodeSelectorTerms {
		for _, expr := range term.MatchExpressions {
			if expr.Key == topologyKey && expr.Operator == corev1.NodeSelectorOpIn {
				return true
			}
		}
	}

	return false
}
