// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package server

import (
	"context"
	"fmt"
	"net"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-csi/csi-lib-utils/protosanitizer"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
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
	proto string
	addr  string

	driver CombinedService
	mp     metric.MeterProvider
	tp     trace.TracerProvider
}

// NewCombinedServer creates a new CSI server.
//
// The server will listen on the provided endpoint and use the provided driver
// to handle incoming requests.
//
// The provided tracer provider will be used to create tracers for the server.
func NewCombined(endpoint string, driver CombinedService, t telemetry.Provider) (*CombinedServer, error) {
	proto, addr, err := parseEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	if err := prepareEndpoint(proto, addr); err != nil {
		return nil, err
	}

	return &CombinedServer{
		proto:  proto,
		addr:   addr,
		driver: driver,
		mp:     t.MeterProvider(),
		tp:     t.TraceProvider(),
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
	log.V(2).Info("starting CSI server", "endpoint", s.addr)

	listener, err := net.Listen(s.proto, s.addr)
	if err != nil {
		return fmt.Errorf("failed to create listener on %s://%s: %w", s.proto, s.addr, err)
	}

	opts := []otelgrpc.Option{
		otelgrpc.WithMeterProvider(s.mp),
		otelgrpc.WithTracerProvider(s.tp),
	}
	server := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler(opts...)),
		grpc.UnaryInterceptor(LoggingInterceptor),
	)

	csi.RegisterIdentityServer(server, s.driver.GetIdentityServer())
	csi.RegisterControllerServer(server, s.driver.GetControllerServer())
	csi.RegisterNodeServer(server, s.driver.GetNodeServer())

	// Ensure the CSIDriver object exists in the cluster.
	if err := s.driver.Register(ctx); err != nil {
		return fmt.Errorf("failed to register driver: %w", err)
	}

	errCh := make(chan error, 1)
	go func() {
		log.V(2).Info("listening for connections", "endpoint", listener.Addr())
		errCh <- server.Serve(listener)
		close(errCh)
	}()

	<-ctx.Done()
	server.GracefulStop()

	return <-errCh
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
