// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package e2e

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/tools/clientcmd"

	_ "local-csi-driver/test/aks"
	"local-csi-driver/test/pkg/common"
	_ "local-csi-driver/test/scale"
)

const namespace = "kube-system"

var (
	// defaultKubeConfigPath is the default path to the kubeconfig file.
	defaultKubeConfigPath = filepath.Join(os.Getenv("HOME"), ".kube", "config")
	// junitReport is the report file generated for consumption by test.
	junitReport = flag.String("junit-report", "junit.xml", "Path to the test report")

	// supportBundleDir is the directory where support bundles are written.
	supportBundleDir = flag.String("support-bundle-dir", "support-bundles", "Path to write support-bundles")

	// helmPrefix is the helm prefix used by helm when creating k8s resources.
	helmPrefix = flag.String("helm-prefix", "csi-local", "Prefix used by helm for k8s resources")
)

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

// TestE2E runs the end-to-end (e2e) test suite for the project. These tests
// execute in an isolated, temporary environment to validate project changes
// with the the purposed to be used in CI jobs. The default setup requires Kind,
// builds/loads the Manager Docker image locally, and installs CertManager and
// Prometheus.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting integration test suite\n")
	RunSpecs(t, "e2e suite")
}

var _ = BeforeSuite(func(ctx context.Context) {
	common.Setup(ctx, namespace)
})

var _ = AfterSuite(func(ctx context.Context) {
	common.Teardown(ctx, namespace, *supportBundleDir)
})

var _ = AfterEach(func(ctx context.Context) {
	common.CollectSupportBundle(ctx, *supportBundleDir)
})

var _ = ReportAfterSuite("e2e reporter", func(ctx SpecContext, r Report) {
	common.CollectSuiteSupportBundle(ctx, *supportBundleDir)
	common.PostReport(ctx, r, *junitReport)
})
