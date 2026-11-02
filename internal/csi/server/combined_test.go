// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package server

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"local-csi-driver/internal/csi/identity"
	"local-csi-driver/internal/pkg/telemetry"
)

type testCombinedService struct {
	csi.UnimplementedNodeServer
	csi.UnimplementedControllerServer
	registerErr   error
	registerCalls int
}

func (d *testCombinedService) GetIdentityServer() csi.IdentityServer {
	return identity.New("test.csi.local", "test")
}

func (d *testCombinedService) GetControllerServer() csi.ControllerServer { return d }
func (d *testCombinedService) GetNodeServer() csi.NodeServer             { return d }

func (d *testCombinedService) Register(context.Context) error {
	d.registerCalls++
	return d.registerErr
}

func (d *testCombinedService) NodeGetInfo(context.Context, *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{NodeId: "node-1", AccessibleTopology: &csi.Topology{
		Segments: map[string]string{"topology.test/node": "node-1"},
	}}, nil
}

func (d *testCombinedService) NodeGetCapabilities(context.Context, *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{Capabilities: []*csi.NodeServiceCapability{{
		Type: &csi.NodeServiceCapability_Rpc{Rpc: &csi.NodeServiceCapability_RPC{
			Type: csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
		}},
	}}}, nil
}

func (d *testCombinedService) NodeStageVolume(context.Context, *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	return &csi.NodeStageVolumeResponse{}, nil
}

func (d *testCombinedService) NodeUnstageVolume(context.Context, *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (d *testCombinedService) NodePublishVolume(context.Context, *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	return &csi.NodePublishVolumeResponse{}, nil
}

func (d *testCombinedService) NodeUnpublishVolume(context.Context, *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (d *testCombinedService) NodeExpandVolume(context.Context, *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	return &csi.NodeExpandVolumeResponse{}, nil
}

func (d *testCombinedService) NodeGetVolumeStats(context.Context, *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	return &csi.NodeGetVolumeStatsResponse{}, nil
}

func (d *testCombinedService) ControllerGetCapabilities(context.Context, *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	return &csi.ControllerGetCapabilitiesResponse{}, nil
}

func (d *testCombinedService) CreateVolume(context.Context, *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	return &csi.CreateVolumeResponse{}, nil
}

func (d *testCombinedService) DeleteVolume(context.Context, *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	return &csi.DeleteVolumeResponse{}, nil
}

func (d *testCombinedService) ControllerExpandVolume(context.Context, *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	return &csi.ControllerExpandVolumeResponse{}, nil
}

func newTestCombined(t *testing.T, driver *testCombinedService) *CombinedServer {
	t.Helper()
	dir, err := os.MkdirTemp("", "csi-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	provider, err := telemetry.New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := provider.MeterProvider().Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
		if err := provider.TraceProvider().Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	server, err := NewCombined("unix://"+filepath.Join(dir, "node.sock"),
		"unix://"+filepath.Join(dir, "controller.sock"), driver, provider)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func testConnection(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///"+addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", addr)
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
	})
	return conn
}

func TestCombinedEndpointIsolation(t *testing.T) {
	driver := &testCombinedService{}
	server := newTestCombined(t, driver)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("CSI servers did not stop")
			return
		}
		if driver.registerCalls != 1 {
			t.Errorf("Register called %d times, want 1", driver.registerCalls)
		}
	})

	nodeConn := testConnection(t, server.nodeAddr)
	controllerConn := testConnection(t, server.controllerAddr)
	for _, conn := range []*grpc.ClientConn{nodeConn, controllerConn} {
		id := csi.NewIdentityClient(conn)
		info, err := id.GetPluginInfo(ctx, &csi.GetPluginInfoRequest{}, grpc.WaitForReady(true))
		if err != nil {
			t.Fatal(err)
		}
		if info.GetName() != "test.csi.local" {
			t.Fatalf("unexpected plugin identity: %v", info)
		}
		if _, err := id.Probe(ctx, &csi.ProbeRequest{}); err != nil {
			t.Fatal(err)
		}
		if _, err := id.GetPluginCapabilities(ctx, &csi.GetPluginCapabilitiesRequest{}); err != nil {
			t.Fatal(err)
		}
		nodeInfo, err := csi.NewNodeClient(conn).NodeGetInfo(ctx, &csi.NodeGetInfoRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if nodeInfo.GetNodeId() != "node-1" || nodeInfo.GetAccessibleTopology().GetSegments()["topology.test/node"] != "node-1" {
			t.Fatalf("unexpected node metadata: %v", nodeInfo)
		}
		nodeCaps, err := csi.NewNodeClient(conn).NodeGetCapabilities(ctx, &csi.NodeGetCapabilitiesRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if len(nodeCaps.Capabilities) != 1 || nodeCaps.Capabilities[0].GetRpc().GetType() != csi.NodeServiceCapability_RPC_EXPAND_VOLUME {
			t.Fatalf("unexpected node capabilities: %v", nodeCaps)
		}
	}

	nodeCalls := []struct {
		method string
		req    any
		resp   any
	}{
		{"NodeStageVolume", &csi.NodeStageVolumeRequest{}, &csi.NodeStageVolumeResponse{}},
		{"NodeUnstageVolume", &csi.NodeUnstageVolumeRequest{}, &csi.NodeUnstageVolumeResponse{}},
		{"NodePublishVolume", &csi.NodePublishVolumeRequest{}, &csi.NodePublishVolumeResponse{}},
		{"NodeUnpublishVolume", &csi.NodeUnpublishVolumeRequest{}, &csi.NodeUnpublishVolumeResponse{}},
		{"NodeExpandVolume", &csi.NodeExpandVolumeRequest{}, &csi.NodeExpandVolumeResponse{}},
		{"NodeGetVolumeStats", &csi.NodeGetVolumeStatsRequest{}, &csi.NodeGetVolumeStatsResponse{}},
	}
	for _, call := range nodeCalls {
		t.Run(call.method, func(t *testing.T) {
			method := "/csi.v1.Node/" + call.method
			if err := nodeConn.Invoke(ctx, method, call.req, call.resp); err != nil {
				t.Fatalf("Node endpoint: %v", err)
			}
			if err := controllerConn.Invoke(ctx, method, call.req, call.resp); status.Code(err) != codes.Unimplemented {
				t.Fatalf("Controller endpoint: got %v, want Unimplemented", err)
			}
		})
	}
	controllerCalls := []struct {
		method string
		req    any
		resp   any
	}{
		{"ControllerGetCapabilities", &csi.ControllerGetCapabilitiesRequest{}, &csi.ControllerGetCapabilitiesResponse{}},
		{"CreateVolume", &csi.CreateVolumeRequest{}, &csi.CreateVolumeResponse{}},
		{"DeleteVolume", &csi.DeleteVolumeRequest{}, &csi.DeleteVolumeResponse{}},
		{"ControllerExpandVolume", &csi.ControllerExpandVolumeRequest{}, &csi.ControllerExpandVolumeResponse{}},
	}
	for _, call := range controllerCalls {
		t.Run(call.method, func(t *testing.T) {
			method := "/csi.v1.Controller/" + call.method
			if err := controllerConn.Invoke(ctx, method, call.req, call.resp); err != nil {
				t.Fatalf("Controller endpoint: %v", err)
			}
			if err := nodeConn.Invoke(ctx, method, call.req, call.resp); status.Code(err) != codes.Unimplemented {
				t.Fatalf("Node endpoint: got %v, want Unimplemented", err)
			}
		})
	}
}

func TestCombinedCanceledStart(t *testing.T) {
	server := newTestCombined(t, &testCombinedService{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatalf("canceled startup should stop both servers cleanly: %v", err)
	}
}

type failingListener struct {
	net.Listener
	fail chan struct{}
	err  error
}

func (l *failingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	select {
	case <-l.fail:
		if conn != nil {
			_ = conn.Close()
		}
		return nil, l.err
	default:
		return conn, err
	}
}

func (l *failingListener) failAccept() error {
	close(l.fail)
	return l.Close()
}

func TestCombinedServingFailureStopsPeer(t *testing.T) {
	for _, endpoint := range []string{"node", "controller"} {
		t.Run(endpoint, func(t *testing.T) {
			server := newTestCombined(t, &testCombinedService{})
			acceptErr := errors.New("non-temporary accept failure")
			failedAddr, peerAddr := server.nodeAddr, server.controllerAddr
			if endpoint == "controller" {
				failedAddr, peerAddr = peerAddr, failedAddr
			}
			injected := make(chan *failingListener, 1)
			server.listen = func(network, address string) (net.Listener, error) {
				listener, err := net.Listen(network, address)
				if err != nil || address != failedAddr {
					return listener, err
				}
				wrapped := &failingListener{Listener: listener, fail: make(chan struct{}), err: acceptErr}
				injected <- wrapped
				return wrapped, nil
			}

			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- server.Start(parent) }()
			finished := false
			t.Cleanup(func() {
				cancel()
				if !finished {
					select {
					case err := <-done:
						if err != nil && !errors.Is(err, acceptErr) {
							t.Errorf("unexpected shutdown error during cleanup: %v", err)
						}
					case <-time.After(10 * time.Second):
						t.Error("CSI servers did not stop during cleanup")
					}
				}
			})

			ctx, deadline := context.WithTimeout(parent, 10*time.Second)
			defer deadline()
			peer := testConnection(t, peerAddr)
			failed := testConnection(t, failedAddr)
			for _, conn := range []*grpc.ClientConn{peer, failed} {
				if _, err := csi.NewIdentityClient(conn).Probe(ctx, &csi.ProbeRequest{}, grpc.WaitForReady(true)); err != nil {
					t.Fatalf("endpoint did not become ready: %v", err)
				}
			}
			listener := <-injected
			if err := listener.failAccept(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				finished = true
				if !errors.Is(err, acceptErr) {
					t.Fatalf("Start returned %v, want injected Accept error", err)
				}
			case <-ctx.Done():
				t.Fatal("Start did not return after serving failure")
			}
			if err := parent.Err(); err != nil {
				t.Fatalf("parent context was canceled: %v", err)
			}
			if _, err := csi.NewIdentityClient(peer).Probe(ctx, &csi.ProbeRequest{}); status.Code(err) != codes.Unavailable {
				t.Fatalf("peer endpoint: got %v, want Unavailable", err)
			}
			conn, err := (&net.Dialer{}).DialContext(ctx, "unix", peerAddr)
			if err == nil {
				_ = conn.Close()
				t.Fatal("peer listener still accepts connections")
			}
		})
	}
}

func TestCombinedInvalidEndpoints(t *testing.T) {
	for _, tt := range []struct {
		name       string
		node       string
		controller string
	}{
		{"invalid node", "invalid", "unix:///tmp/controller.sock"},
		{"invalid controller", "unix:///tmp/node.sock", "invalid"},
		{"same endpoint", "unix:///tmp/node.sock", "unix:///tmp/node.sock"},
		{"same normalized endpoint", "unix:///tmp/node.sock", "unix:///tmp/./node.sock"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewCombined(tt.node, tt.controller, nil, nil); err == nil {
				t.Fatal("expected endpoint validation error")
			}
		})
	}
}

func TestCombinedStartupFailureClosesListeners(t *testing.T) {
	registrationErr := errors.New("registration failed")
	for _, tt := range []struct {
		name          string
		registerErr   error
		blockEndpoint string
		wantErr       string
	}{
		{name: "registration", registerErr: registrationErr, wantErr: registrationErr.Error()},
		{name: "controller listen", blockEndpoint: "controller", wantErr: "failed to create controller listener"},
		{name: "node listen", blockEndpoint: "node", wantErr: "failed to create node listener"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestCombined(t, &testCombinedService{registerErr: tt.registerErr})
			if tt.blockEndpoint != "" {
				addr := server.controllerAddr
				if tt.blockEndpoint == "node" {
					addr = server.nodeAddr
				}
				listener, err := net.Listen("unix", addr)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = listener.Close() }()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := server.Start(ctx)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("got %v, want %s", err, tt.wantErr)
			}
			if tt.registerErr != nil && !errors.Is(err, tt.registerErr) {
				t.Fatalf("registration error not preserved: %v", err)
			}
			if tt.blockEndpoint != "node" {
				listener, err := net.Listen("unix", server.nodeAddr)
				if err != nil {
					t.Fatalf("node listener leaked: %v", err)
				}
				if err := listener.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if tt.blockEndpoint == "" {
				listener, err := net.Listen("unix", server.controllerAddr)
				if err != nil {
					t.Fatalf("controller listener leaked: %v", err)
				}
				if err := listener.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
