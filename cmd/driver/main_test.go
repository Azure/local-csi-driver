// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"local-csi-driver/internal/csi/node"
)

type nonLeaderRunnable struct {
	manager.RunnableFunc
}

func (nonLeaderRunnable) NeedLeaderElection() bool { return false }

func testManager(t *testing.T, handler http.Handler) manager.Manager {
	t.Helper()
	api := httptest.NewServer(handler)
	t.Cleanup(api.Close)
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{corev1.SchemeGroupVersion})
	mapper.Add(corev1.SchemeGroupVersion.WithKind("Node"), meta.RESTScopeRoot)
	mapper.Add(corev1.SchemeGroupVersion.WithKind("PersistentVolume"), meta.RESTScopeRoot)
	mgr, err := ctrl.NewManager(&rest.Config{Host: api.URL}, ctrl.Options{
		Scheme: scheme,
		MapperProvider: func(*rest.Config, *http.Client) (meta.RESTMapper, error) {
			return mapper, nil
		},
		Client:                 driverClientOptions(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}

func TestNodeReadsDoNotStartAnInformer(t *testing.T) {
	var gets, inventories atomic.Int32
	mgr := testManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/nodes/own-node", "/api/v1/nodes/other-node":
			gets.Add(1)
			name := "own-node"
			if r.URL.Path == "/api/v1/nodes/other-node" {
				name = "other-node"
			}
			_ = json.NewEncoder(w).Encode(&corev1.Node{
				TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Node"},
				ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"kubernetes.io/hostname": name}},
			})
		default:
			inventories.Add(1)
			http.Error(w, "unexpected inventory or watch", http.StatusInternalServerError)
		}
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// A cached read cannot succeed before the manager/cache is started.
	if err := mgr.GetClient().Get(ctx, client.ObjectKey{Name: "own-node"}, &corev1.Node{}); err != nil {
		t.Fatalf("targeted Node read failed: %v", err)
	}
	metadata := &metav1.PartialObjectMetadata{}
	metadata.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Node"))
	if err := mgr.GetClient().Get(ctx, client.ObjectKey{Name: "other-node"}, metadata); err != nil {
		t.Fatalf("cross-node deletion metadata check failed: %v", err)
	}
	if gets.Load() != 2 || inventories.Load() != 0 {
		t.Fatalf("GETs=%d, inventories/watches=%d; want two targeted GETs only", gets.Load(), inventories.Load())
	}
}

func TestPVIdentityIndexGatesRunnableStartup(t *testing.T) {
	for _, withController := range []bool{false, true} {
		t.Run(fmt.Sprintf("gc-controller=%t", withController), func(t *testing.T) {
			listed := make(chan struct{}, 1)
			release := make(chan struct{})
			var lists atomic.Int32
			mgr := testManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path != "/api/v1/persistentvolumes" {
					http.Error(w, "unexpected resource", http.StatusNotFound)
					return
				}
				watch := r.URL.Query().Get("watch") == "true"
				if watch && r.URL.Query().Get("sendInitialEvents") != "true" {
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				lists.Add(1)
				select {
				case listed <- struct{}{}:
				default:
				}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				if watch {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"type": "BOOKMARK",
						"object": &corev1.PersistentVolume{
							TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolume"},
							ObjectMeta: metav1.ObjectMeta{
								ResourceVersion: "1",
								Annotations:     map[string]string{"k8s.io/initial-events-end": "true"},
							},
						},
					})
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				} else {
					_ = json.NewEncoder(w).Encode(&corev1.PersistentVolumeList{
						TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeList"},
						ListMeta: metav1.ListMeta{ResourceVersion: "1"},
						Items:    []corev1.PersistentVolume{},
					})
				}
			}))
			if withController {
				err := ctrl.NewControllerManagedBy(mgr).For(&corev1.PersistentVolume{}).
					Complete(reconcile.Func(func(context.Context, ctrl.Request) (ctrl.Result, error) {
						return ctrl.Result{}, nil
					}))
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := node.RegisterPVIdentityIndex(context.Background(), mgr.GetFieldIndexer()); err != nil {
				t.Fatal(err)
			}
			serving := make(chan struct{})
			if err := mgr.Add(nonLeaderRunnable{manager.RunnableFunc(func(ctx context.Context) error {
				close(serving)
				<-ctx.Done()
				return nil
			})}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			done := make(chan error, 1)
			go func() { done <- mgr.Start(ctx) }()
			t.Cleanup(func() {
				cancel()
				if err := <-done; err != nil {
					t.Errorf("manager shutdown: %v", err)
				}
			})
			select {
			case <-listed:
			case <-ctx.Done():
				t.Fatal("PV inventory never started")
			}
			select {
			case <-serving:
				t.Fatal("CSI-like runnable started before PV cache synchronization")
			case <-time.After(100 * time.Millisecond):
			}
			close(release)
			select {
			case <-serving:
			case <-ctx.Done():
				t.Fatal("runnable did not start after PV synchronization")
			}
			if lists.Load() != 1 {
				t.Fatalf("PV inventories=%d; index and controller must share one informer", lists.Load())
			}
		})
	}
}
