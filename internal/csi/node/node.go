// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package node

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"go.opentelemetry.io/otel/attribute"
	otcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/tools/record"
	"k8s.io/kubernetes/pkg/volume"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"local-csi-driver/internal/csi/capability"
	"local-csi-driver/internal/csi/controller"
	"local-csi-driver/internal/csi/core"
	"local-csi-driver/internal/csi/core/lvm"
	"local-csi-driver/internal/csi/mounter"
	"local-csi-driver/internal/pkg/events"
)

const (
	defaultFsType = "ext4"
)

type Server struct {
	volume                   core.NodeInterface
	nodeID                   string
	driver                   string
	caps                     []*csi.NodeServiceCapability
	mounter                  mounter.Interface
	k8sClient                client.Client
	selectedNodeAnnotation   string
	selectedInitialNodeParam string
	removePvNodeAffinity     bool
	recorder                 record.EventRecorder
	tracer                   trace.Tracer

	// Embed for forward compatibility.
	csi.UnimplementedNodeServer
}

// Server must implement the csi.NodeServer interface.
var _ csi.NodeServer = &Server{}

func New(volume core.NodeInterface, nodeID, selectedNodeAnnotation, selectedInitialNodeParam string, driver string, caps []*csi.NodeServiceCapability, mounter mounter.Interface, k8sClient client.Client, removeNodeAffinity bool, recorder record.EventRecorder, tp trace.TracerProvider) *Server {
	return &Server{
		nodeID:                   nodeID,
		driver:                   driver,
		caps:                     caps,
		mounter:                  mounter,
		volume:                   volume,
		k8sClient:                k8sClient,
		selectedNodeAnnotation:   selectedNodeAnnotation,
		selectedInitialNodeParam: selectedInitialNodeParam,
		removePvNodeAffinity:     removeNodeAffinity,
		recorder:                 recorder,
		tracer:                   tp.Tracer("localdisk.csi.acstor.io/internal/csi/node"),
	}
}

func (ns *Server) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	ctx, span := ns.tracer.Start(ctx, "csi.v1.Node/NodePublishVolume", trace.WithAttributes(
		attribute.String("vol.id", req.GetVolumeId()),
	))
	defer span.End()

	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		span.SetStatus(otcodes.Error, "Volume ID missing in request")
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}

	volumeCapability := req.GetVolumeCapability()
	if volumeCapability == nil {
		span.SetStatus(otcodes.Error, "Volume capability missing in request")
		return nil, status.Error(codes.InvalidArgument, "Volume capability missing in request")
	}

	if err := capability.IsValidVolumeCapabilities([]*csi.VolumeCapability{volumeCapability}); err != nil {
		span.SetStatus(otcodes.Error, "Invalid volume capability")
		span.RecordError(err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	stagingTargetPath := req.GetStagingTargetPath()
	if len(stagingTargetPath) == 0 {
		span.SetStatus(otcodes.Error, "Staging target path not provided")
		return nil, status.Error(codes.InvalidArgument, "Staging target path not provided")
	}

	targetPath := req.GetTargetPath()
	if len(targetPath) == 0 {
		span.SetStatus(otcodes.Error, "Target path not provided")
		return nil, status.Error(codes.InvalidArgument, "Target path not provided")
	}

	log := log.FromContext(ctx).WithName("NodePublishVolume").WithValues("volumeID", volumeID)

	// Log the complete volume context received
	volumeContext := req.GetVolumeContext()
	log.Info("NodePublishVolume received request parameters",
		"volumeId", volumeID,
		"stagingTargetPath", stagingTargetPath,
		"targetPath", targetPath,
		"readonly", req.GetReadonly(),
		"volumeContext", volumeContext,
		"secretsCount", len(req.GetSecrets()),
		"volumeCapability", req.GetVolumeCapability(),
	)

	mountOptions := []string{"bind"}
	if req.GetReadonly() {
		mountOptions = append(mountOptions, "ro")
	}

	switch req.GetVolumeCapability().GetAccessType().(type) {
	case *csi.VolumeCapability_Block:
		parentDirectory := filepath.Dir(targetPath)
		if _, err := ns.EnsureMount(parentDirectory); err != nil {
			span.SetStatus(otcodes.Error, "failed to create parent directory")
			span.RecordError(err)
			return nil, status.Error(codes.Internal, err.Error())
		}

		log.V(2).Info("creating target file for raw block volume")
		if err := ns.EnsureTargetFile(targetPath); err != nil {
			span.SetStatus(otcodes.Error, "failed to create target file")
			span.RecordError(err)
			return nil, status.Error(codes.Internal, err.Error())
		}

		log.V(2).Info("mounting")
		var err error
		stagingTargetPath, err = ns.volume.GetNodeDevicePath(volumeID)
		if err != nil {
			span.SetStatus(otcodes.Error, "failed to get node device path")
			span.RecordError(err)
			return nil, err
		}
	case *csi.VolumeCapability_Mount:
		mnt, err := ns.EnsureMount(targetPath)
		if err != nil {
			span.SetStatus(otcodes.Error, "failed to ensure mount")
			span.RecordError(err)
			return nil, status.Errorf(codes.Internal, "could not mount target: %v", err)
		}
		if mnt {
			log.V(2).Info("already mounted", "target", targetPath)
			span.SetStatus(otcodes.Ok, "already mounted")
			return &csi.NodePublishVolumeResponse{}, nil
		}
	}

	log.V(2).Info("mounting", "source", stagingTargetPath, "target", targetPath)
	if err := ns.mounter.Mount(stagingTargetPath, targetPath, "", mountOptions); err != nil {
		span.SetStatus(otcodes.Error, "failed to mount")
		span.RecordError(err)
		return nil, CheckMountError(fmt.Errorf("could not mount: %v", err))
	}

	log.V(2).Info("mount successful", "source", stagingTargetPath, "target", targetPath)

	// Apply IO throttling configuration to the pod's cgroup if parameters are present
	if err := ns.applyPodIOThrottling(ctx, volumeContext, targetPath, volumeID); err != nil {
		log.Error(err, "Failed to apply IO throttling to pod", "volumeId", volumeID, "targetPath", targetPath)
		// Don't fail the mount if throttling configuration fails
		span.AddEvent("IO throttling configuration failed", trace.WithAttributes(
			attribute.String("error", err.Error()),
		))
	}

	span.SetStatus(otcodes.Ok, "mount successful")
	return &csi.NodePublishVolumeResponse{}, nil
}

func (ns *Server) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	ctx, span := ns.tracer.Start(ctx, "csi.v1.Node/NodeUnpublishVolume", trace.WithAttributes(
		attribute.String("vol.id", req.GetVolumeId()),
	))
	defer span.End()

	targetPath := req.GetTargetPath()
	volumeID := req.GetVolumeId()

	if len(volumeID) == 0 {
		span.SetStatus(otcodes.Error, "Volume ID missing in request")
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}
	if len(targetPath) == 0 {
		span.SetStatus(otcodes.Error, "Target path missing in request")
		return nil, status.Error(codes.InvalidArgument, "Target path missing in request")
	}
	log := log.FromContext(ctx).WithName("NodeUnpublishVolume").WithValues("volumeID", volumeID)

	log.V(2).Info("unmounting volume")

	if err := ns.mounter.CleanupMountPoint(targetPath); err != nil {
		span.SetStatus(otcodes.Error, "failed to unmount")
		span.RecordError(err)
		return nil, status.Errorf(codes.Internal, "failed to unmount target %q: %v", targetPath, err)
	}

	log.V(2).Info("unmounted volume")
	span.SetStatus(otcodes.Ok, "unmounted volume")
	return &csi.NodeUnpublishVolumeResponse{}, nil

}

func (ns *Server) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	_, span := ns.tracer.Start(ctx, "csi.v1.Node/NodeGetInfo")
	defer span.End()
	return &csi.NodeGetInfoResponse{
		NodeId: ns.nodeID,
		AccessibleTopology: &csi.Topology{
			Segments: map[string]string{
				ns.topologyKey(): ns.topologyValue(),
			},
		},
	}, nil
}

func (ns *Server) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	_, span := ns.tracer.Start(ctx, "csi.v1.Node/NodeGetCapabilities")
	defer span.End()
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: ns.caps,
	}, nil
}

func (ns *Server) NodeGetVolumeStats(ctx context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	_, span := ns.tracer.Start(ctx, "csi.v1.Node/NodeGetVolumeStats", trace.WithAttributes(
		attribute.String("vol.id", req.GetVolumeId()),
	))
	defer span.End()

	if len(req.GetVolumeId()) == 0 {
		span.SetStatus(otcodes.Error, "Volume ID was empty")
		return nil, status.Error(codes.InvalidArgument, "volume ID was empty")
	}
	if len(req.GetVolumePath()) == 0 {
		span.SetStatus(otcodes.Error, "Volume path was empty")
		return nil, status.Error(codes.InvalidArgument, "volume path was empty")
	}

	exists, err := ns.mounter.PathExists(req.GetVolumePath())
	if err != nil {
		span.SetStatus(otcodes.Error, "failed to check if path exists")
		span.RecordError(err)
		return nil, status.Errorf(codes.Internal, "failed to stat volume path %s: %v", req.GetVolumePath(), err)
	}
	if !exists {
		span.SetStatus(otcodes.Error, "volume path does not exist")
		return nil, status.Error(codes.NotFound, "volume path does not exist")
	}

	isBlock, err := ns.mounter.PathIsDevice(req.GetVolumePath())
	if err != nil {
		span.SetStatus(otcodes.Error, "failed to determine if path is block device")
		span.RecordError(err)
		return nil, status.Errorf(codes.Internal, "failed to determine whether %s is block device: %v", req.GetVolumePath(), err)
	}
	if isBlock {
		bcap, err := ns.mounter.GetBlockSizeBytes(req.GetVolumePath())
		if err != nil {
			span.SetStatus(otcodes.Error, "failed to get block size")
			span.RecordError(err)
			return nil, status.Errorf(codes.Internal, "failed to get block capacity on path %s: %v", req.GetVolumePath(), err)
		}
		span.SetStatus(otcodes.Ok, "block device stats retrieved")
		return &csi.NodeGetVolumeStatsResponse{
			Usage: []*csi.VolumeUsage{
				{
					Unit:  csi.VolumeUsage_BYTES,
					Total: bcap,
				},
			},
		}, nil
	}

	volumeMetrics, err := volume.NewMetricsStatFS(req.GetVolumePath()).GetMetrics()
	if err != nil {
		span.SetStatus(otcodes.Error, "failed to get volume metrics")
		span.RecordError(err)
		return nil, err
	}

	available, ok := volumeMetrics.Available.AsInt64()
	if !ok {
		span.SetStatus(otcodes.Error, "failed to transform volume available size")
		return nil, status.Errorf(codes.Internal, "failed to transform volume available size(%v)", volumeMetrics.Available)
	}
	capacity, ok := volumeMetrics.Capacity.AsInt64()
	if !ok {
		span.SetStatus(otcodes.Error, "failed to transform volume capacity size")
		return nil, status.Errorf(codes.Internal, "failed to transform volume capacity size(%v)", volumeMetrics.Capacity)
	}
	used, ok := volumeMetrics.Used.AsInt64()
	if !ok {
		span.SetStatus(otcodes.Error, "failed to transform volume used size")
		return nil, status.Errorf(codes.Internal, "failed to transform volume used size(%v)", volumeMetrics.Used)
	}

	inodesFree, ok := volumeMetrics.InodesFree.AsInt64()
	if !ok {
		span.SetStatus(otcodes.Error, "failed to transform disk inodes free")
		return nil, status.Errorf(codes.Internal, "failed to transform disk inodes free(%v)", volumeMetrics.InodesFree)
	}
	inodes, ok := volumeMetrics.Inodes.AsInt64()
	if !ok {
		span.SetStatus(otcodes.Error, "failed to transform disk inodes")
		return nil, status.Errorf(codes.Internal, "failed to transform disk inodes(%v)", volumeMetrics.Inodes)
	}
	inodesUsed, ok := volumeMetrics.InodesUsed.AsInt64()
	if !ok {
		span.SetStatus(otcodes.Error, "failed to transform disk inodes used")
		return nil, status.Errorf(codes.Internal, "failed to transform disk inodes used(%v)", volumeMetrics.InodesUsed)
	}

	span.SetStatus(otcodes.Ok, "volume stats retrieved")
	return &csi.NodeGetVolumeStatsResponse{
		Usage: []*csi.VolumeUsage{
			{
				Unit:      csi.VolumeUsage_BYTES,
				Available: available,
				Total:     capacity,
				Used:      used,
			},
			{
				Unit:      csi.VolumeUsage_INODES,
				Available: inodesFree,
				Total:     inodes,
				Used:      inodesUsed,
			},
		},
	}, nil
}

func (ns *Server) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	ctx, span := ns.tracer.Start(ctx, "csi.v1.Node/NodeStageVolume", trace.WithAttributes(
		attribute.String("vol.id", req.GetVolumeId()),
	))
	defer span.End()

	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		span.SetStatus(otcodes.Error, "Volume ID missing in request")
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}

	stagingTargetPath := req.GetStagingTargetPath()
	if len(stagingTargetPath) == 0 {
		span.SetStatus(otcodes.Error, "Staging target path not provided")
		return nil, status.Error(codes.InvalidArgument, "Staging target path not provided")
	}

	volumeCapability := req.GetVolumeCapability()
	if volumeCapability == nil {
		span.SetStatus(otcodes.Error, "Volume capability missing in request")
		return nil, status.Error(codes.InvalidArgument, "Volume capability missing in request")
	}

	if err := capability.IsValidVolumeCapabilities([]*csi.VolumeCapability{volumeCapability}); err != nil {
		span.SetStatus(otcodes.Error, "Invalid volume capability")
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	log := log.FromContext(ctx).WithName("NodeStageVolume").WithValues("volumeID", volumeID)

	pvName, err := ns.volume.GetVolumeName(volumeID)
	if err != nil {
		span.SetStatus(otcodes.Error, "failed to get volume name")
		log.Error(err, "failed to get volume name")
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Get the pv and add selected-node annotation.
	pv := &corev1.PersistentVolume{}
	if err := ns.k8sClient.Get(ctx, client.ObjectKey{Name: pvName}, pv); err != nil {
		log.Error(err, "failed to get persistent volume")
		// Do not return error, as pv is only for recreating empty volumes.
		// If the volume is not found, the mount in the later stage will fail
		// and the call will be retired.
	}

	if pv.Spec.ClaimRef != nil {
		ctx = events.WithObjectIntoContext(ctx, ns.recorder, pv.Spec.ClaimRef)
	}

	// When removing node affinity from the PV, add the selected-node annotation to it.
	// This annotation helps track the node to which the volume is associated.
	if ns.removePvNodeAffinity {
		original := pv.DeepCopy()
		selectedNode, initialNode := "", ""
		updatePv := false // Can't have dependency on the PV for sanity tests, so we use a flag to track if we need to update the PV.

		// Mark current node as selected-node
		if pv.Annotations == nil {
			pv.Annotations = make(map[string]string)
		}

		// Check if the selected-node annotation is already set.
		selectedNode, ok := pv.Annotations[ns.selectedNodeAnnotation]
		if !ok {
			// If not, check if the initial node is set in the volume attributes.
			if pv.Spec.CSI != nil && pv.Spec.CSI.VolumeAttributes != nil {
				initialNode, ok = pv.Spec.CSI.VolumeAttributes[ns.selectedInitialNodeParam]
				// If the initial node is set, update the selected-node
				// annotation only if it is not the current node.
				if ok && !strings.EqualFold(initialNode, ns.nodeID) {
					pv.Annotations[ns.selectedNodeAnnotation] = ns.nodeID
					updatePv = true
				}
			}
		} else if !strings.EqualFold(selectedNode, ns.nodeID) {
			// If the selected-node annotation is set, but it is not the current
			// node, update it to the current node.
			pv.Annotations[ns.selectedNodeAnnotation] = ns.nodeID
			updatePv = true
		}

		// Patch the PV with the current node as the selected-node if needed.
		if updatePv {
			if err := ns.k8sClient.Patch(ctx, pv, client.MergeFrom(original)); err != nil {
				span.SetStatus(otcodes.Error, "failed to update pv with selected-node annotation")
				log.Error(err, "failed to update pv with selected-node annotation")
				return nil, status.Error(codes.Internal, err.Error())
			}
		}
	}

	if pv.Spec.CSI != nil && pv.Spec.CSI.VolumeAttributes != nil {
		// Call volume manager to ensure the volume exists. Re-creating empty
		// volumes is acceptable for local volumes.

		// Get the capacity request from the PV attributes.
		capacityBytes, limitBytes, err := getCapacityAndLimit(pv.Spec.CSI.VolumeAttributes)
		if err != nil {
			log.Error(err, "failed to get capacity and limit from pv attributes")
			span.SetStatus(otcodes.Error, "failed to get capacity and limit from pv attributes")
			return nil, status.Error(codes.Internal, err.Error())
		}

		if err := ns.volume.NodeEnsureVolume(ctx, req.GetVolumeId(), capacityBytes, limitBytes); err != nil {
			log.Error(err, "failed to publish volume")
			span.SetStatus(otcodes.Error, "failed to publish volume")
			span.RecordError(err)
			return nil, status.Error(codes.Internal, err.Error())
		}
	}

	if volumeCapability.GetBlock() != nil {
		span.SetStatus(otcodes.Ok, "block volume staged")
		return &csi.NodeStageVolumeResponse{}, nil
	}

	log.V(2).Info("creating staging directory if not present")
	if isMnt, err := ns.EnsureMount(stagingTargetPath); err != nil {
		span.SetStatus(otcodes.Error, "failed to ensure mount")
		span.RecordError(err)
		return nil, status.Error(codes.Internal, err.Error())
	} else if isMnt {
		log.V(2).Info("already mounted")
		span.SetStatus(otcodes.Ok, "already mounted")
		return &csi.NodeStageVolumeResponse{}, nil
	}

	mountVol := volumeCapability.GetMount()
	fsType := defaultFsType
	if mountVol != nil {
		fsType = mountVol.GetFsType()
	}
	mountOptions := volumeCapability.GetMount().GetMountFlags()
	log.V(2).Info("mounting")

	source, err := ns.volume.GetNodeDevicePath(volumeID)
	if err != nil {
		span.SetStatus(otcodes.Error, "failed to get node device path")
		return nil, err
	}

	formatOptions := []string{}
	if fsType == "xfs" {
		// Needed for compatibility with early kernels in azurelinux2.0 and ubuntu
		formatOptions = append(formatOptions, "-i", "nrext64=0")

	}

	if err := ns.mounter.FormatAndMountSensitiveWithFormatOptions(source, stagingTargetPath, fsType, mountOptions, nil, formatOptions); err != nil {
		log.Error(err, "failed to format and mount")
		span.SetStatus(otcodes.Error, "failed to format and mount")
		return nil, CheckMountError(err)
	}
	log.V(2).Info("mounted")
	span.SetStatus(otcodes.Ok, "mounted")
	return &csi.NodeStageVolumeResponse{}, nil
}

// getCapacityAndLimit retrieves the capacity and limit from the PV attributes.
func getCapacityAndLimit(attrs map[string]string) (capacityBytes, limitBytes int64, err error) {
	c, ok := attrs[lvm.CapacityParam]
	if !ok {
		return 0, 0, fmt.Errorf("volume request size is missing in pv attribute %s - recovery impossible", lvm.CapacityParam)
	}
	if len(c) > 0 {
		capacity, err := resource.ParseQuantity(attrs[lvm.CapacityParam])
		if err != nil {
			return 0, 0, fmt.Errorf("failed to parse volume request size from pv attribute %s=%s: %w", lvm.CapacityParam, attrs[lvm.CapacityParam], err)
		}
		capacityBytes = capacity.Value()
	}

	if l, ok := attrs[lvm.LimitParam]; ok && len(l) > 0 {
		limit, err := resource.ParseQuantity(l)
		if err != nil {
			return 0, 0, fmt.Errorf("failed to parse volume limit size from pv attribute %s=%s: %w", lvm.LimitParam, attrs[lvm.LimitParam], err)
		}
		limitBytes = limit.Value()
	}

	return capacityBytes, limitBytes, nil
}

func (ns *Server) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	ctx, span := ns.tracer.Start(ctx, "csi.v1.Node/NodeUnstageVolume", trace.WithAttributes(
		attribute.String("vol.id", req.GetVolumeId()),
	))
	defer span.End()

	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		span.SetStatus(otcodes.Error, "Volume ID missing in request")
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}

	stagingTargetPath := req.GetStagingTargetPath()
	if len(stagingTargetPath) == 0 {
		span.SetStatus(otcodes.Error, "Staging target path not provided")
		return nil, status.Error(codes.InvalidArgument, "Staging target path not provided")
	}

	log := log.FromContext(ctx).WithName("NodeUnstageVolume").WithValues("volumeID", volumeID)

	log.V(2).Info("NodeUnstageVolume deferring target path unmount to DeleteVolume")

	span.SetStatus(otcodes.Ok, "skipping unmounting staging target")
	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (ns *Server) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	ctx, span := ns.tracer.Start(ctx, "csi.v1.Node/NodeExpandVolume", trace.WithAttributes(
		attribute.String("vol.id", req.GetVolumeId()),
	))
	defer span.End()

	if len(req.GetVolumeId()) == 0 {
		span.SetStatus(otcodes.Error, "Volume ID missing in request")
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in the request")
	}

	if len(req.GetVolumePath()) == 0 {
		span.SetStatus(otcodes.Error, "Volume path is required")
		return nil, status.Error(codes.InvalidArgument, "volume path is required")
	}

	if _, err := os.Stat(req.GetVolumePath()); err != nil {
		if os.IsNotExist(err) {
			span.SetStatus(otcodes.Error, "volume path does not exist")
			return nil, status.Error(codes.NotFound, "volume path does not exist")
		}
		span.SetStatus(otcodes.Error, "failed to stat volume path")
		return nil, status.Errorf(codes.Internal, "failed to stat volume path %s: %v", req.GetVolumePath(), err)
	}

	log := log.FromContext(ctx).WithName("NodeExpandVolume").WithValues("volumeID", req.GetVolumeId(), "capacityBytes", req.GetCapacityRange().GetRequiredBytes())

	pvName, err := ns.volume.GetVolumeName(req.GetVolumeId())
	if err != nil {
		log.Error(err, "failed to get volume name")
		span.SetStatus(otcodes.Error, "failed to get volume name")
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	var pv corev1.PersistentVolume
	if err := ns.k8sClient.Get(ctx, client.ObjectKey{Name: pvName}, &pv); err != nil {
		log.Error(err, "failed to get persistent volume for events")
		// Do not return an error, it best effort to get the pv for events.
	}

	if pv.Spec.ClaimRef != nil {
		ctx = events.WithObjectIntoContext(ctx, ns.recorder, pv.Spec.ClaimRef)
	}

	resp, err := ns.volume.NodeExpandVolume(ctx, req)
	if err != nil {
		log.Error(err, "failed to expand volume")
		span.SetStatus(otcodes.Error, "failed to expand volume")
		span.RecordError(err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	log.V(2).Info("NodeExpandVolume resized volume")
	span.SetStatus(otcodes.Ok, "resized volume")
	return resp, nil
}

// topologyKey returns the key for the topology map.
func (ns *Server) topologyKey() string {
	return fmt.Sprintf("topology.%s/node", ns.driver)
}

// topologyValue returns the value for the topology map.
func (ns *Server) topologyValue() string {
	return ns.nodeID
}

func (ns *Server) EnsureTargetFile(targetPath string) error {
	exists, err := ns.mounter.FileExists(targetPath)
	if err != nil {
		return err
	}
	if !exists {
		return ns.mounter.MakeFile(targetPath)
	}
	return nil
}

func (ns *Server) EnsureMount(path string) (bool, error) {
	notMnt, err := ns.mounter.IsLikelyNotMountPoint(path)
	if err != nil {
		if os.IsNotExist(err) {
			if err := ns.mounter.MakeDir(path); err != nil {
				return false, err
			}
			return false, nil
		} else {
			return false, err
		}
	}
	return !notMnt, nil
}

// getThrottlingParamsFromVAC fetches the latest throttling parameters from VolumeAttributesClass
// This ensures we always have current values and eliminates parameter duplication
func (ns *Server) getThrottlingParamsFromVAC(ctx context.Context, volumeId string) *IOThrottleParams {
	log := log.FromContext(ctx)

	// Extract PV name from volume ID
	// Assuming volumeId format is "containerstorage#pv-name"
	parts := strings.Split(volumeId, "#")
	if len(parts) != 2 {
		log.V(3).Info("Unable to parse volume ID for PV lookup", "volumeId", volumeId)
		return nil
	}
	pvName := parts[1]

	// Get PV object from Kubernetes API
	pv := &corev1.PersistentVolume{}
	if err := ns.k8sClient.Get(ctx, client.ObjectKey{Name: pvName}, pv); err != nil {
		log.V(3).Info("Failed to get PV for VolumeAttributesClass lookup", "pvName", pvName, "error", err)
		return nil
	}

	// Check if PV references a VolumeAttributesClass
	if pv.Spec.VolumeAttributesClassName == nil || *pv.Spec.VolumeAttributesClassName == "" {
		log.V(3).Info("PV has no VolumeAttributesClass reference", "pvName", pvName)
		return nil
	}

	vacName := *pv.Spec.VolumeAttributesClassName
	log.V(2).Info("Found VolumeAttributesClass reference", "pvName", pvName, "vacName", vacName)

	// Get throttling parameters directly from VAC
	throttleParams, clearParams, err := controller.GetThrottlingParamsFromVAC(ctx, ns.k8sClient, vacName)
	if err != nil {
		log.V(2).Info("Failed to get throttling parameters from VAC", "vacName", vacName, "error", err)
		return nil
	}

	// If clear throttling is requested, return nil to indicate no throttling should be applied
	if clearParams != nil && clearParams.ShouldClear {
		log.V(2).Info("Clear throttling requested via VAC", "vacName", vacName)
		return nil
	}

	// Convert controller.IOThrottleParams to node.IOThrottleParams
	if throttleParams == nil {
		return nil
	}

	nodeThrottleParams := &IOThrottleParams{
		RBPS:  throttleParams.RBPS,
		WBPS:  throttleParams.WBPS,
		RIOPS: throttleParams.RIOPS,
		WIOPS: throttleParams.WIOPS,
	}

	return nodeThrottleParams
}

// applyPodIOThrottling applies IO throttling configuration to the pod's cgroup
// based on throttling parameters from volume context (fast) or VolumeAttributesClass (fallback with API call)
func (ns *Server) applyPodIOThrottling(ctx context.Context, volumeContext map[string]string, targetPath string, volumeId string) error {
	log := log.FromContext(ctx)

	// Priority 1: Check VolumeAttributesClass for throttling parameters (API call required)
	throttleParams := ns.getThrottlingParamsFromVAC(ctx, volumeId)
	if throttleParams != nil {
		log.V(2).Info("Found throttling parameters in VolumeAttributesClass", "throttleParams", throttleParams)
	} else {
		// Priority 2: Check volume context for throttling parameters (fast)
		throttleParams = extractThrottlingParamsFromVolumeContext(volumeContext)
		if throttleParams != nil {
			log.V(2).Info("Found throttling parameters in volume context", "throttleParams", throttleParams)
		}
	}

	if throttleParams == nil {
		log.V(3).Info("No throttling parameters found in volume context or VolumeAttributesClass")
		return nil
	}

	// Get the device path for the volume
	// We need to find the underlying block device for the mounted volume
	devicePath, err := ns.findDeviceForMountPath(targetPath)
	if err != nil {
		return fmt.Errorf("failed to find device for mount path %s: %w", targetPath, err)
	}

	log.V(3).Info("Found device for mount", "targetPath", targetPath, "devicePath", devicePath)

	// Find the pod's cgroup path from the target path
	podCgroupPath, err := ns.findPodCgroupFromTargetPath(targetPath)
	if err != nil {
		return fmt.Errorf("failed to find pod cgroup for target path %s: %w", targetPath, err)
	}

	log.V(3).Info("Found pod cgroup path", "targetPath", targetPath, "cgroupPath", podCgroupPath)

	// Apply throttling configuration to the pod's cgroup
	if err := ns.configurePodCgroupIOMax(devicePath, throttleParams, podCgroupPath); err != nil {
		return fmt.Errorf("failed to configure pod cgroup IO throttling: %w", err)
	}

	log.Info("Successfully applied IO throttling to pod",
		"targetPath", targetPath,
		"devicePath", devicePath,
		"cgroupPath", podCgroupPath,
		"rbps", throttleParams.RBPS,
		"wbps", throttleParams.WBPS,
		"riops", throttleParams.RIOPS,
		"wiops", throttleParams.WIOPS,
	)

	return nil
}

func CheckMountError(err error) error {
	if strings.Contains(err.Error(), "permission denied") {
		return status.Error(codes.PermissionDenied, err.Error())
	}
	if strings.Contains(err.Error(), "invalid argument") {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	return status.Error(codes.Internal, err.Error())
}
