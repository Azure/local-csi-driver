// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-csi/csi-lib-utils/protosanitizer"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"local-csi-driver/internal/pkg/telemetry"
)

// CombinedService is the interface that all CSI drivers must implement.
type CombinedService interface {
	GetIdentityServer() csi.IdentityServer
	GetControllerServer() csi.ControllerServer
	GetNodeServer() csi.NodeServer
	Register(ctx context.Context) error
}

// CombinedServer is used to configure, start and stop the CSI server.
type CombinedServer struct {
	nodeProto       string
	nodeAddr        string
	controllerProto string
	controllerAddr  string
	listen          func(network, address string) (net.Listener, error)

	driver CombinedService
	mp     metric.MeterProvider
	tp     trace.TracerProvider
}

// NewCombined creates a CSI server with separate Node and Controller endpoints.
//
// Both endpoints provide Identity RPCs. The Controller endpoint also provides
// Node metadata RPCs needed by the external-provisioner and external-resizer.
//
// The provided tracer provider will be used to create tracers for the server.
func NewCombined(nodeEndpoint, controllerEndpoint string, driver CombinedService, t telemetry.Provider) (*CombinedServer, error) {
	nodeProto, nodeAddr, err := parseEndpoint(nodeEndpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid node endpoint: %w", err)
	}
	controllerProto, controllerAddr, err := parseEndpoint(controllerEndpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid controller endpoint: %w", err)
	}
	if nodeProto == controllerProto && (nodeAddr == controllerAddr ||
		(nodeProto == "unix" && filepath.Clean(nodeAddr) == filepath.Clean(controllerAddr))) {
		return nil, fmt.Errorf("node and controller endpoints must be different")
	}
	if err := prepareEndpoint(nodeProto, nodeAddr); err != nil {
		return nil, err
	}
	if err := prepareEndpoint(controllerProto, controllerAddr); err != nil {
		return nil, err
	}

	return &CombinedServer{
		nodeProto:       nodeProto,
		nodeAddr:        nodeAddr,
		controllerProto: controllerProto,
		controllerAddr:  controllerAddr,
		listen:          net.Listen,
		driver:          driver,
		mp:              t.MeterProvider(),
		tp:              t.TraceProvider(),
	}, nil
}

// Start starts the CSI server.
//
// The server will listen on the configured endpoint and serve incoming
// requests.
//
// The server will stop when the provided context is canceled.
func (s *CombinedServer) Start(ctx context.Context) error {
	log := log.FromContext(ctx)
	log.V(2).Info("starting CSI servers", "nodeEndpoint", s.nodeAddr, "controllerEndpoint", s.controllerAddr)

	nodeListener, err := s.listen(s.nodeProto, s.nodeAddr)
	if err != nil {
		return fmt.Errorf("failed to create node listener on %s://%s: %w", s.nodeProto, s.nodeAddr, err)
	}
	defer func() { _ = nodeListener.Close() }()

	controllerListener, err := s.listen(s.controllerProto, s.controllerAddr)
	if err != nil {
		return fmt.Errorf("failed to create controller listener on %s://%s: %w", s.controllerProto, s.controllerAddr, err)
	}
	defer func() { _ = controllerListener.Close() }()

	opts := []otelgrpc.Option{
		otelgrpc.WithMeterProvider(s.mp),
		otelgrpc.WithTracerProvider(s.tp),
	}
	newServer := func() *grpc.Server {
		return grpc.NewServer(
			grpc.StatsHandler(otelgrpc.NewServerHandler(opts...)),
			grpc.UnaryInterceptor(LoggingInterceptor),
		)
	}
	nodeServer := newServer()
	controllerServer := newServer()

	csi.RegisterIdentityServer(nodeServer, s.driver.GetIdentityServer())
	csi.RegisterNodeServer(nodeServer, s.driver.GetNodeServer())
	csi.RegisterIdentityServer(controllerServer, s.driver.GetIdentityServer())
	csi.RegisterControllerServer(controllerServer, s.driver.GetControllerServer())
	csi.RegisterNodeServer(controllerServer, &nodeMetadataServer{node: s.driver.GetNodeServer()})

	// Ensure the CSIDriver object exists in the cluster.
	if err := s.driver.Register(ctx); err != nil {
		return fmt.Errorf("failed to register driver: %w", err)
	}

	group, ctx := errgroup.WithContext(ctx)
	group.Go(func() error {
		log.V(2).Info("listening for node connections", "endpoint", nodeListener.Addr())
		if err := nodeServer.Serve(nodeListener); !errors.Is(err, grpc.ErrServerStopped) {
			return err
		}
		return nil
	})
	group.Go(func() error {
		log.V(2).Info("listening for controller connections", "endpoint", controllerListener.Addr())
		if err := controllerServer.Serve(controllerListener); !errors.Is(err, grpc.ErrServerStopped) {
			return err
		}
		return nil
	})
	<-ctx.Done()
	stopped := make(chan struct{})
	go func() {
		controllerServer.GracefulStop()
		close(stopped)
	}()
	nodeServer.GracefulStop()
	<-stopped

	return group.Wait()
}

// nodeMetadataServer exposes discovery RPCs without Node volume operations.
type nodeMetadataServer struct {
	csi.UnimplementedNodeServer
	node csi.NodeServer
}

func (s *nodeMetadataServer) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return s.node.NodeGetInfo(ctx, req)
}

func (s *nodeMetadataServer) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return s.node.NodeGetCapabilities(ctx, req)
}

// NeedLeaderElection returns false since the CSI controllers should run on each
// node.
func (s *CombinedServer) NeedLeaderElection() bool {
	return false
}

// getLogLevel returns the log level for the given method.
func getLogLevel(method string) int {
	if method == "/csi.v1.Identity/Probe" ||
		method == "/csi.v1.Identity/GetPluginInfo" ||
		method == "/csi.v1.Identity/GetPluginCapabilities" ||
		method == "/csi.v1.Node/NodeGetInfo" ||
		method == "/csi.v1.Node/NodeGetCapabilities" ||
		method == "/csi.v1.Node/NodeGetVolumeStats" ||
		method == "/csi.v1.Controller/ControllerGetCapabilities" ||
		method == "/csi.v1.Controller/ListVolumes" ||
		method == "/csi.v1.Controller/GetCapacity" {
		return 4
	}
	return 2
}

// LoggingInterceptor for unary gRPC calls.
func LoggingInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	log := log.FromContext(ctx)
	level := getLogLevel(info.FullMethod)
	log.V(level).Info("gRPC request", "method", info.FullMethod, "request", protosanitizer.StripSecrets(req))

	resp, err := handler(ctx, req)
	if err != nil {
		log.Info("gRPC error", "method", info.FullMethod, "error", err)
	} else {
		log.V(level).Info("gRPC response", "method", info.FullMethod, "response", protosanitizer.StripSecrets(resp))
	}
	return resp, err
}
