// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package external

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/framework/config"

	"local-csi-driver/test/pkg/common"
)

const namespace = "kube-system"

var (

	// defaultKubeConfigPath is the default path to the kubeconfig file.
	defaultKubeConfigPath = filepath.Join(os.Getenv("HOME"), ".kube", "config")

	// junitReport is the report file generated for consumption by test.
	junitReport = flag.String("junit-report", "junit.xml", "Path to the test report")

	// supportBundleDir is the directory where support bundles are written.
	supportBundleDir = flag.String("support-bundle-dir", "support-bundles", "Path to write support-bundles")
)

func TestMain(m *testing.M) {
	if os.Getenv(clientcmd.RecommendedConfigPathEnvVar) == "" {
		err := os.Setenv(clientcmd.RecommendedConfigPathEnvVar, defaultKubeConfigPath)
		if err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "Failed to set default kubeconfig path: %v\n", err)
			os.Exit(1)
		}
	}
	framework.RegisterCommonFlags(config.Flags)
	framework.RegisterClusterFlags(config.Flags)
	flag.Parse()
	framework.AfterReadingAllFlags(&framework.TestContext)

	// register the tests
	os.Exit(m.Run())

}

func TestExternalE2E(t *testing.T) {
	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)
	EnforceDefaultTimeoutsWhenUsingContexts()
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting lcd integration test suite\n")
	RunSpecs(t, "external e2e suite")
}

var _ = SynchronizedBeforeSuite(func(ctx context.Context) {
	By("Installing csi driver and other required components")
	common.Setup(ctx, namespace)
	DeferCleanup(func(ctx context.Context) {
		common.Teardown(ctx, namespace)
	})

	for _, testConfig := range testConfigs {
		if testConfig.matchesLabelFilter() {
			testConfig.setupTest(ctx)
		}
	}

}, func() {
	// Nothing to do here
})

var _ = ReportAfterSuite("e2e reporter", func(ctx SpecContext, r Report) {
	common.CollectSuiteSupportBundle(ctx, *supportBundleDir)
	common.PostReport(ctx, r, *junitReport)
})

var _ = Describe("External Storage", func() {

	for _, testConfig := range testConfigs {
		if testConfig.matchesLabelFilter() {
			testConfig.registerTests()
		}
	}
})
