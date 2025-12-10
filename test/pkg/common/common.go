// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package common

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/reporters"
	"github.com/onsi/ginkgo/v2/types"
	. "github.com/onsi/gomega"

	"local-csi-driver/test/pkg/docker"
	"local-csi-driver/test/pkg/flakes"
	"local-csi-driver/test/pkg/kind"
	"local-csi-driver/test/pkg/utils"
)

const (
	// trueString is the string representation of a boolean true value.
	trueString = "true"
)

var (
	// Optional Environment Variables:
	// - SKIP_INSTALL_PROMETHEUS=true: Skips Prometheus Operator installation
	//   during test setup.
	// - SKIP_UNINSTALL=true: Skips uninstalling everything. Useful for
	//   debugging and re-running tests.
	// - CREATE_CLUSTER=true: Creates and deletes a new Kind cluster. Useful for
	//   debugging and re-running tests.
	// - SKIP_SUPPORT_BUNDLE=true: Skips collecting support bundle after the
	//   test.
	// - SKIP_BUILD=true: Skips building the manager and agent images.
	// - INSTALLATION_METHOD=helm: Installs csi driver using helm, otherwise installs
	//   using make install and make deploy.
	skipPrometheusInstall                = os.Getenv("SKIP_INSTALL_PROMETHEUS") == trueString
	skipUninstall                        = os.Getenv("SKIP_UNINSTALL") == trueString
	createCluster                        = os.Getenv("CREATE_CLUSTER") == trueString
	skipSupportBundle                    = os.Getenv("SKIP_SUPPORT_BUNDLE") == trueString
	skipBuild                            = os.Getenv("SKIP_BUILD") == trueString
	useLocalHelmCharts                   = os.Getenv("INSTALLATION_METHOD") == "localHelm"
	isPrometheusOperatorAlreadyInstalled = false

	// isKindClusterCreated will be set true when a Kind cluster is already created.
	isKindClusterCreated = false

	// Image will be built and injected into the deploy config.
	defaultDriverImage  = "csi-local-driver/driver:latest"
	defaultManagerImage = "csi-local-driver/manager:latest"
	driver_image        = os.Getenv("DRIVER_IMG")
	manager_image       = os.Getenv("MANAGER_IMG")

	startTime     = time.Now()
	filePathRegex = regexp.MustCompile(`[ /]`)
)

// Setup installs the CSI driver prerequisites and components.
func Setup(ctx context.Context, namespace string, helmArgs ...string) {
	cmd := exec.CommandContext(ctx, "az", "--version")
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to get az cli version")
	_, _ = fmt.Fprintf(GinkgoWriter, "az cli version: %s\n", output)

	cmd = exec.CommandContext(ctx, "kubectl", "version")
	output, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to get kubectl version")
	_, _ = fmt.Fprintf(GinkgoWriter, "kubectl version: %s\n", output)

	By("ensure that Prometheus is enabled")
	_ = utils.UncommentCode("config/default/kustomization.yaml", "#- ../prometheus", "#")

	By("ensure that image is set")
	if driver_image == "" {
		driver_image = defaultDriverImage
	}
	if manager_image == "" {
		manager_image = defaultManagerImage
	}

	isKindClusterCreated = kind.IsClusterCreated()

	if createCluster && isKindClusterCreated {
		By("setting the kubeconfig context to kind cluster")
		err = kind.SetKubeContext(ctx)
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to set kubeconfig context")
	}

	tasks := []func(ctx context.Context) error{}

	if !skipBuild {
		tasks = append(tasks, makeDockerImage)
		tasks = append(tasks, pullDockerImage)
		tasks = append(tasks, makeHelmChart)
	}
	if !isKindClusterCreated && createCluster {
		tasks = append(tasks, createKindCluster)
	}

	errCh := make(chan error, len(tasks))
	for _, task := range tasks {
		go func(task func(ctx context.Context) error) {
			defer GinkgoRecover()
			errCh <- task(ctx)
		}(task)
	}

	var errs []error
	for range tasks {
		if err := <-errCh; err != nil {
			errs = append(errs, err)
		}
	}
	close(errCh)

	for _, err := range errs {
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
	}
	if kind.IsCluster() {
		By("loading the image into Kind cluster")
		err = kind.LoadImageWithName(ctx, driver_image)
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to load the driver image into Kind")
		err = kind.LoadImageWithName(ctx, manager_image)
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to load the manager image into Kind")
	} else if !skipBuild {
		// if we are not a kind cluster, we push the images to the registry
		// instead of loading them into the Kind cluster
		By("pushing the image to the registry")
		err = docker.PushImage(ctx, driver_image)
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to push the driver image to the registry")
		err = docker.PushImage(ctx, manager_image)
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to push the manager image to the registry")

		if !useLocalHelmCharts {
			By("pushing the helm chart to the registry")
			cmd := exec.CommandContext(ctx, "make", "helm-push")
			_, err = utils.Run(cmd)
			ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to push the helm chart to the registry")
		}
	}

	// The test-e2e make target is intended to run on a temporary cluster that
	// is created and destroyed for testing. To prevent errors when tests run in
	// environments with Prometheus or CertManager already installed, we check
	// for their presence before execution.
	//
	// Setup Prometheus and CertManager before the suite if not skipped and if
	// not already installed.
	if !skipPrometheusInstall {
		By("checking if prometheus is installed already")
		isPrometheusOperatorAlreadyInstalled = utils.IsPrometheusCRDsInstalled(ctx)
		if !isPrometheusOperatorAlreadyInstalled {
			_, _ = fmt.Fprintf(GinkgoWriter, "Installing Prometheus Operator...\n")
			Eventually(func(g Gomega, ctx context.Context) {
				g.Expect(utils.InstallPrometheusOperator(ctx)).To(Succeed(), "Failed to install Prometheus Operator")
			}).WithTimeout(5*time.Minute).WithContext(ctx).Should(Succeed(), "Failed to install Prometheus Operator")
		} else {
			_, _ = fmt.Fprintf(GinkgoWriter, "WARNING: Prometheus Operator is already installed. Skipping installation...\n")
		}
	}

	By("creating namespace")
	Eventually(func(g Gomega) {
		cmd = exec.CommandContext(ctx, "kubectl", "get", "ns", namespace)
		if _, err := utils.Run(cmd); err != nil {
			cmd := exec.CommandContext(ctx, "kubectl", "create", "ns", namespace)
			_, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")
		}
	}).WithContext(ctx).Should(Succeed(), "Namespace creation failed or namespace not found")

	if useLocalHelmCharts {
		By("installing csi driver with local helm charts")
		args := []string{"deploy"}
		args = append(args, helmArgs...)
		cmd = exec.CommandContext(ctx, "make", args...)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install csi driver with local helm charts")
	} else {
		By("installing csi driver with helm")
		Eventually(func(g Gomega, ctx context.Context) {
			args := []string{"helm-install"}
			args = append(args, helmArgs...)
			cmd = exec.CommandContext(ctx, "make", args...)
			_, err = utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred(), "Failed to install csi driver with helm")
		}).WithTimeout(5*time.Minute).WithContext(ctx).Should(Succeed(), "Failed to install csi driver with helm")
	}

	By("validating that components are running as expected")
	VerifyDriverUp(ctx, namespace)
}

// Teardown undeploys the csi driver components and cleans up the cluster.
func Teardown(ctx context.Context, namespace string, supportBundleDir string) {

	if !skipSupportBundle {
		By("collecting support bundle")
		supportBundlePath := filepath.Join(supportBundleDir, "suite.tar.gz")
		Eventually(func(g Gomega, ctx context.Context) {
			output, err := utils.CollectSupportBundle(ctx, supportBundlePath, startTime)
			g.Expect(err).NotTo(HaveOccurred(), "Failed to collect support bundle: %s\n", output)
		}).WithContext(ctx).Should(Succeed(), "Failed to collect support bundle")
	}

	if skipUninstall {
		_, _ = fmt.Fprintf(GinkgoWriter, "Skipping uninstall...\n")
		return
	}

	By("uninstalling csi driver with helm")
	Eventually(func(g Gomega, ctx context.Context) {
		cmd := exec.CommandContext(ctx, "make", "uninstall-helm")
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "Failed to uninstall csi driver with helm")
	}).WithTimeout(5*time.Minute).WithContext(ctx).Should(Succeed(), "Failed to uninstall csi driver with helm")

	if namespace != "kube-system" {
		By("removing namespace")
		Eventually(func(g Gomega, ctx context.Context) {
			cmd := exec.CommandContext(ctx, "kubectl", "delete", "ns", namespace, "--ignore-not-found", "--wait")
			_, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred(), "Failed to delete namespace")
		}).WithContext(ctx).Should(Succeed(), "Namespace deletion failed")
	}

	// Teardown Prometheus after the suite if not skipped and if it was not
	// already installed.
	if !skipPrometheusInstall && !isPrometheusOperatorAlreadyInstalled {
		_, _ = fmt.Fprintf(GinkgoWriter, "Uninstalling Prometheus Operator...\n")
		utils.UninstallPrometheusOperator(ctx)
	}

	if createCluster && !isKindClusterCreated {
		_, _ = fmt.Fprintf(GinkgoWriter, "Deleting kind cluster...\n")
		cmd := exec.CommandContext(ctx, "make", "clean")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete kind cluster")
	}
}

// PostReport generates the junit report for the test and copies it to the
// specified path. It also patches the report to skip flaky tests.
func PostReport(ctx SpecContext, r Report, testReport string) {
	suiteConfig, _ := GinkgoConfiguration()
	if suiteConfig.DryRun {
		_, _ = fmt.Fprintf(GinkgoWriter, "Skipping report generation as this is a dry run\n")
		return
	}
	By("generating test report")
	Expect(testReport).NotTo(BeEmpty(), "test report path is empty")
	reportPath, err := getArtifactPath(testReport)
	Expect(err).NotTo(HaveOccurred(), "failed to get absolute path for test report")

	config := reporters.JunitReportConfig{
		OmitTimelinesForSpecState: types.SpecStateSkipped | types.SpecStatePending,
		OmitLeafNodeType:          true,
	}

	// Patch the report to skip flakey tests
	r = flakes.PatchReport(r)

	By("generating path to absolute path: " + reportPath)
	Expect(reporters.GenerateJUnitReportWithConfig(r, reportPath, config)).
		To(Succeed(), "fail to generate junit report")
}

func getArtifactPath(path string) (string, error) {
	reportPath := path
	if filepath.IsAbs(path) {
		return reportPath, nil
	}
	projectDir, err := utils.GetProjectDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(projectDir, path), nil
}

// createKindCluster creates a new kind cluster.
func createKindCluster(ctx context.Context) error {
	By("creating kind cluster")
	start := time.Now()
	defer func() {
		_, _ = fmt.Fprintf(GinkgoWriter, "Kind cluster created in %v\n", time.Since(start))
	}()
	cmd := exec.CommandContext(ctx, "make", "single")
	_, err := utils.Run(cmd)
	if err != nil {
		return fmt.Errorf("failed to create kind cluster: %v", err)
	}
	return nil
}

// makeDockerImage builds the docker image.
func makeDockerImage(ctx context.Context) error {
	By("building the controller image")
	start := time.Now()
	defer func() {
		_, _ = fmt.Fprintf(GinkgoWriter, "docker image built in %v\n", time.Since(start))
	}()
	cmd := exec.CommandContext(ctx, "make", "docker-build")
	_, err := utils.Run(cmd)
	if err != nil {
		return fmt.Errorf("failed to build the docker image: %v", err)
	}
	return nil
}

// pullDockerImage pulls the docker image.
//
// Pulling the image is needed since buildx pushes the image to the registry,
// but if the image is not present locally it will fail to load the image.
func pullDockerImage(ctx context.Context) error {
	By("pulling the controller image")
	start := time.Now()
	defer func() {
		_, _ = fmt.Fprintf(GinkgoWriter, "docker image pulled in %v\n", time.Since(start))
	}()
	cmd := exec.CommandContext(ctx, "make", "docker-pull")
	_, err := utils.Run(cmd)
	if err != nil {
		return fmt.Errorf("failed to pull the docker image: %v", err)
	}
	return nil
}

// makeHelmChart builds the helm chart.
func makeHelmChart(ctx context.Context) error {
	By("building the helm chart")
	start := time.Now()
	defer func() {
		_, _ = fmt.Fprintf(GinkgoWriter, "helm chart built in %v\n", time.Since(start))
	}()
	cmd := exec.CommandContext(ctx, "make", "helm-build")
	_, err := utils.Run(cmd)
	if err != nil {
		return fmt.Errorf("failed to build the helm chart: %v", err)
	}
	return nil
}

// CollectSupportBundle collects the support bundle for the test if the test
// failed and support bundle collection is not skipped.
func CollectSupportBundle(ctx context.Context, supportBundleDir string) {
	if CurrentSpecReport().Failed() && !skipSupportBundle {
		By("Collecting support bundle for the test")
		testFilePath := filePathRegex.ReplaceAllString(CurrentSpecReport().FullText(), "_") + ".tar.gz"
		supportBundlePath := filepath.Join(supportBundleDir, testFilePath)
		_, _ = fmt.Fprintf(GinkgoWriter, "Collecting support bundle for to %s\n", supportBundlePath)
		Eventually(func(g Gomega, ctx context.Context) {
			output, err := utils.CollectSupportBundle(ctx, supportBundlePath, CurrentSpecReport().StartTime)
			g.Expect(err).NotTo(HaveOccurred(), "Failed to collect support bundle: %s", output)
		}).WithContext(ctx).Should(Succeed(), "Failed to collect support bundle")
	}
}

// CollectSuiteSupportBundle collects the support bundle for the suite if the
// support bundle collection is not skipped.
func CollectSuiteSupportBundle(ctx context.Context, supportBundleDir string) {
	if !skipSupportBundle {
		By("Collecting support bundle for the suite")
		supportBundlePath := filepath.Join(supportBundleDir, "suite.tar.gz")

		_, _ = fmt.Fprintf(GinkgoWriter, "Collecting support bundle for suite to %s\n", supportBundlePath)
		Eventually(func(g Gomega, ctx context.Context) {
			output, err := utils.CollectSupportBundle(ctx, supportBundlePath, startTime)
			g.Expect(err).NotTo(HaveOccurred(), "Failed to collect support bundle: %s", output)
		}).WithContext(ctx).Should(Succeed(), "Failed to collect support bundle")
	}
}

// VerifyDriverUp validates that the node pod is running as expected.
func VerifyDriverUp(ctx context.Context, namespace string) {
	By("validating that the node pod is running as expected")
	verifyNodeUp := func(g Gomega) {
		// Get the name of the node pod.
		cmd := exec.CommandContext(ctx, "kubectl", "get",
			"daemonset", "-l", "app=csi-local",
			"-o", "go-template={{ range .items }}"+
				"{{ if not .metadata.deletionTimestamp }}"+
				"{{ .metadata.name }}"+
				"{{ \"\\n\" }}{{ end }}{{ end }}",
			"-n", namespace,
		)

		daemonsetOutput, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve node pod information")

		daemonsets := utils.GetNonEmptyLines(daemonsetOutput)
		g.Expect(daemonsets).To(HaveLen(1), "Expected exactly one daemonset, got %d", len(daemonsets))

		daemonsetName := daemonsets[0]
		g.Expect(daemonsetName).To(ContainSubstring("csi-local-node"), "Daemonset name does not match expected pattern")

		cmd = exec.CommandContext(ctx, "kubectl", "get",
			"daemonset", daemonsetName,
			"-o", "jsonpath={.status.numberReady}/{.status.desiredNumberScheduled}",
			"-n", namespace,
		)
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "Failed to get daemonset ready status")
		g.Expect(output).NotTo(BeEmpty(), "Empty output when checking daemonset readiness")

		// Split and verify all pods are ready
		readyCount := strings.Split(output, "/")
		g.Expect(readyCount).To(HaveLen(2), "Unexpected format in daemonset ready count")
		g.Expect(readyCount[0]).To(Equal(readyCount[1]), fmt.Sprintf("Not all daemonset pods are ready: %s/%s", readyCount[0], readyCount[1]))

		// Verify that webhooks are available.
		cmd = exec.CommandContext(ctx, "kubectl", "get", "endpoints", "csi-local-webhook-service",
			"-n", namespace, "-o", "jsonpath={.subsets[*].addresses[*].ip}")
		output, err = utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve webhook service endpoints")
		g.Expect(output).NotTo(BeEmpty(), "Webhook service endpoints are not available")
	}
	Eventually(verifyNodeUp).WithTimeout(5 * time.Minute).WithContext(ctx).Should(Succeed())
}
