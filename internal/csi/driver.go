// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package csi

import (
	"context"
	"fmt"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"go.opentelemetry.io/otel/trace"
	storagev1 "k8s.io/api/storage/v1"
	kevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"local-csi-driver/internal/csi/controller"
	"local-csi-driver/internal/csi/core"
	"local-csi-driver/internal/csi/identity"
	"local-csi-driver/internal/csi/mounter"
	"local-csi-driver/internal/csi/node"
	"local-csi-driver/internal/pkg/events"
)

const (
	// Version is the version of the CSI driver.
	Version = "0.0.1"

	// SelectedInitialNodeParam is used by the CSI driver to indicate the node
	// that received the CreateVolume call and created the volume.
	SelectedInitialNodeParam = "localdisk.csi.acstor.io/selected-initial-node"

	// SelctedNodeAnnotation is used by the CSI driver to indicate the node that
	// received the NodeStage call in cases where the node is different from the
	// SelectedInitialNodeParam. In such cases the volume is recreated on
	// the node that received the NodeStage call to unblock workloads.
	SelectedNodeAnnotation = "localdisk.csi.acstor.io/selected-node"
)

type Driver struct {
	is *identity.Server
	cs *controller.Server
	ns *node.Server

	client   client.Client
	volume   core.Interface
	recorder kevents.EventRecorder
	tp       trace.TracerProvider
}

// NewCombined creates a new CSI driver with controller and node capabilities.
func NewCombined(nodeID string, volumeClient core.Interface, client client.Client, removePvNodeAffinity bool, recorder kevents.EventRecorder, tp trace.TracerProvider) *Driver {
	d := &Driver{
		is:       identity.New(volumeClient.GetDriverName(), Version),
		cs:       controller.New(volumeClient, volumeClient.GetControllerDriverCapabilities(), volumeClient.GetNodeAccessModes(), mounter.New(tp), client, nodeID, SelectedNodeAnnotation, SelectedInitialNodeParam, removePvNodeAffinity, recorder, tp),
		ns:       node.New(volumeClient, nodeID, SelectedNodeAnnotation, SelectedInitialNodeParam, volumeClient.GetDriverName(), volumeClient.GetNodeDriverCapabilities(), mounter.New(tp), client, removePvNodeAffinity, recorder, tp),
		client:   client,
		volume:   volumeClient,
		recorder: recorder,
		tp:       tp,
	}

	return d
}

// NewController creates a new CSI driver for the controller service.
func NewController(volumeClient core.Interface, client client.Client, removePvNodeAffinity bool, recorder kevents.EventRecorder, tp trace.TracerProvider) *Driver {
	return &Driver{
		is:       identity.New(volumeClient.GetDriverName(), Version),
		cs:       controller.New(volumeClient, volumeClient.GetControllerDriverCapabilities(), volumeClient.GetNodeAccessModes(), mounter.New(tp), client, "", SelectedNodeAnnotation, SelectedInitialNodeParam, removePvNodeAffinity, events.NewNoopRecorder(), tp),
		ns:       nil,
		client:   client,
		volume:   volumeClient,
		recorder: recorder,
		tp:       tp,
	}
}

// NewNode creates a new CSI driver for the node service.
func NewNode(nodeID string, volumeClient core.Interface, client client.Client, removePvNodeAffinity bool, recorder kevents.EventRecorder, tp trace.TracerProvider) *Driver {
	return &Driver{
		is:       identity.New(volumeClient.GetDriverName(), Version),
		cs:       nil,
		ns:       node.New(volumeClient, nodeID, SelectedNodeAnnotation, SelectedInitialNodeParam, volumeClient.GetDriverName(), volumeClient.GetNodeDriverCapabilities(), mounter.New(tp), client, removePvNodeAffinity, recorder, tp),
		client:   client,
		volume:   volumeClient,
		recorder: recorder,
		tp:       tp,
	}
}

// Register registers the CSI driver with the Kubernetes API server if it
// doesn't exist.
//
// TODO(sc): Add cluster manager deployment as owner.
func (d *Driver) Register(ctx context.Context) error {
	obj := &storagev1.CSIDriver{}
	if err := d.client.Get(ctx, client.ObjectKeyFromObject(d.volume.GetCSIDriver()), obj); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed to get CSIDriver %s: %w", d.volume.GetCSIDriver(), err)
		}
		return d.client.Create(ctx, d.volume.GetCSIDriver())
	}
	return nil
}

func (d *Driver) GetIdentityServer() csi.IdentityServer {
	return d.is
}

func (d *Driver) GetControllerServer() csi.ControllerServer {
	return d.cs
}

func (d *Driver) GetNodeServer() csi.NodeServer {
	return d.ns
}
