// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package hyperconverged

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

const (
	namespace = "test-namespace"
)

var (
	cfg          *rest.Config
	testEnv      *envtest.Environment
	k8sClient    client.Client
	k8sClientSet kubernetes.Clientset
	ctx          context.Context
	cancel       context.CancelFunc
)

var config = `
apiVersion: admissionregistration.k8s.io/v1
kind: MutatingWebhookConfiguration
metadata:
  name: hyperconverged-webhook
webhooks:
- admissionReviewVersions:
  - v1
  clientConfig:
    service:
      name: webhook-service
      namespace: kube-system
      path: /mutate-pod
  failurePolicy: Fail # Set to fail for tests.
  name: hyperconverged.localdisk.csi.acstor.io
  rules:
  - apiGroups:
    - ""
    apiVersions:
    - v1
    operations:
    - CREATE
    resources:
    - pods
  sideEffects: None
`

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

func TestWebhook(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Hyperconverged Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	ctx, cancel = context.WithCancel(context.TODO())

	var err error

	By("writing webhook configuration to file")
	cfgDir, err := os.MkdirTemp(os.TempDir(), "hyperconverged")
	Expect(err).NotTo(HaveOccurred())
	defer func() {
		Expect(os.RemoveAll(cfgDir)).To(Succeed(), "failed to remove temporary directory %s", cfgDir)
	}()
	err = os.WriteFile(filepath.Join(cfgDir, "webhook.yaml"), []byte(config), 0644)
	Expect(err).NotTo(HaveOccurred())

	By("bootstrapping test environment")
	err = kscheme.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	testEnv = &envtest.Environment{
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			Paths: []string{cfgDir},
		},
		AttachControlPlaneOutput: false,
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
	err = k8sClient.Get(ctx, client.ObjectKey{Name: namespace}, &corev1.Namespace{})
	if errors.IsNotFound(err) {
		ns := corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}
		Expect(k8sClient.Create(ctx, &ns)).To(Succeed())
	} else {
		Expect(err).NotTo(HaveOccurred())
	}

	// create the test namespace
	testNamespace := GenNamespace("test")
	err = k8sClient.Get(ctx, client.ObjectKey{Name: testNamespace.Name}, &corev1.Namespace{})
	if errors.IsNotFound(err) {
		Expect(k8sClient.Create(ctx, testNamespace)).To(Succeed())
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

	hyperconvergedHandler, err := NewHandler(namespace, mgr.GetClient(), mgr.GetScheme())
	Expect(err).NotTo(HaveOccurred(), "failed to create hyperconverged controller")

	mgr.GetWebhookServer().Register("/mutate-pod", &webhook.Admission{Handler: hyperconvergedHandler})

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

// DialWebhookServer dials the webhook server to check if it is ready.
func DialWebhookServer(host string, port int) error {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	addrPort := fmt.Sprintf("%s:%d", host, port)

	conn, err := tls.DialWithDialer(dialer, "tcp", addrPort, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		return err
	}
	if err := conn.Close(); err != nil {
		return err
	}
	return nil
}
