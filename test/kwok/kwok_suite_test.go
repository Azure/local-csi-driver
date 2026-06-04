// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package kwok

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	_ "local-csi-driver/test/aks"
	"local-csi-driver/test/pkg/common"
	"local-csi-driver/test/pkg/kwok"
	_ "local-csi-driver/test/scale"
)

const (
	namespace   = "kube-system"
	clientQPS   = 1000
	clientBurst = 2000
)

var (
	// defaultKubeConfigPath is the default path to the kubeconfig file.
	defaultKubeConfigPath = filepath.Join(os.Getenv("HOME"), ".kube", "config")
	// junitReport is the report file generated for consumption by test.
	junitReport = flag.String("junit-report", "junit.xml", "Path to the test report")

	// supportBundleDir is the directory where support bundles are written.
	supportBundleDir = flag.String("support-bundle-dir", "support-bundles", "Path to write support-bundles")

	// skipCleanup skips the per-resource cleanup of nodes/pods/pvs/pvcs at
	// the end of the test. Use in CI where the cluster is torn down anyway.
	// Set SKIP_UNINSTALL=true to also skip uninstalling kwok and the csi
	// driver.
	skipCleanup = flag.Bool("skip-cleanup", false, "Skip per-resource cleanup of kwok nodes/pods/pvs/pvcs")

	// scheme is the runtime scheme shared across the suite.
	scheme = runtime.NewScheme()

	// k8sClient is the controller-runtime client shared across specs.
	k8sClient client.Client
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
}

func TestMain(m *testing.M) {
	if os.Getenv(clientcmd.RecommendedConfigPathEnvVar) == "" {
		err := os.Setenv(clientcmd.RecommendedConfigPathEnvVar, defaultKubeConfigPath)
		if err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "Failed to set default kubeconfig path: %v\n", err)
			os.Exit(1)
		}
	}
	flag.Parse()
	os.Exit(m.Run())
}

func TestKwok(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting integration test suite\n")
	RunSpecs(t, "kwok suite")
}

var _ = BeforeSuite(func(ctx context.Context) {
	kubeconfig := os.Getenv(clientcmd.RecommendedConfigPathEnvVar)
	Expect(kubeconfig).NotTo(BeEmpty(), "KUBECONFIG env var must be set")
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	Expect(err).NotTo(HaveOccurred(), "building rest config")
	cfg.QPS = clientQPS
	cfg.Burst = clientBurst

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred(), "building client")

	By("installing csi driver with kwok-specific helm overrides")
	merged := "HELM_ARGS=" + strings.TrimSpace(os.Getenv("HELM_ARGS")+" -f test/kwok/values-kwok.yaml")
	common.Setup(ctx, namespace, merged)

	Eventually(func() error {
		return kwok.Install(ctx)
	}, "5m", "10s").Should(Succeed())
})

var _ = AfterSuite(func(ctx context.Context) {
	if !*skipCleanup {
		By("waiting for fake pods to drain")
		waitEmpty(ctx, &corev1.PodList{}, client.MatchingLabels{"type": "kwok"})

		By("waiting for fake nodes to drain")
		waitEmpty(ctx, &corev1.NodeList{}, client.MatchingLabels{"type": "kwok"})
	} else {
		_, _ = fmt.Fprintf(GinkgoWriter, "skip-cleanup: leaving kwok nodes/pods/pvs/pvcs in place\n")
	}

	if os.Getenv("SKIP_UNINSTALL") != "true" {
		Eventually(func() error {
			return kwok.Uninstall(ctx)
		}, "5m", "10s").Should(Succeed())
	} else {
		_, _ = fmt.Fprintf(GinkgoWriter, "skip-uninstall: leaving kwok installed\n")
	}

	common.Teardown(ctx, namespace, *supportBundleDir)
})

// waitEmpty polls until no objects remain that match the given selector.
// Uses Limit(1) to keep memory bounded regardless of cluster size.
func waitEmpty(ctx context.Context, list client.ObjectList, opts ...client.ListOption) {
	GinkgoHelper()
	opts = append(opts, client.Limit(1))
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.List(ctx, list, opts...)).To(Succeed(), "listing %T", list)
		items, err := apimeta.ExtractList(list)
		g.Expect(err).NotTo(HaveOccurred(), "extracting items from %T", list)
		g.Expect(items).To(BeEmpty(), "expected 0 items in list %T", list)
	}).WithTimeout(10*time.Minute).WithPolling(5*time.Second).Should(Succeed(), "objects of type %T still present", list)
}

var _ = ReportAfterSuite("kwok reporter", func(ctx SpecContext, r Report) {
	common.PostReport(ctx, r, *junitReport)
})
