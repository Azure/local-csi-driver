// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package external

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/kubernetes/test/e2e/framework/testfiles"
	"k8s.io/kubernetes/test/e2e/storage/external"
	storageframework "k8s.io/kubernetes/test/e2e/storage/framework"
	"k8s.io/kubernetes/test/e2e/storage/testsuites"
	e2etestingmanifests "k8s.io/kubernetes/test/e2e/testing-manifests"

	"local-csi-driver/test/pkg/common"
	"local-csi-driver/test/pkg/utils"
)

var (
	// pvcAnnotator is the path to the PVC annotator to add accept-ephemeral-storage annotation.
	pvcAnnotator = path.Join("test", "external", "fixtures", "pvc_annotator.yaml")
)

func init() {
	// Add testsuite for xfs on generic ephemeral volumes. By default, this isn't included
	testsuites.CSISuites = append(testsuites.CSISuites, func() storageframework.TestSuite {
		return testsuites.InitCustomEphemeralTestSuite(genericEphemeralXfsTestPatterns())
	})
	// Add testsuite for capacity tests on generic ephemeral volumes. By default, this isn't included
	testsuites.CSISuites = append(testsuites.CSISuites, func() storageframework.TestSuite {
		return testsuites.InitCustomCapacityTestSuite(genericEphemeralXfsTestPatterns())
	})
}

// testConfig is a struct that holds the driver and storagepool paths.
type testConfig struct {
	// driverPath is the path to the driver yaml file
	driverPath string
	// labels are the labels to apply to the test
	labels Labels
	// additionalArgs are additional arguments to pass to the test
	additionalArgs []any
	// testSuiteName is the name of the test suite
	testSuiteName string
	// supportTestPattern is the test pattern that the driver supports
	supportTestPattern string
	// skipTestPatterns is a list of test patterns to skip
	skipTestPatterns []string
	// setupTest is a function to run before the test
	setup func(ctx context.Context)
}

// testConfigs is a list of test configurations to run.
var testConfigs = []testConfig{
	{
		driverPath:         path.Join("test", "external", "drivers", "lvm.yaml"),
		labels:             Label("aks", "lvm", "external-e2e"),
		testSuiteName:      "lvm External E2E Test Suite",
		supportTestPattern: "Generic Ephemeral-volume",
	},
	{
		driverPath:    path.Join("test", "external", "drivers", "lvm.yaml"),
		labels:        Label("aks", "lvm-annotation", "external-e2e"),
		testSuiteName: "lvm-annotation External E2E Test Suite",
		setup: func(ctx context.Context) {
			By("setting up installing kyverno")
			Eventually(func(g Gomega, ctx context.Context) {
				cmd := exec.CommandContext(ctx, "make", "kyverno")
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "failed to install kyverno")
			}).WithTimeout(5*time.Minute).WithContext(ctx).Should(Succeed(), "failed to install kyverno")

			DeferCleanup(func(ctx context.Context) {
				By("cleaning up kyverno")
				Eventually(func(g Gomega, ctx context.Context) {
					cmd := exec.CommandContext(ctx, "make", "kyverno-uninstall")
					_, err := utils.Run(cmd)
					g.Expect(err).NotTo(HaveOccurred(), "failed to uninstall kyverno")
				}).WithTimeout(5*time.Minute).WithContext(ctx).Should(Succeed(), "failed to uninstall kyverno")
			})

			By("setting up installing kyverno policies")
			Eventually(func(g Gomega, ctx context.Context) {
				cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", pvcAnnotator)
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "failed to install kyverno policies")
			}).WithContext(ctx).Should(Succeed(), "failed to install kyverno policies")

			DeferCleanup(func(ctx context.Context) {
				By("cleaning up kyverno policies")
				Eventually(func(g Gomega, ctx context.Context) {
					cmd := exec.CommandContext(ctx, "kubectl", "delete", "-f", pvcAnnotator, "--ignore-not-found", "--wait")
					_, err := utils.Run(cmd)
					g.Expect(err).NotTo(HaveOccurred(), "failed to cleanup kyverno policies")
				}).WithContext(ctx).Should(Succeed(), "failed to cleanup kyverno policies")
			})
		},
	},
}

// setupTest ensures the cluster is ready for testing.
func (t *testConfig) setupTest(ctx context.Context) {
	By("validating that components are running as expected")
	common.VerifyDriverUp(ctx, namespace)
	if t.setup != nil {
		t.setup(ctx)
	}
}

func (t *testConfig) registerTests() {
	parentDir, err := utils.GetProjectDir()
	if err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get project directory: %v\n", err)
		os.Exit(1)
	}
	absDriverPath := path.Join(parentDir, t.driverPath)

	var _ = Context(t.testSuiteName, t.labels, t.additionalArgs, func() {
		BeforeEach(func(ctx context.Context) {
			t.skipUnsupportedTest()
		})

		AfterEach(func(ctx context.Context) {
			common.CollectSupportBundle(ctx, *supportBundleDir)
		})

		rootFileSource := testfiles.RootFileSource{Root: parentDir}
		testfiles.AddFileSource(rootFileSource)
		testfiles.AddFileSource(e2etestingmanifests.GetE2ETestingManifestsFS())

		if err := external.AddDriverDefinition(absDriverPath); err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "Failed to add driver definition: %v\n", err)
			os.Exit(1)
		}
	})
}

func (t *testConfig) skipUnsupportedTest() {
	for _, skipPattern := range t.skipTestPatterns {
		if strings.Contains(CurrentSpecReport().FullText(), skipPattern) {
			Skip(fmt.Sprintf("Test pattern %s is not supported by driver %s", skipPattern, t.driverPath))
		}
	}
	if !strings.Contains(CurrentSpecReport().FullText(), t.supportTestPattern) {
		Skip(fmt.Sprintf("Test pattern %s is only supported by driver %s", t.supportTestPattern, t.driverPath))
	}
}

func (t *testConfig) matchesLabelFilter() bool {
	return t.labels.MatchesLabelFilter(GinkgoLabelFilter())
}

// genericEphemeralXfsTestPatterns returns a list of test patterns for xfs on generic ephemeral volumes.
func genericEphemeralXfsTestPatterns() []storageframework.TestPattern {
	genericLateBinding := storageframework.XfsGenericEphemeralVolume
	genericLateBinding.Name += " (late-binding)"
	genericLateBinding.BindingMode = storagev1.VolumeBindingWaitForFirstConsumer
	genericLateBinding.AllowExpansion = true

	genericImmediateBinding := storageframework.XfsGenericEphemeralVolume
	genericImmediateBinding.Name += " (immediate-binding)"
	genericImmediateBinding.BindingMode = storagev1.VolumeBindingImmediate
	genericImmediateBinding.AllowExpansion = true

	return []storageframework.TestPattern{
		genericLateBinding,
		genericImmediateBinding,
	}
}
