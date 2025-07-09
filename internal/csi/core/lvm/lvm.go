// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/go-logr/logr"
	"github.com/gotidy/ptr"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"local-csi-driver/internal/csi/core"
	"local-csi-driver/internal/pkg/events"
	"local-csi-driver/internal/pkg/lvm"
	"local-csi-driver/internal/pkg/probe"
)

const (
	// DriverName as registered with Kubernetes.
	DriverName = "localdisk.csi.acstor.io"

	// DefaultVolumeGroup is the default volume group name used if no
	// VolumeGroupNameParam is specified in the volume create request.
	DefaultVolumeGroup = "containerstorage"

	// DefaultVolumeGroupTag is the default volume group tag used for all
	// volume groups created by the driver.
	DefaultVolumeGroupTag = "local-csi"

	// TopologyKey is the expected key used in the volume request to specify the
	// node where the volume should be placed.
	TopologyKey = "topology.localdisk.csi.acstor.io/node"

	// Raid0LvType is the logical volume type for raid0.
	raid0LvType = "raid0"

	// ID is the unique identifier of the CSI driver, used internally.
	ID = "lvm"

	// Physical Volume (PV) related event reasons.
	provisioningPhysicalVolume       = "ProvisioningPhysicalVolume"
	provisionedPhysicalVolume        = "ProvisionedPhysicalVolume"
	provisioningPhysicalVolumeFailed = "ProvisioningPhysicalVolumeFailed"

	// Volume Group (VG) related event reasons.
	provisioningVolumeGroup       = "ProvisioningVolumeGroup"
	provisionedVolumeGroup        = "ProvisionedVolumeGroup"
	provisioningVolumeGroupFailed = "ProvisioningVolumeGroupFailed"

	// Logical Volume (LV) related event reasons.
	provisioningLogicalVolume            = "ProvisioningLogicalVolume"
	provisionedLogicalVolume             = "ProvisionedLogicalVolume"
	provisioningLogicalVolumeFailed      = "ProvisioningLogicalVolumeFailed"
	provisionedLogicalVolumeSizeMismatch = "ProvisionedLogicalVolumeSizeMismatch"
)

// Volume context parameters.
var (
	// VolumeGroupParam is the expected key used in the volume create request to
	// specify the LVM volume group name where the logical volume should be
	// created.
	VolumeGroupParam = DriverName + "/vg"

	// CapacityParam and LimitParam are used in the volume context to specify
	// the requested and maximum size of the logical volume. It is used for PV
	// recovery during NodeStageVolume to recreate the volume if it doesn't
	// exist.
	CapacityParam = DriverName + "/capacity"
	LimitParam    = DriverName + "/limit"
)

var (
	// ErrNoDisksFound is returned when no disks are found during volume
	// provisioning.
	ErrNoDisksFound = fmt.Errorf("no disks found")

	// removeVolumeRetryPoll is the time to wait between retries when removing
	// a volume.
	removeVolumeRetryPoll = 500 * time.Millisecond

	// removeVolumeRetryTimeout is the time to wait before giving up on
	// removing a volume.
	removeVolumeRetryTimeout = 10 * time.Second
)

// csiDriver is the CSI driver object that is registered with the Kubernetes API
// server.
var csiDriver = &storagev1.CSIDriver{
	ObjectMeta: metav1.ObjectMeta{
		Name: DriverName,
	},
	Spec: storagev1.CSIDriverSpec{
		AttachRequired:  ptr.Of(false),
		PodInfoOnMount:  ptr.Of(true),
		StorageCapacity: ptr.Of(true),
		FSGroupPolicy:   ptr.Of(storagev1.FileFSGroupPolicy),
		VolumeLifecycleModes: []storagev1.VolumeLifecycleMode{
			storagev1.VolumeLifecyclePersistent,
			storagev1.VolumeLifecycleEphemeral,
		},
	},
}

var (
	controllerCapabilities = []csi.ControllerServiceCapability_RPC_Type{
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		csi.ControllerServiceCapability_RPC_GET_CAPACITY,
		// csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
		// csi.ControllerServiceCapability_RPC_LIST_VOLUMES,
		// csi.ControllerServiceCapability_RPC_EXPAND_VOLUME,
		// csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT,
		// csi.ControllerServiceCapability_RPC_LIST_SNAPSHOTS,
		// csi.ControllerServiceCapability_RPC_CLONE_VOLUME,
		// csi.ControllerServiceCapability_RPC_PUBLISH_READONLY,
		// csi.ControllerServiceCapability_RPC_LIST_VOLUMES_PUBLISHED_NODES,
		// csi.ControllerServiceCapability_RPC_VOLUME_CONDITION,
		// csi.ControllerServiceCapability_RPC_GET_VOLUME,
		// csi.ControllerServiceCapability_RPC_SINGLE_NODE_MULTI_WRITER,
		// csi.ControllerServiceCapability_RPC_MODIFY_VOLUME,
	}

	nodeCapabilities = []csi.NodeServiceCapability_RPC_Type{
		csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
		csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
		csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
		// csi.NodeServiceCapability_RPC_VOLUME_CONDITION,
		// csi.NodeServiceCapability_RPC_SINGLE_NODE_MULTI_WRITER,
		// csi.NodeServiceCapability_RPC_VOLUME_MOUNT_GROUP,
	}

	// Access modes supported by the driver.
	accessModes = []*csi.VolumeCapability_AccessMode{
		{
			Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		},
		{
			Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY,
		},
		{
			Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER,
		},
	}
)

type LVM struct {
	client.Client
	releaseNamespace string
	podName          string
	nodeName         string
	probe            probe.Interface
	lvm              lvm.Manager
	tracer           trace.Tracer
}

// New creates a new LVM volume manager.
func New(k8sclient client.Client, podName, nodeName, releaseNamespace string, probe probe.Interface, lvmMgr lvm.Manager, tp trace.TracerProvider) (*LVM, error) {
	if podName == "" {
		return nil, fmt.Errorf("podName must not be empty")
	}
	if nodeName == "" {
		return nil, fmt.Errorf("nodeName must not be empty")
	}
	if releaseNamespace == "" {
		return nil, fmt.Errorf("releaseNamespace must not be empty")
	}
	return &LVM{
		Client:           k8sclient,
		podName:          podName,
		nodeName:         nodeName,
		releaseNamespace: releaseNamespace,
		probe:            probe,
		lvm:              lvmMgr,
		tracer:           tp.Tracer("localdisk.csi.acstor.io/internal/csi/api/volume/lvm"),
	}, nil
}

// Start starts the LVM volume manager.
func (l *LVM) Start(ctx context.Context) error {
	ctxMain, cancelMain := context.WithCancel(context.Background())
	log := log.FromContext(ctxMain).WithValues("lvm", "start")

	<-ctx.Done()
	defer cancelMain()

	log.Info("pod shutdown signal received, checking if cleanup is required")
	cleanup, err := isCleanupRequired(ctxMain, l.Client, l.podName, l.releaseNamespace, log)
	if err != nil {
		log.Error(err, "failed to check if cleanup is required")
		return err
	}
	if cleanup {
		log.Info("running cleanup of lvm resources")
		if err := l.Cleanup(ctxMain); err != nil {
			log.Error(err, "failed to cleanup lvm resources")
			return err
		}
	}
	log.Info("cleanup of lvm resources completed")
	return nil
}

// NeedLeaderElection returns false since LVM should be running on each node.
func (l *LVM) NeedLeaderElection() bool {
	return false
}

// GetCSIDriver returns the CSI driver object for the volume manager.
func (l *LVM) GetCSIDriver() *storagev1.CSIDriver {
	return csiDriver
}

// GetDriverName returns the name of the driver.
func (l *LVM) GetDriverName() string {
	return DriverName
}

// GetVolumeName returns the logical volume name from the volume id. The volume
// id is expected to be in the format <volume-group>#<logical-volume>.
func (l *LVM) GetVolumeName(volumeId string) (string, error) {
	id, err := newIdFromString(volumeId)
	if err != nil {
		return "", fmt.Errorf("failed to parse volume id: %w", err)
	}
	return id.LogicalVolume, nil
}

// EnsureVolumeGroup ensures that the volume group exists with the given name
// and devices.
//
// If the volume group already exists or is created, it returns it. Otherwise it
// returns an error.
func (l *LVM) EnsureVolumeGroup(ctx context.Context, name string, devices []string) (*lvm.VolumeGroup, error) {
	log := log.FromContext(ctx).WithValues("vg", name)
	recorder := events.FromContext(ctx)
	ctx, span := l.tracer.Start(ctx, "volume.lvm.csi/EnsureVolumeGroup", trace.WithAttributes(
		attribute.String("vol.group", name),
	))
	defer span.End()
	if len(name) == 0 {
		log.Error(fmt.Errorf("volume group name is required"), "vgName", name)
		span.SetStatus(codes.Error, "volume group name is required")
		return nil, fmt.Errorf("%w: volume group name is required", core.ErrInvalidArgument)
	}
	if len(devices) == 0 {
		log.Error(ErrNoDisksFound, "no disks found")
		span.SetStatus(codes.Error, "no disks found")
		return nil, fmt.Errorf("%w: no disks found", core.ErrResourceExhausted)
	}

	// Check if the volume group already exists.
	vg, err := l.lvm.GetVolumeGroup(ctx, name)
	if lvm.IgnoreNotFound(err) != nil {
		span.SetStatus(codes.Error, "failed to get volume group")
		span.RecordError(err)
		return nil, fmt.Errorf("failed to get volume group: %w", err)
	}
	if vg != nil {
		log.V(1).Info("volume group already exists")
		return vg, nil
	}

	recorder.Eventf(corev1.EventTypeNormal, provisioningVolumeGroup, "Provisioning volume group %s", name)
	// Create the volume group.
	if err := l.lvm.CreateVolumeGroup(ctx, lvm.CreateVGOptions{
		Name:    name,
		PVNames: devices,
		Tags:    []string{DefaultVolumeGroupTag},
	}); err != nil {
		// Multiple workers may try to create the same volume group at the same time,
		// so ignore the already exists error and return the existing volume group.
		if !errors.Is(err, lvm.ErrAlreadyExists) {
			// If one of the PVs is already in a volume group, we return resource exhausted
			// error. This will help schedule the volume on a different node where PVs may
			// be available.
			if errors.Is(err, lvm.ErrPVAlreadyInVolumeGroup) {
				span.SetStatus(codes.Error, "pv already in volume group")
				span.RecordError(err)
				recorder.Eventf(corev1.EventTypeWarning, provisioningVolumeGroupFailed, "Provisioning volume group %s failed: %s", name, err)
				return nil, fmt.Errorf("%w: failed to create volume group: %v", core.ErrResourceExhausted, err)
			}
			// If the error is not already exists, return it.
			log.Error(err, "failed to create volume group")
			span.SetStatus(codes.Error, "failed to create volume group")
			span.RecordError(err)
			recorder.Eventf(corev1.EventTypeWarning, provisioningVolumeGroupFailed, "Provisioning volume group %s failed: %s", name, err)
			return nil, fmt.Errorf("failed to create volume group: %w", err)
		}
	}
	recorder.Eventf(corev1.EventTypeNormal, provisionedVolumeGroup, "Successfully provisioned volume group %s", name)
	log.V(1).Info("created volume group")

	// Get the newly created volume group.
	vg, err = l.lvm.GetVolumeGroup(ctx, name)
	if err != nil {
		span.SetStatus(codes.Error, "failed to get volume group after creation")
		span.RecordError(err)
		return nil, fmt.Errorf("failed to get volume group after creation: %w", err)
	}
	return vg, nil
}

// EnsurePhysicalVolumes ensures that the physical volumes are created for the
// devices matching the filter.
//
// If all physical volumes exist or were created successfully, it returns them
// as a list of device paths, otherwise it returns an error and an empty list.
func (l *LVM) EnsurePhysicalVolumes(ctx context.Context) ([]string, error) {
	log := log.FromContext(ctx)
	recorder := events.FromContext(ctx)
	ctx, span := l.tracer.Start(ctx, "volume.lvm.csi/EnsurePhysicalVolumes")
	defer span.End()

	// Get list of physical disks matching the device filter.
	devices, err := l.probe.ScanAvailableDevices(ctx, log)
	if err != nil {
		if errors.Is(err, probe.ErrNoDevicesFound) {
			log.Error(err, "no devices found")
			span.SetStatus(codes.Error, "no devices found")
			recorder.Eventf(corev1.EventTypeWarning, provisioningPhysicalVolumeFailed, "Provisioning physical volume failed: %s", err.Error())
			return nil, fmt.Errorf("%w: scan devices found: %v", core.ErrResourceExhausted, err)
		}
		span.SetStatus(codes.Error, "failed to scan devices")
		span.RecordError(err)
		return nil, fmt.Errorf("failed to scan devices: %w", err)
	}
	log.V(2).Info("found devices", "devices", devices)

	// Get list of existing physical volumes.
	existing := map[string]struct{}{}
	finalPvs := make([]string, 0, len(devices.Devices))
	pvs, err := l.lvm.ListPhysicalVolumes(ctx, nil)
	if err != nil {
		span.SetStatus(codes.Error, "failed to list physical volumes")
		span.RecordError(err)
		return nil, fmt.Errorf("failed to list physical volumes: %w", err)
	}
	for _, pv := range pvs {
		existing[pv.Name] = struct{}{}
	}

	// Create any missing physical volumes.
	for _, device := range devices.Devices {
		if _, ok := existing[device.Path]; ok {
			finalPvs = append(finalPvs, device.Path)
			log.V(2).Info("physical volume already exists", "device", device)
			continue
		}
		recorder.Eventf(corev1.EventTypeNormal, provisioningPhysicalVolume, "Provisioning physical volume %s", device)
		pvCreateOptions := lvm.CreatePVOptions{
			Name: device.Path,
		}
		if err := l.lvm.CreatePhysicalVolume(ctx, pvCreateOptions); err != nil {
			span.SetStatus(codes.Error, "failed to create physical volume")
			span.RecordError(err)
			recorder.Eventf(corev1.EventTypeWarning, provisioningPhysicalVolumeFailed, "Provisioning physical volume %s failed: %s", device, err)
			if !errors.Is(err, lvm.ErrInUse) {
				return nil, fmt.Errorf("failed to create physical volume %s: %w", device.Path, err)
			}
			continue
		}
		finalPvs = append(finalPvs, device.Path)
		recorder.Eventf(corev1.EventTypeNormal, provisionedPhysicalVolume, "Successfully provisioned physical volume %s", device)
		log.V(1).Info("created physical volume", "device", device)
	}

	if len(finalPvs) == 0 {
		log.Error(fmt.Errorf("no disks are available"), "no disks are available")
		// Return as resource exhausted to prompt scheduler to move the workload to another node
		return nil, fmt.Errorf("%w: no disks are available", core.ErrResourceExhausted)
	}

	return finalPvs, nil
}

// EnsureVolume ensures that the volume exists with the given name and size.
// If the volume already exists, it returns it. Otherwise it creates the
// volume and returns it.
func (l *LVM) EnsureVolume(ctx context.Context, volumeId string, capacity int64, limit int64) error {
	ctx, span := l.tracer.Start(ctx, "volume.lvm.csi/EnsureVolume", trace.WithAttributes(
		attribute.String("vol.id", volumeId),
		attribute.Int64("vol.capacity", capacity),
		attribute.Int64("vol.limit", limit),
	))
	defer span.End()

	log := log.FromContext(ctx).WithValues("volumeId", volumeId)
	recorder := events.FromContext(ctx)
	log.V(1).Info("creating volume")

	id, err := newIdFromString(volumeId)
	if err != nil {
		log.Error(err, "failed to parse volume id", "volumeId", volumeId)
		span.SetStatus(codes.Error, "failed to parse volume id")
		return fmt.Errorf("%w: failed to parse volume id %s: %w", core.ErrInvalidArgument, volumeId, err)
	}

	log = log.WithValues("vg", id.VolumeGroup, "lv", id.LogicalVolume)

	if id.VolumeGroup == "" {
		log.Error(fmt.Errorf("volume group name is required"), "vg", id.VolumeGroup)
		span.SetStatus(codes.Error, "volume group name is required")
		return fmt.Errorf("%w: volume group name is required", core.ErrInvalidArgument)
	}
	if capacity <= 0 || limit < 0 || (limit > 0 && limit < capacity) {
		log.Error(fmt.Errorf("invalid capacity or limit"), "capacity", capacity, "limit", limit)
		span.SetStatus(codes.Error, "invalid capacity or limit")
		return fmt.Errorf("%w: invalid capacity %d or limit %d", core.ErrInvalidArgument, capacity, limit)
	}

	// Check for existing volume on the node.
	lv, err := l.lvm.GetLogicalVolume(ctx, id.VolumeGroup, id.LogicalVolume)
	if lvm.IgnoreNotFound(err) != nil {
		log.Error(err, "failed to check if volume exists")
		return err
	}
	if err == nil && lv != nil {
		log.V(2).Info("found existing volume", "bytes", lv.Size)
		span.AddEvent("found existing volume", trace.WithAttributes(attribute.Int64("bytes", int64(lv.Size))))

		// Check volume size.
		if int64(lv.Size) < capacity || (limit > 0 && int64(lv.Size) > limit) {
			log.Error(err, "volume size mismatch", "request", capacity, "limit", limit, "actual", lv.Size)
			span.SetStatus(codes.Error, "volume size mismatch")
			recorder.Eventf(corev1.EventTypeWarning, provisionedLogicalVolumeSizeMismatch, "Volume size mismatch %s/%s: request %d, limit %d, actual %d", id.VolumeGroup, id.LogicalVolume, capacity, limit, lv.Size)
			return fmt.Errorf("volume size mismatch: request %d, limit %d, actual %d: %w", capacity, limit, lv.Size, core.ErrVolumeSizeMismatch)
		}
		return nil
	}

	recorder.Eventf(corev1.EventTypeNormal, provisioningLogicalVolume, "Provisioning logical volume %s/%s", id.VolumeGroup, id.LogicalVolume)
	log.V(2).Info("no existing volume found, creating new one")
	// Check if the volume group already exists and create if needed.
	vg, err := l.lvm.GetVolumeGroup(ctx, id.VolumeGroup)
	if lvm.IgnoreNotFound(err) != nil {
		log.Error(err, "failed to check if volume group exists", "vg", id.VolumeGroup)
		span.SetStatus(codes.Error, "failed to check if volume group exists")
		span.RecordError(err)
		recorder.Eventf(corev1.EventTypeWarning, provisioningLogicalVolumeFailed, "Failed to check if volume group exists for %s/%s: %s", id.VolumeGroup, id.LogicalVolume, err.Error())
		return fmt.Errorf("failed to check if volume group exists: %w", err)
	}
	if vg == nil {
		log.V(2).Info("no existing volume group found, creating new one")
		devices, err := l.EnsurePhysicalVolumes(ctx)
		if err != nil {
			log.Error(err, "failed to ensure physical volumes")
			span.SetStatus(codes.Error, "failed to ensure physical volumes")
			span.RecordError(err)
			recorder.Eventf(corev1.EventTypeWarning, provisioningLogicalVolumeFailed, "Failed to ensure physical volumes created for %s/%s: %s", id.VolumeGroup, id.LogicalVolume, err.Error())
			return err
		}
		if vg, err = l.EnsureVolumeGroup(ctx, id.VolumeGroup, devices); err != nil {
			log.Error(err, "failed to ensure volume group")
			span.SetStatus(codes.Error, "failed to ensure volume group")
			span.RecordError(err)
			recorder.Eventf(corev1.EventTypeWarning, provisioningLogicalVolumeFailed, "Failed to ensure volume group created for %s/%s: %s", id.VolumeGroup, id.LogicalVolume, err.Error())
			return err
		}
	}
	// Provision the logical volume on the node.
	createOps := lvm.CreateLVOptions{
		Name:   id.LogicalVolume,
		VGName: vg.Name,
		Size:   fmt.Sprintf("%dB", capacity),
	}

	// if we have more than one PV, create a raid0 volume. Otherwise, create a
	// linear volume. We can't create a raid0 volume with only one PV.
	if vg.PVCount > 1 {
		createOps.Type = raid0LvType
		createOps.Stripes = ptr.Of(int(vg.PVCount))
	}

	if err := l.lvm.CreateLogicalVolume(ctx, createOps); err != nil {
		log.Error(err, "failed to create logical volume", "capacity", capacity)
		span.SetStatus(codes.Error, "failed to create logical volume")
		span.RecordError(err)
		recorder.Eventf(corev1.EventTypeWarning, provisioningLogicalVolumeFailed, "Provisioning logical volume %s/%s failed: %s", id.VolumeGroup, id.LogicalVolume, err)
		if errors.Is(err, lvm.ErrResourceExhausted) {
			return fmt.Errorf("failed to create logical volume: %w", core.ErrResourceExhausted)
		}
		return fmt.Errorf("failed to create logical volume: %w", err)
	}
	log.V(1).Info("created logical volume", "capacity", capacity)
	span.AddEvent("created logical volume", trace.WithAttributes(attribute.Int64("capacity", capacity)))
	recorder.Eventf(corev1.EventTypeNormal, provisionedLogicalVolume, "Successfully provisioned logical volume %s/%s", id.VolumeGroup, id.LogicalVolume)
	return nil
}

// GetNodeDevicePath returns the device path for the given volume ID.
//
// The CSI volume context contains the volume group but it's not passed to all
// CSI operations. Instead, since the volumeId (lv name) is unique across
// vgs, we can look it up from the lv.
func (l *LVM) GetNodeDevicePath(volumeId string) (string, error) {
	id, err := newIdFromString(volumeId)
	if err != nil {
		return "", fmt.Errorf("failed to parse volume id: %w", err)
	}
	return id.ReconstructLogicalVolumePath(), nil
}

// Cleanup cleans up the LVM resources on the node.
func (l *LVM) Cleanup(ctx context.Context) error {
	ctx, span := l.tracer.Start(ctx, "volume.lvm.csi/Cleanup")
	defer span.End()

	log := log.FromContext(ctx)
	log.V(1).Info("validating volume")
	volumeGroup, err := l.lvm.ListVolumeGroups(ctx, &lvm.ListVGOptions{Select: "vg_tags=" + DefaultVolumeGroupTag})
	if err != nil {
		log.Error(err, "failed to list volume groups")
		return fmt.Errorf("failed to list volume groups: %w", err)
	}

	for _, vg := range volumeGroup {
		if err := l.removeVolumeGroup(ctx, vg.Name); err != nil {
			log.Error(err, "failed to remove volume group", "vg", DefaultVolumeGroup)
			return err
		}
	}

	pvs, err := l.lvm.ListPhysicalVolumes(ctx, nil)
	if err != nil {
		log.Error(err, "failed to list physical volumes")
		return fmt.Errorf("failed to list physical volumes: %w", err)
	}

	devices := make([]string, 0, len(pvs))
	for _, pv := range pvs {
		devices = append(devices, pv.Name)
	}

	if err := l.removePhysicalVolumes(ctx, devices); err != nil {
		log.Error(err, "failed to remove physical volumes", "devices", devices)
		return err
	}
	log.V(1).Info("cleanup completed")
	return nil
}

// removeVolumeGroup removes the VG from the node.
func (l *LVM) removeVolumeGroup(ctx context.Context, vgName string) error {
	// Check if the VG exists, if there is no VG, we get NotFound error.
	if _, err := l.lvm.GetVolumeGroup(ctx, vgName); err != nil {
		if errors.Is(err, lvm.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("failed to get VG: %w", err)
	}

	vgRemoveOptions := lvm.RemoveVGOptions{
		Name: vgName,
	}

	// VG exists, remove it
	if err := l.lvm.RemoveVolumeGroup(ctx, vgRemoveOptions); err != nil {
		return fmt.Errorf("failed to remove VG: %w", err)
	}
	return nil
}

// removePhysicalVolumes removes the PVs from the node.
func (l *LVM) removePhysicalVolumes(ctx context.Context, devicePaths []string) error {
	pvs := map[string]struct{}{}
	// lists all the available PVs
	listResult, err := l.lvm.ListPhysicalVolumes(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to list physical volumes: %w", err)
	}
	for _, pv := range listResult {
		pvs[pv.Name] = struct{}{}
	}

	for _, device := range devicePaths {
		if _, ok := pvs[device]; !ok {
			continue
		}
		pvRemoveOptions := lvm.RemovePVOptions{
			Name: device,
		}
		if err := l.lvm.RemovePhysicalVolume(ctx, pvRemoveOptions); err != nil {
			return fmt.Errorf("failed to remove physical volumes: %w", err)
		}
	}
	return nil
}

// isCleanupRequired checks if the cleanup of lvm resources is required during
// the shutdown of the controller. Cleanup is only required if the daemonset for
// the deployment is being deleted. Othrewise, cleanup is skipped because the
// shutdown is likely due to a pod failover, eviction, restart or other transient issue.
func isCleanupRequired(ctx context.Context, c client.Client, podName, namespace string, log logr.Logger) (bool, error) {
	log.Info("getting daemonset name from pod name", "podName", podName)
	daemonsetName, err := getDaemonSetNameFromPodName(ctx, c, podName, namespace)
	if err != nil {
		log.Error(err, "pod is not part of a daemonset")
		return false, err
	}

	// get the daemonset and check the deletion timestamp
	daemonset := &appsv1.DaemonSet{}
	err = c.Get(ctx, client.ObjectKey{
		Name:      daemonsetName,
		Namespace: namespace,
	}, daemonset)

	if client.IgnoreNotFound(err) != nil {
		log.Error(err, "failed to get daemonset")
		return false, err
	}
	if err != nil || !daemonset.DeletionTimestamp.IsZero() {
		log.Info("daemonset is being deleted, cleanup required")
		return true, nil
	}
	log.Info("daemonset is not being deleted, cleanup not required")
	return false, nil
}

// getDaemonSetNameFromPodName retrieves the name of the Kubernetes DaemonSet
// based on the given pod name.
func getDaemonSetNameFromPodName(ctx context.Context, c client.Client, podName, namespace string) (string, error) {
	pod := &corev1.Pod{}
	err := c.Get(ctx, client.ObjectKey{
		Name:      podName,
		Namespace: namespace,
	}, pod)
	if err != nil {
		return "", err
	}

	ownerReferences := pod.GetOwnerReferences()
	for _, owner := range ownerReferences {
		if owner.Kind == "DaemonSet" {
			return owner.Name, nil
		}
	}

	return "", fmt.Errorf("no DaemonSet found for pod: %s", podName)
}
