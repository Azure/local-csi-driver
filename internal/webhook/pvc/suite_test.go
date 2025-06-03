// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package pvc

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/scheme"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	kscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	// +kubebuilder:scaffold:imports
)

var (
	cfg          *rest.Config
	testEnv      *envtest.Environment
	k8sClient    client.Client
	k8sClientSet kubernetes.Clientset
	recorder     *record.FakeRecorder
	ctx          context.Context
	cancel       context.CancelFunc
	pvcHandler   *Handler
)

const (
	Namespace  = "default"
	DriverName = "test-driver"
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

func TestPvcWebhook(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "PVC Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	ctx, cancel = context.WithCancel(context.TODO())

	By("bootstrapping test environment")
	var err error
	err = kscheme.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	testEnv = &envtest.Environment{
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			Paths: []string{filepath.Join("..", "..", "..", "config", "webhook", "pvc")},
		},
	}

	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	k8sClientSet = *kubernetes.NewForConfigOrDie(cfg)

	// create the release namespace
	err = k8sClient.Get(ctx, client.ObjectKey{Name: Namespace}, &corev1.Namespace{})
	if errors.IsNotFound(err) {
		ns := corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: Namespace,
			},
		}
		Expect(k8sClient.Create(ctx, &ns)).To(Succeed())
	} else {
		Expect(err).NotTo(HaveOccurred())
	}

	// Configure manager with webhooks.
	webhookInstallOptions := &testEnv.WebhookInstallOptions
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
		Metrics: server.Options{
			BindAddress: "0",
		},
		WebhookServer: webhook.NewServer(webhook.Options{
			Port:    webhookInstallOptions.LocalServingPort,
			CertDir: webhookInstallOptions.LocalServingCertDir,
			Host:    webhookInstallOptions.LocalServingHost,
		}),
		LeaderElection: false,
	})
	Expect(err).NotTo(HaveOccurred(), "failed to create manager")

	// Controller setup using fake clients.
	recorder = record.NewFakeRecorder(10)

	pvcHandler, err = NewHandler(DriverName, mgr.GetClient(), mgr.GetScheme(), recorder)
	Expect(err).NotTo(HaveOccurred(), "failed to create pvc controller")

	mgr.GetWebhookServer().Register("/pvc-create", &webhook.Admission{Handler: pvcHandler})

	go func() {
		defer GinkgoRecover()
		err := mgr.Start(ctx)
		Expect(err).NotTo(HaveOccurred(), "failed to start manager")
	}()

	// Wait for the webhook server to be ready.
	Eventually(func() error {
		return DialWebhookServer(webhookInstallOptions.LocalServingHost, webhookInstallOptions.LocalServingPort)
	}).Should(Succeed())
})

var _ = AfterSuite(func() {
	cancel()
	By("tearing down the test environment")
	Expect(testEnv.Stop()).To(Succeed())
})

func DialWebhookServer(host string, port int) error {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	addrPort := fmt.Sprintf("%s:%d", host, port)

	conn, err := tls.DialWithDialer(dialer, "tcp", addrPort, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		return err
	}
	return conn.Close()
}
