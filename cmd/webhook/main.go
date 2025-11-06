// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/open-policy-agent/cert-controller/pkg/rotator"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/klog/v2/textlogger"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"local-csi-driver/internal/pkg/events"
	"local-csi-driver/internal/pkg/version"
	"local-csi-driver/internal/webhook/enforceEphemeral"
	"local-csi-driver/internal/webhook/hyperconverged"
)

const (
	// ServiceName is the name of the service used in traces.
	ServiceName = "local-csi-webhook"

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
	var namespace string
	var webhookSvcName string
	var webhookPort int
	var enforceEphemeralWebhookConfig string
	var hyperconvergedWebhookConfig string
	var certSecretName string
	var metricsAddr string
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var leaderElectionID string
	var enableLeaderElection bool
	var apiQPS int
	var apiBurst int
	var traceAddr string
	var traceSampleRate int
	var traceServiceID string
	var tlsOpts []func(*tls.Config)
	var printVersionAndExit bool
	var eventRecorderEnabled bool

	flag.StringVar(&namespace, "namespace", "default",
		"The namespace to use for creating objects.")
	flag.StringVar(&webhookSvcName, "webhook-service-name", "",
		"The name of the service used by the webhook server. Must be set to enable webhooks.")
	flag.IntVar(&webhookPort, "webhook-port", 9443, "The port the webhook server listens on.")
	flag.StringVar(&enforceEphemeralWebhookConfig, "enforce-ephemeral-webhook-config", "",
		"The name of the enforce ephemeral webhook config. Must be set to enable the webhook.")
	flag.StringVar(&hyperconvergedWebhookConfig, "hyperconverged-webhook-config", "",
		"The name of the hyperconverged webhook config. Must be set to enable the webhook.")
	flag.StringVar(&certSecretName, "certificate-secret-name", "",
		"The name of the secret used to store the certificates. Must be set to enable webhooks.")
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.StringVar(&leaderElectionID, "leader-election-id", "local-csi-webhook-leader-election",
		"The ID used for leader election.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", true,
		"Enable leader election for webhook manager. Enabling this will ensure there is only one active webhook manager.")
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
		"If enabled, the webhook will use the event recorder to record events. This is useful for debugging and monitoring purposes.")

	// Initialize logger flags config.
	logConfig := textlogger.NewConfig(textlogger.VerbosityFlagName("v"))
	logConfig.AddFlags(flag.CommandLine)

	flag.Parse()

	ctrl.SetLogger(textlogger.NewLogger(logConfig))

	// Log version set by build process.
	version.Log(log)
	if printVersionAndExit {
		return
	}

	log.Info("starting webhook server")

	// Validate required flags
	if webhookSvcName == "" {
		logAndExit(fmt.Errorf("--webhook-service-name is required"), "webhook service name must be set")
	}
	if certSecretName == "" {
		logAndExit(fmt.Errorf("--certificate-secret-name is required"), "certificate secret name must be set")
	}
	if enforceEphemeralWebhookConfig == "" && hyperconvergedWebhookConfig == "" {
		logAndExit(fmt.Errorf("at least one webhook must be enabled"), "no webhooks configured")
	}

	// Setup signal context
	ctx := ctrl.SetupSignalHandler()

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		log.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	webhookServer := webhook.NewServer(webhook.Options{
		TLSOpts: tlsOpts,
		Port:    webhookPort,
	})

	// Setup metrics server
	var metricsOptions metricsserver.Options
	if metricsAddr == "0" {
		log.Info("metrics server disabled")
		metricsOptions = metricsserver.Options{
			BindAddress: "0", // Disable metrics
		}
	} else {
		metricsOptions = metricsserver.Options{
			BindAddress:   metricsAddr,
			SecureServing: secureMetrics,
			TLSOpts:       tlsOpts,
		}
		if secureMetrics {
			// FilterProvider is used to protect the metrics endpoint with authn/authz.
			// These configurations ensure that only authorized users and service accounts
			// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
			// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.1/pkg/metrics/filters#WithAuthenticationAndAuthorization
			metricsOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
		}
	}

	// Set API client config
	restConfig := ctrl.GetConfigOrDie()
	restConfig.QPS = float32(apiQPS)
	restConfig.Burst = apiBurst

	// Setup controller manager
	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                     scheme,
		Metrics:                    metricsOptions,
		WebhookServer:              webhookServer,
		HealthProbeBindAddress:     probeAddr,
		LeaderElection:             enableLeaderElection,
		LeaderElectionID:           leaderElectionID, // Required for cert rotation.
		LeaderElectionResourceLock: "leases",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		logAndExit(err, "unable to start manager")
	}

	// Setup webhook certificates
	webhooks := []rotator.WebhookInfo{}
	if enforceEphemeralWebhookConfig != "" {
		webhooks = append(webhooks, rotator.WebhookInfo{
			Name: enforceEphemeralWebhookConfig,
			Type: rotator.Validating,
		})
	}
	if hyperconvergedWebhookConfig != "" {
		webhooks = append(webhooks, rotator.WebhookInfo{
			Name: hyperconvergedWebhookConfig,
			Type: rotator.Mutating,
		})
	}

	certSetupFinished := make(chan struct{})
	log.Info("setting up cert rotation")
	if err := rotator.AddRotator(mgr, &rotator.CertRotator{
		SecretKey: types.NamespacedName{
			Namespace: namespace,
			Name:      certSecretName,
		},
		CertDir:        "/tmp/k8s-webhook-server/serving-certs",
		CAName:         "local-csi-ca",
		CAOrganization: "Local CSI Driver",
		DNSName:        fmt.Sprintf("%s.%s.svc", webhookSvcName, namespace),
		ExtraDNSNames: []string{
			fmt.Sprintf("%s.%s.svc.cluster.local", webhookSvcName, namespace),
			fmt.Sprintf("%s.%s", webhookSvcName, namespace),
		},
		Webhooks:             webhooks,
		IsReady:              certSetupFinished,
		EnableReadinessCheck: true,
	}); err != nil {
		logAndExit(err, "unable to set up cert rotation")
	}

	// Setup health checks
	checker := func(req *http.Request) error {
		select {
		case <-certSetupFinished:
			return mgr.GetWebhookServer().StartedChecker()(req)
		default:
			return fmt.Errorf("certs are not ready yet")
		}
	}

	if err := mgr.AddHealthzCheck("healthz", checker); err != nil {
		logAndExit(err, "unable to set up health check")
	}
	if err := mgr.AddReadyzCheck("readyz", checker); err != nil {
		logAndExit(err, "unable to set up ready check")
	}

	recorder := events.NewNoopRecorder()
	if eventRecorderEnabled {
		log.Info("event recorder enabled")
		recorder = mgr.GetEventRecorderFor("local-csi-webhook")
	}

	// Register webhooks once certificates are ready
	go func() {
		<-certSetupFinished

		// Register enforce ephemeral webhook
		if enforceEphemeralWebhookConfig != "" {
			log.Info("registering enforce ephemeral webhook")
			enforceEphemeralHandler, err := enforceEphemeral.NewHandler("localdisk.csi.acstor.io", mgr.GetClient(), mgr.GetScheme(), recorder)
			if err != nil {
				logAndExit(err, "unable to create enforce ephemeral handler")
			}
			mgr.GetWebhookServer().Register("/validate-pvc", &webhook.Admission{Handler: enforceEphemeralHandler})
			log.Info("enforce ephemeral webhook registered")
		}

		// Register hyperconverged webhook
		if hyperconvergedWebhookConfig != "" {
			log.Info("registering hyperconverged webhook")
			hyperconvergedHandler, err := hyperconverged.NewHandler(namespace, mgr.GetClient(), mgr.GetScheme())
			if err != nil {
				logAndExit(err, "unable to create hyperconverged handler")
			}
			mgr.GetWebhookServer().Register("/mutate-pod", &webhook.Admission{Handler: hyperconvergedHandler})
			log.Info("hyperconverged webhook registered")
		}

		log.Info("all webhooks registered successfully")
	}()

	log.Info("starting webhook manager")
	if err := mgr.Start(ctx); err != nil {
		logAndExit(err, "problem running webhook manager")
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
