// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/gotidy/ptr"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"local-csi-driver/internal/csi/core"
	"local-csi-driver/internal/pkg/events"
	"local-csi-driver/internal/pkg/lvm"
	"local-csi-driver/internal/pkg/probe"
)

const (
	// DriverName as registered with Kubernetes.
	DriverName = "localdisk.csi.acstor.io"

	// VolumeGroupNameParam is the StorageClass parameter key for specifying
	// a custom volume group name. If not specified, DefaultVolumeGroup is used.
	VolumeGroupNameParam = "volumeGroup"

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
	provisionedEmptyVolume               = "ProvisionedEmptyVolume"
	removedCorruptedLogicalVolume        = "RemovedCorruptedLogicalVolume"
	failedToRemoveCorruptedLogicalVolume = "FailedToRemoveCorruptedLogicalVolume"
)

// Volume context parameters.
var (
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
	releaseNamespace string
	podName          string
	nodeName         string
	enableCleanup    bool
	probe            probe.Interface
	lvm              lvm.Manager
	tracer           trace.Tracer
}

// New creates a new LVM volume manager.
func New(podName, nodeName, releaseNamespace string, enableCleanup bool, probe probe.Interface, lvmMgr lvm.Manager, tp trace.TracerProvider) (*LVM, error) {
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
		podName:          podName,
		nodeName:         nodeName,
		releaseNamespace: releaseNamespace,
		enableCleanup:    enableCleanup,
		probe:            probe,
		lvm:              lvmMgr,
		tracer:           tp.Tracer("localdisk.csi.acstor.io/internal/csi/api/volume/lvm"),
	}, nil
}

// Start starts the LVM volume manager.
func (l *LVM) Start(ctx context.Context) error {
	ctxMain, cancelMain := context.WithCancel(context.Background())
	defer cancelMain()
	log := log.FromContext(ctxMain).WithValues("lvm", "start")

	<-ctx.Done()

	if !l.enableCleanup {
		log.Info("cleanup of lvm resources is disabled, skipping")
		return nil
	}

	log.Info("running cleanup of lvm resources")
	if err := l.Cleanup(ctxMain); err != nil {
		log.Error(err, "failed to cleanup lvm resources")
		return err
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
func (l *LVM) EnsurePhysicalVolumes(ctx context.Context, vgName string) ([]string, error) {
	log := log.FromContext(ctx)
	recorder := events.FromContext(ctx)
	ctx, span := l.tracer.Start(ctx, "volume.lvm.csi/EnsurePhysicalVolumes")
	defer span.End()

	// Get list of physical disks matching the device filter.
	devices, err := l.probe.ScanAvailableDevices(ctx)
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

	pvs := make([]string, 0, len(devices.Devices))
	for _, d := range devices.Devices {
		pv, err := l.ensurePhysicalVolume(ctx, d.Path)
		if err != nil {
			log.Error(err, "failed to ensure physical volume", "device", d.Path)
			span.SetStatus(codes.Error, "failed to ensure physical volume")
			span.RecordError(err)
			recorder.Eventf(corev1.EventTypeWarning, provisioningPhysicalVolumeFailed, "Provisioning physical volume on device %s failed: %s", d.Path, err.Error())
			return nil, fmt.Errorf("failed to ensure physical volume on device %s: %w", d.Path, err)
		}
		if pv.VGName != "" && pv.VGName != vgName {
			log.V(1).Info("physical volume already part of a different volume group, skipping", "device", d.Path, "vg", pv.VGName)
			continue
		}
		pvs = append(pvs, d.Path)
	}
	if len(pvs) == 0 {
		log.Error(ErrNoDisksFound, "no disks found")
		span.SetStatus(codes.Error, "no disks found")
		recorder.Eventf(corev1.EventTypeWarning, provisioningPhysicalVolumeFailed, "Provisioning physical volume failed: %s", ErrNoDisksFound.Error())
		return nil, fmt.Errorf("%w: no disks found", core.ErrResourceExhausted)
	}
	return pvs, nil
}

func (l *LVM) ensurePhysicalVolume(ctx context.Context, device string) (*lvm.PhysicalVolume, error) {
	log := log.FromContext(ctx).WithValues("pv", device)
	recorder := events.FromContext(ctx)
	ctx, span := l.tracer.Start(ctx, "volume.lvm.csi/ensurePhysicalVolume", trace.WithAttributes(
		attribute.String("vol.pv", device),
	))
	defer span.End()
	if device == "" {
		log.Error(fmt.Errorf("device path is required"), "device", device)
		span.SetStatus(codes.Error, "device path is required")
		return nil, fmt.Errorf("device path is required")
	}

	pv, err := l.lvm.GetPhysicalVolume(ctx, device)
	if lvm.IgnoreNotFound(err) != nil {
		span.SetStatus(codes.Error, "failed to get physical volume")
		span.RecordError(err)
		return nil, fmt.Errorf("failed to get physical volume %s: %w", device, err)
	}
	if pv != nil {
		log.V(1).Info("physical volume already exists")
		return pv, nil
	}
	pvCreateOptions := lvm.CreatePVOptions{
		Name: device,
	}
	if err := l.lvm.CreatePhysicalVolume(ctx, pvCreateOptions); lvm.IgnoreAlreadyExists(err) != nil {
		log.Error(err, "failed to create physical volume")
		span.SetStatus(codes.Error, "failed to create physical volume")
		span.RecordError(err)
		return nil, fmt.Errorf("failed to create physical volume on device %s: %w", device, err)
	}

	if !errors.Is(err, lvm.ErrAlreadyExists) {
		recorder.Eventf(corev1.EventTypeNormal, provisionedPhysicalVolume, "Successfully provisioned physical volume on device %s", device)
	}

	if pv, err = l.lvm.GetPhysicalVolume(ctx, device); err != nil {
		span.SetStatus(codes.Error, "failed to get physical volume after creation")
		span.RecordError(err)
		return nil, fmt.Errorf("failed to get physical volume %s after creation: %w", device, err)
	}
	return pv, nil
}

// EnsureVolume ensures that the volume exists with the given name and size.
// If the volume already exists, it returns it. Otherwise it creates the
// volume and returns it.
func (l *LVM) EnsureVolume(ctx context.Context, volumeId string, capacity int64, limit int64, isMountOperation bool) (int64, error) {
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
		return 0, fmt.Errorf("%w: failed to parse volume id %s: %w", core.ErrInvalidArgument, volumeId, err)
	}

	log = log.WithValues("vg", id.VolumeGroup, "lv", id.LogicalVolume)

	if id.VolumeGroup == "" {
		log.Error(fmt.Errorf("volume group name is required"), "vg", id.VolumeGroup)
		span.SetStatus(codes.Error, "volume group name is required")
		return 0, fmt.Errorf("%w: volume group name is required", core.ErrInvalidArgument)
	}
	if capacity <= 0 || limit < 0 || (limit > 0 && limit < capacity) {
		log.Error(fmt.Errorf("invalid capacity or limit"), "capacity", capacity, "limit", limit)
		span.SetStatus(codes.Error, "invalid capacity or limit")
		return 0, fmt.Errorf("%w: invalid capacity %d or limit %d", core.ErrInvalidArgument, capacity, limit)
	}

	// Check for existing volume on the node.
	lv, err := l.lvm.GetLogicalVolume(ctx, id.VolumeGroup, id.LogicalVolume)
	if lvm.IgnoreNotFound(err) != nil {
		log.Error(err, "failed to check if volume exists")
		return 0, err
	}

	// Check if the logical volume is corrupted
	if lv != nil {
		corrupted, err := l.lvm.IsLogicalVolumeCorrupted(ctx, id.VolumeGroup, id.LogicalVolume)
		if err != nil {
			log.Error(err, "failed to check if logical volume is corrupted")
			span.SetStatus(codes.Error, "failed to check if logical volume is corrupted")
			span.RecordError(err)
			return 0, fmt.Errorf("failed to check if logical volume is corrupted: %w", err)
		}

		if corrupted {
			log.V(1).Info("found corrupted logical volume, removing it", "vg", id.VolumeGroup, "lv", id.LogicalVolume) // Remove the corrupted LV
			opts := lvm.RemoveLVOptions{
				Name: fmt.Sprintf("%s/%s", id.VolumeGroup, id.LogicalVolume),
			}
			if err := l.lvm.RemoveLogicalVolume(ctx, opts); err != nil {
				log.Error(err, "failed to remove corrupted logical volume", "vg", id.VolumeGroup, "lv", id.LogicalVolume)
				span.SetStatus(codes.Error, "failed to remove corrupted logical volume")
				span.RecordError(err)
				recorder.Eventf(corev1.EventTypeWarning, failedToRemoveCorruptedLogicalVolume,
					"Failed to remove corrupted logical volume %s/%s: %s",
					id.VolumeGroup, id.LogicalVolume, err.Error())
				return 0, fmt.Errorf("failed to remove corrupted logical volume %s/%s: %w", id.VolumeGroup, id.LogicalVolume, err)
			}

			log.V(1).Info("removed corrupted logical volume", "vg", id.VolumeGroup, "lv", id.LogicalVolume)
			recorder.Eventf(corev1.EventTypeNormal, removedCorruptedLogicalVolume,
				"Successfully removed corrupted logical volume %s/%s",
				id.VolumeGroup, id.LogicalVolume)

			// Set lv to nil so it will be recreated below
			lv = nil
		}
	}

	if err == nil && lv != nil {
		log.V(2).Info("found existing volume", "bytes", lv.Size)
		span.AddEvent("found existing volume", trace.WithAttributes(attribute.Int64("bytes", int64(lv.Size))))

		lvSize := int64(lv.Size)
		// Check volume size.
		if lvSize < capacity || (limit > 0 && lvSize > limit) {
			log.Error(err, "volume size mismatch", "request", capacity, "limit", limit, "actual", lv.Size)
			span.SetStatus(codes.Error, "volume size mismatch")
			recorder.Eventf(corev1.EventTypeWarning, provisionedLogicalVolumeSizeMismatch, "Volume size mismatch %s/%s: request %d, limit %d, actual %d", id.VolumeGroup, id.LogicalVolume, capacity, limit, lv.Size)
			return 0, fmt.Errorf("volume size mismatch: request %d, limit %d, actual %d: %w", capacity, limit, lv.Size, core.ErrVolumeSizeMismatch)
		}
		return lvSize, nil
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
		return 0, fmt.Errorf("failed to check if volume group exists: %w", err)
	}
	if vg == nil {
		log.V(2).Info("no existing volume group found, creating new one")
		devices, err := l.EnsurePhysicalVolumes(ctx, id.VolumeGroup)
		if err != nil {
			log.Error(err, "failed to ensure physical volumes")
			span.SetStatus(codes.Error, "failed to ensure physical volumes")
			span.RecordError(err)
			recorder.Eventf(corev1.EventTypeWarning, provisioningLogicalVolumeFailed, "Failed to ensure physical volumes created for %s/%s: %s", id.VolumeGroup, id.LogicalVolume, err.Error())
			return 0, err
		}
		if vg, err = l.EnsureVolumeGroup(ctx, id.VolumeGroup, devices); err != nil {
			log.Error(err, "failed to ensure volume group")
			span.SetStatus(codes.Error, "failed to ensure volume group")
			span.RecordError(err)
			recorder.Eventf(corev1.EventTypeWarning, provisioningLogicalVolumeFailed, "Failed to ensure volume group created for %s/%s: %s", id.VolumeGroup, id.LogicalVolume, err.Error())
			return 0, err
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

	allocatedSize, err := l.lvm.CreateLogicalVolume(ctx, createOps)
	if err != nil {
		log.Error(err, "failed to create logical volume", "capacity", capacity)
		span.SetStatus(codes.Error, "failed to create logical volume")
		span.RecordError(err)
		recorder.Eventf(corev1.EventTypeWarning, provisioningLogicalVolumeFailed, "Provisioning logical volume %s/%s failed: %s", id.VolumeGroup, id.LogicalVolume, err)
		if errors.Is(err, lvm.ErrResourceExhausted) {
			return 0, fmt.Errorf("failed to create logical volume: %w", core.ErrResourceExhausted)
		}
		return 0, fmt.Errorf("failed to create logical volume: %w", err)
	}

	if allocatedSize > capacity {
		log.V(1).Info("allocated logical volume size is larger than requested", "allocated", allocatedSize, "requested", capacity)
	}

	log.V(1).Info("created logical volume", "capacity", allocatedSize)
	span.AddEvent("created logical volume", trace.WithAttributes(attribute.Int64("capacity", allocatedSize)))
	recorder.Eventf(corev1.EventTypeNormal, provisionedLogicalVolume, "Successfully provisioned logical volume %s/%s", id.VolumeGroup, id.LogicalVolume)
	if isMountOperation {
		recorder.Eventf(corev1.EventTypeNormal, provisionedEmptyVolume, "A new empty volume was created during mount because the logical volume %s/%s was not found on the node. This may indicate the volume was not previously provisioned on this node.", id.VolumeGroup, id.LogicalVolume)
	}
	return allocatedSize, nil
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

	volumeGroup, err := l.lvm.ListVolumeGroups(ctx, &lvm.ListVGOptions{Select: "vg_tags=" + DefaultVolumeGroupTag})
	if err != nil {
		span.SetStatus(codes.Error, "failed to list volume groups")
		span.RecordError(err)
		log.Error(err, "failed to list volume groups")
		return fmt.Errorf("failed to list volume groups: %w", err)
	}

	totalLVCount := 0
	for _, vg := range volumeGroup {
		totalLVCount += int(vg.LVCount)
	}
	if totalLVCount > 0 {
		log.V(1).Info("found existing logical volumes, skipping VG and PV cleanup", "count", totalLVCount)
		span.AddEvent("found existing logical volumes, skipping VG and PV cleanup", trace.WithAttributes(attribute.Int("count", totalLVCount)))
		span.SetStatus(codes.Ok, "found existing logical volumes, skipping VG and PV cleanup")
		return nil
	}

	for _, vg := range volumeGroup {
		if err := l.removeVolumeGroup(ctx, vg.Name); err != nil {
			log.Error(err, "failed to remove volume group", "vg", vg.Name)
			span.SetStatus(codes.Error, "failed to remove volume group")
			span.RecordError(err)
			return fmt.Errorf("failed to remove volume group %s: %w", vg.Name, err)
		}
	}

	pvs, err := l.lvm.ListPhysicalVolumes(ctx, nil)
	if err != nil {
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
