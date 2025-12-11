// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sanity

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/tools/clientcmd"

	"local-csi-driver/test/pkg/common"
	"local-csi-driver/test/pkg/utils"
)

const (
	// namespace is the namespace where the local-csi-driver components are
	// deployed.
	namespace = "kube-system"
	// controllerDs is the resource name used in kubectl commands for the node.
	controllerDs = "daemonsets/csi-local-node"

	// helmArgs is the environment variable used to pass extra arguments to
	// helm during installation of the csi driver. We need to disable cleanup,
	// PV garbage collection, and LVM orphan cleanup for the sanity tests.
	helmArgs = "HELM_ARGS=--set cleanup.lvGarbageCollection.enabled=false --set cleanup.lvmOrphanCleanup.enabled=false"
)

var (
	// defaultKubeConfigPath is the default path to the kubeconfig file.
	defaultKubeConfigPath = filepath.Join(os.Getenv("HOME"), ".kube", "config")

	// socatPatch is the path to the socat patch file.
	socatPatch = filepath.Join("test", "sanity", "fixtures", "socat-patch.yaml")

	// junitReport is the report file generated for consumption by test framework.
	junitReport = flag.String("junit-report", "junit.xml", "Path to the junit report")

	// supportBundleDir is the directory where support bundles are written.
	supportBundleDir = flag.String("support-bundle-dir", "support-bundles", "Path to write support-bundles")

	// skipSocatRollback is a flag to skip the rollback of the socat patch.
	// This is useful for debugging the socat patch in the test so we keep the
	// logs from the patched images around for support-bundle collection.
	skipSocatRollback = os.Getenv("SKIP_SOCAT_ROLLBACK") == "true"
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
	// Register the tests.
	os.Exit(m.Run())
}

// TestCSISanity runs the csi sanity tests.
//
// To run the tests, we need access to the socket of the csi-node and
// csi-controller plugins. To access this socket file, we need to port-forward
// the csi-node and csi-controller plugins. Additionally, we patch both the
// controller and node containers to use socat to expose the socket.
func TestCSISanity(t *testing.T) {
	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)
	SetDefaultConsistentlyDuration(5 * time.Second)
	EnforceDefaultTimeoutsWhenUsingContexts()
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting csi driver sanity test suite\n")
	RunSpecs(t, "csi sanity suite")
}

var _ = SynchronizedBeforeSuite(func(ctx context.Context) {
	By("Installing csi driver and other required components")
	common.Setup(ctx, namespace, helmArgs)
	DeferCleanup(func(ctx context.Context) {
		common.Teardown(ctx, namespace, *supportBundleDir)
	})

	By("Applying socat patch to node and controller")
	for _, component := range []string{controllerDs} {
		cmd := exec.Command("kubectl", "patch", component, "-n", namespace, "--patch-file", socatPatch)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to apply socat patch to "+component)

		DeferCleanup(func() {
			if skipSocatRollback {
				_, err = fmt.Fprintf(GinkgoWriter, "Skipping socat rollback for %s\n", component)
				Expect(err).NotTo(HaveOccurred(), "Failed to write to writer")
				return
			}
			By("Removing socat patch from " + component)
			cmd = exec.Command("kubectl", "rollout", "undo", component, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to rollout undo for "+component)

			By("Waiting for " + component + " to rollout the undo")
			cmd = exec.Command("kubectl", "rollout", "status", component, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to rollout status for "+component)

		})

		By("Waiting for " + component + " to rollout")
		cmd = exec.Command("kubectl", "rollout", "status", component, "-n", namespace)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to rollout status for "+component)
	}

	for _, t := range TestConfigs {
		if t.MatchesLabelFilter() {
			t.SetupPortForwarding(ctx)
		}
	}

}, func() {
	_, _ = fmt.Fprintf(GinkgoWriter, "Finished installing csi driver and other required components\n")
})

var _ = AfterEach(func(ctx context.Context) {
	common.CollectSupportBundle(ctx, *supportBundleDir)
})

var _ = ReportAfterSuite("e2e reporter", func(ctx SpecContext, r Report) {
	common.CollectSuiteSupportBundle(ctx, *supportBundleDir)
	common.PostReport(ctx, r, *junitReport)
})

var _ = Describe("CSI sanity", Label("sanity"), func() {
	for _, testConfig := range TestConfigs {
		if testConfig.MatchesLabelFilter() {
			testConfig.RegisterTests()
		}
	}
})
