// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.

	driver "local-csi-driver/internal/csi"
	"local-csi-driver/internal/csi/core/lvm"
	"local-csi-driver/internal/csi/mounter"
	"local-csi-driver/internal/csi/server"
	"local-csi-driver/internal/gc"
	"local-csi-driver/internal/pkg/block"
	"local-csi-driver/internal/pkg/events"
	lvmMgr "local-csi-driver/internal/pkg/lvm"
	"local-csi-driver/internal/pkg/probe"
	"local-csi-driver/internal/pkg/raid"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/klog/v2/textlogger"
	"k8s.io/utils/exec"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"local-csi-driver/internal/pkg/telemetry"
	"local-csi-driver/internal/pkg/version"
)

const (
	// ServiceName is the name of the service used in traces.
	ServiceName = "local-csi-driver"

	// terminationMessagePath is the path to the termination message file for the
	// Kubernetes pod. This file is used to store the last error message.
	terminationMessagePath = "/tmp/termination-log"
)

var (
	scheme = runtime.NewScheme()
	log    = ctrl.Log
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
}

func main() {
	var nodeName string
	var podName string
	var namespace string
	var csiAddr string
	var metricsAddr string
	var probeAddr string
	var secureMetrics bool
	var workers int
	var apiQPS int
	var apiBurst int
	var traceAddr string
	var traceSampleRate int
	var traceServiceID string
	var printVersionAndExit bool
	var eventRecorderEnabled bool
	var enableCleanup bool
	var enablePVGarbageCollection bool
	var enableLVMOrphanCleanup bool
	var lvmOrphanCleanupInterval time.Duration
	var runAlongsideWebhook bool
	flag.StringVar(&nodeName, "node-name", "",
		"The name of the node this agent is running on.")
	flag.StringVar(&podName, "pod-name", "",
		"The name of the pod this agent is running on.")
	flag.StringVar(&namespace, "namespace", "default",
		"The namespace to use for creating objects.")
	flag.StringVar(&csiAddr, "csi-bind-address", "unix:///tmp/csi.sock",
		"The address the CSI endpoint binds to. Format: <proto>://<address>")
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.IntVar(&workers, "worker-threads", 10,
		"Number of worker threads per controller, in other words nr. of simultaneous CSI calls.")
	flag.IntVar(&apiQPS, "kube-api-qps", 20,
		"QPS to use while communicating with the kubernetes apiserver. Defaults to 20.")
	flag.IntVar(&apiBurst, "kube-api-burst", 30,
		"Burst to use while communicating with the kubernetes apiserver. Defaults to 30.")
	flag.StringVar(&traceAddr, "trace-address", "",
		"The address to send traces to. Disables tracing if not set.")
	flag.IntVar(&traceSampleRate, "trace-sample-rate", 0,
		"Sample rate per million. 0 to disable tracing, 1000000 to trace everything.")
	flag.StringVar(&traceServiceID, "trace-service-id", "",
		"The service id to set in traces that identifies this service instance.")
	flag.BoolVar(&printVersionAndExit, "version", false, "Print version and exit")
	flag.BoolVar(&eventRecorderEnabled, "event-recorder-enabled", true,
		"If enabled, the driver will use the event recorder to record events. This is useful for debugging and monitoring purposes.")
	flag.BoolVar(&enableCleanup, "enable-cleanup", true, "If enabled, the driver will clean up the LVM volume groups and persistent volumes when not in use")
	flag.BoolVar(&enablePVGarbageCollection, "enable-pv-garbage-collection", true,
		"If enabled, the PV garbage collection controller will monitor PersistentVolumes for node annotation mismatches and clean up orphaned LVM volumes.")
	flag.BoolVar(&enableLVMOrphanCleanup, "enable-lvm-orphan-cleanup", true,
		"If enabled, the LVM orphan cleanup controller will periodically scan and clean up orphaned LVM volumes on the node.")
	flag.DurationVar(&lvmOrphanCleanupInterval, "lvm-orphan-cleanup-interval", 30*time.Minute,
		"Interval for the LVM orphan cleanup controller to scan and clean up orphaned volumes.")
	flag.BoolVar(&runAlongsideWebhook, "run-alongside-webhook", false,
		"If set, indicates that the driver is running alongside a separate webhook deployment. This affects PV node affinity behavior.")
	// Initialize logger flagsconfig.
	logConfig := textlogger.NewConfig(textlogger.VerbosityFlagName("v"))
	logConfig.AddFlags(flag.CommandLine)

	// Parse flags.
	flag.Parse()

	ctrl.SetLogger(textlogger.NewLogger(logConfig))

	// Log version set by build process.
	version.Log(log)
	if printVersionAndExit {
		return
	}

	// Parent context will be closed on interrupt or sigterm. From this point,
	// context should be closed before exiting.
	ctx, cancel := context.WithCancel(ctrl.SetupSignalHandler())
	defer cancel()

	// Add telemetry.
	t, err := telemetry.New(ctx,
		// telemetry.WithOTLP(),	// Needs testing.
		telemetry.WithServiceInstanceID(traceServiceID),
		telemetry.WithPrometheus(metrics.Registry),
		telemetry.WithEndpoint(traceAddr),
		telemetry.WithTraceSampleRate(traceSampleRate),
	)
	if err != nil {
		logAndExit(err, "failed to initialize telemetry")
	}

	// TraceProvider is passed into controllers and other components that need
	// to create spans.
	tp := t.TraceProvider()

	ctx, span := tp.Tracer("main").Start(ctx, "setup")
	defer span.End()

	// Create mounter for volume operations
	mounterInstance := mounter.New(tp)

	// Setup metrics server
	var metricsServerOptions metricsserver.Options
	if metricsAddr == "0" {
		log.Info("metrics server disabled")
		metricsServerOptions = metricsserver.Options{
			BindAddress: "0", // Disable metrics
		}
	} else {
		// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
		// More info:
		// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.1/pkg/metrics/server
		// - https://book.kubebuilder.io/reference/metrics.html
		metricsServerOptions = metricsserver.Options{
			BindAddress:   metricsAddr,
			SecureServing: secureMetrics,
		}

		if secureMetrics {
			// FilterProvider is used to protect the metrics endpoint with authn/authz.
			// These configurations ensure that only authorized users and service accounts
			// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
			// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.1/pkg/metrics/filters#WithAuthenticationAndAuthorization
			metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
		}
	}

	// Override the default QPS and Burst settings for the Kubernetes client.
	restCfg, err := ctrl.GetConfig()
	if err != nil {
		logAndExit(err, "unable to get rest config for api server")
	}
	restCfg.QPS = float32(apiQPS)
	restCfg.Burst = apiBurst

	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         false, // No webhooks, no leader election needed
		LeaderElectionID:       "local-csi-driver",
	})
	if err != nil {
		logAndExit(err, "unable to start manager")
	}

	// Add telemetry to manager.
	if err := mgr.Add(t); err != nil {
		logAndExit(err, "unable to add telemetry to internal manager")
	}

	// Setup all Controllers.
	if err := raid.Initialize(exec.New()); err != nil {
		logAndExit(err, "failed to initialize raid")
	}

	// TODO(sc): move filter to controller so we can read filters from
	// storageclass params. Hardcoded for now.
	blockDevUtils := block.New()
	deviceProbe := probe.New(blockDevUtils, probe.EphemeralDiskFilter)

	// Create the LVM manager.
	// LVM manager is an abstraction that understands how to create and
	// manage LVM resources like PV, VG, and LV.
	lvmMgr := lvmMgr.NewClient(lvmMgr.WithTracerProvider(tp), lvmMgr.WithBlockDeviceUtilities(blockDevUtils))
	if !lvmMgr.IsSupported() {
		logAndExit(fmt.Errorf("lvm is not supported on this node"), "lvm is not supported")
	}

	// Create the LVM CSI server.
	//
	// Volume client is an abstraction that understands csi requests and
	// responses and how to implement them for a storage type.
	volumeClient, err := lvm.New(podName, nodeName, namespace, enableCleanup, deviceProbe, lvmMgr, tp)
	if err != nil {
		logAndExit(err, "unable to create lvm volume client")
	}

	// setup the volume client with the manager for running volume client
	// cleanup tasks when the manager is stopped.
	err = mgr.Add(volumeClient)
	if err != nil {
		logAndExit(err, "unable to setup volume client with manager")
	}

	recorder := events.NewNoopRecorder()
	if eventRecorderEnabled {
		log.Info("event recorder enabled")
		recorder = mgr.GetEventRecorderFor("local-csi-driver")
	}

	// Setup PV garbage collection controller to clean up orphaned LVM volumes
	// when PV node annotations don't match the current node
	if enablePVGarbageCollection {
		pvGCController := gc.NewPVFailoverReconciler(
			mgr.GetClient(),
			mgr.GetScheme(),
			recorder,
			nodeName,
			driver.SelectedNodeAnnotation,
			driver.SelectedInitialNodeParam,
			volumeClient,
			lvmMgr,
			mounterInstance,
		)

		if err = pvGCController.SetupWithManager(mgr); err != nil {
			logAndExit(err, "unable to create PV garbage collection controller")
		}
		log.Info("PV garbage collection controller configured")
	} else {
		log.Info("PV garbage collection controller disabled")
	}

	// Setup LVM orphan cleanup controller for periodic scanning and cleanup
	if enableLVMOrphanCleanup {
		lvmOrphanCleanup := gc.NewLVMOrphanScanner(
			mgr.GetClient(),
			mgr.GetScheme(),
			recorder,
			nodeName,
			driver.SelectedNodeAnnotation,
			driver.SelectedInitialNodeParam,
			lvmMgr,
			volumeClient,
			mounterInstance,
			gc.LVMOrphanScannerConfig{
				ReconcileInterval: lvmOrphanCleanupInterval,
			},
		)

		if err = lvmOrphanCleanup.SetupWithManager(mgr); err != nil {
			logAndExit(err, "unable to setup LVM orphan cleanup controller with manager")
		}
		log.Info("LVM orphan cleanup controller configured")
	} else {
		log.Info("LVM orphan cleanup controller disabled")
	}

	// Create the CSI server.
	csiServer, err := server.NewCombined(csiAddr, driver.NewCombined(nodeName, volumeClient, mgr.GetClient(), runAlongsideWebhook, recorder, tp), t)
	if err != nil {
		logAndExit(err, "unable to create csi server")
	}
	if err := mgr.Add(csiServer); err != nil {
		logAndExit(err, "unable to add csi server to internal manager")
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logAndExit(err, "unable to set up health check")
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logAndExit(err, "unable to set up ready check")
	}

	log.Info("starting manager")
	span.AddEvent("starting manager")
	if err := mgr.Start(ctx); err != nil {
		logAndExit(err, "problem running manager")
	}
}

// logAndExit logs the error and exits the program with a non-zero status code.
// It also writes the error message to the termination message file, if possible.
// This is useful for debugging and monitoring purposes.
// The termination message file is used by Kubernetes to display the last error message.
func logAndExit(err error, msg string) {
	logError(err, msg)
	os.Exit(1)
}

func logError(err error, msg string) {
	log.Error(err, msg)
	errMsg := fmt.Sprintf("%s: %v", msg, err)
	parentDir := filepath.Dir(terminationMessagePath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		log.Error(err, "failed to create directory for termination message")
		return
	}
	if err := os.WriteFile(terminationMessagePath, []byte(errMsg), 0600); err != nil {
		log.Error(err, "failed to write termination message")
	}
}
