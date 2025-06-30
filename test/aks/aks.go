// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package aks

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/component-helpers/storage/volume"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"local-csi-driver/test/pkg/common"
	"local-csi-driver/test/pkg/filesystem"
	"local-csi-driver/test/pkg/node"
	"local-csi-driver/test/pkg/pod"
	"local-csi-driver/test/pkg/utils"
)

const (
	// aksManagedRgTag is the tag used to identify the resource group for the AKS managed cluster.
	aksManagedRgTag = "aks-managed-cluster-rg"
	// aksManagedClusterTag is the tag used to identify the AKS managed cluster.
	aksManagedClusterTag = "aks-managed-cluster-name"
)

var (
	namespace            = "kube-system"
	applicationNamespace = "workload-" + utils.RandomTag()
	tc                   testContext

	repeat = flag.Int("repeat", 1, "Number of times to repeat the test")
)

type testsuite struct {
	// name of the test suite
	name string
	// labels to be used for the test suite
	labels Labels
	// storageClassPath is the path to the storage class fixture
	storageClassPath string
	// statefulsetPath is the path to the statefulset fixture
	statefulsetPath string
}

type testContext struct {
	client.Client
	cache             cache.Cache
	managerCluster    string
	resourceGroupName string
}

var _ = Describe("aks tests", Label("aks"), Serial, func() {
	tests := []testsuite{
		{
			name:             "lvm aks tests",
			labels:           Label("lvm"),
			storageClassPath: common.LvmStorageClassFixture,
			statefulsetPath:  common.LvmStatefulSetFixture,
		},
		{
			name:             "lvm annotation aks tests",
			labels:           Label("lvm-annotation"),
			storageClassPath: common.LvmStorageClassFixture,
			statefulsetPath:  common.LvmAnnotationStatefulSetFixture,
		},
	}

	for _, test := range tests {
		test.registerTestSuite()
	}
})

// registerTestSuite registers the test suite with Ginkgo.
func (ts *testsuite) registerTestSuite() {
	Context(ts.name, ts.labels, Serial, func() {
		BeforeEach(ts.beforeEach)
		for range *repeat {
			testClusterRestart(&tc)
			testClusterUpgrade(&tc)
			testClusterScaledown(&tc)
		}
		AfterEach(ts.afterEach)
	})
}

// beforeEach sets up the test environment for each test case.
// - Setting up Kubernetes clients and caches
// - Discovering AKS cluster name and resource group
// - Deploying storage pool for the test
// - Creating a test application namespace and statefulset
// - Establishing monitors for filesystem, node, and pod health
// - Configuring cleanup handlers to restore the cluster to its original state.
func (ts *testsuite) beforeEach(ctx context.Context) {

	Expect(*repeat).To(BeNumerically(">", 0), "repeat must be greater than 0")

	scheme := runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed(), "failed to add clientgoscheme to scheme")

	kubeconfig := os.Getenv(clientcmd.RecommendedConfigPathEnvVar)
	Expect(kubeconfig).NotTo(BeEmpty(), "KUBECONFIG env var must be set")

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	Expect(err).NotTo(HaveOccurred())

	tc = testContext{}
	tc.Client, err = client.New(config, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred(), "failed to create client")

	// get node with providerid using client
	var nodeList corev1.NodeList
	Eventually(func(g Gomega, ctx context.Context) {
		g.Expect(tc.List(ctx, &nodeList)).To(Succeed(), "failed to list nodes")
	}).WithContext(ctx).Should(Succeed(), "failed to list nodes")

	Expect(err).NotTo(HaveOccurred(), "Failed to list nodes")
	Expect(nodeList.Items).NotTo(BeEmpty(), "No nodes found")
	providerID := nodeList.Items[0].Spec.ProviderID
	Expect(providerID).NotTo(BeEmpty(), "ProviderID is empty")

	parts := strings.Split(providerID, "/")
	Expect(len(parts)).To(BeNumerically(">=", 7), "Unexpected providerID format")
	nodeResourceGroup := parts[6]

	// get resource group using az cli
	cmd := exec.CommandContext(ctx, "az", "group", "show", "--name", nodeResourceGroup, "--query", "tags", "-o", "json")
	tags, err := utils.Run(cmd)
	handleAzureError(err, "failed to get resource group tags")

	// parse tags to get manager cluster and resource group name
	var tagMap map[string]string
	err = json.Unmarshal([]byte(tags), &tagMap)
	Expect(err).NotTo(HaveOccurred(), "Failed to parse tags")

	tc.managerCluster = tagMap[aksManagedClusterTag]
	tc.resourceGroupName = tagMap[aksManagedRgTag]
	Expect(tc.managerCluster).NotTo(BeEmpty(), "managerCluster tag is empty")
	Expect(tc.resourceGroupName).NotTo(BeEmpty(), "resourceGroupName tag is empty")

	tc.cache, err = cache.New(config, cache.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred(), "failed to create cache")

	Eventually(func(g Gomega, ctx context.Context) {
		cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", ts.storageClassPath)
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "Failed to apply storageclass")
	}).WithContext(ctx).Should(Succeed(), "failed to wait for storageclass to be applied")

	DeferCleanup(func(ctx context.Context) {
		Eventually(func(g Gomega, ctx context.Context) {
			var nodeList corev1.NodeList
			var pvList corev1.PersistentVolumeList
			g.Expect(tc.List(ctx, &nodeList)).To(Succeed(), "failed to list nodes")
			g.Expect(tc.List(ctx, &pvList)).To(Succeed(), "failed to list persistent volumes")
			nodes := map[string]struct{}{}
			for _, node := range nodeList.Items {
				nodes[node.Name] = struct{}{}
			}
			for _, pv := range pvList.Items {
				if !pvAffinityForExistingNode(pv.Spec.NodeAffinity, nodes) {
					_, _ = fmt.Fprintf(GinkgoWriter, "skipping pv %s because it is not bound to a node that exists: %v\n", pv.Name, pv.Spec.NodeAffinity)
					continue
				}
				provisionedBy := pv.Annotations[volume.AnnDynamicallyProvisioned]
				g.Expect(provisionedBy).NotTo(ContainSubstring("localdisk.csi.acstor.io"), "provisionedBy annotation should not contain localdisk.csi.acstor.io")
			}
		}).WithContext(ctx).Should(Succeed(), "failed to wait for persistent volumes to be empty")
		Eventually(func(g Gomega, ctx context.Context) {
			cmd := exec.CommandContext(ctx, "kubectl", "delete", "--wait", "--ignore-not-found", "-f", ts.storageClassPath)
			_, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred(), "Failed to delete storageclass")
		}).WithContext(ctx).Should(Succeed(), "failed to wait for storageclass to be deleted")
	})

	// if ns application does not exist, create it
	Eventually(func(g Gomega, ctx context.Context) {
		cmd := exec.CommandContext(ctx, "kubectl", "get", "ns", applicationNamespace)
		_, err := utils.Run(cmd)
		if err != nil {
			cmd = exec.CommandContext(ctx, "kubectl", "create", "ns", applicationNamespace)
			_, err = utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred(), "Failed to create application namespace")
		}
	}).WithContext(ctx).Should(Succeed(), "failed to ensure application namespace exists")

	Eventually(func(g Gomega, ctx context.Context) {
		cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", ts.statefulsetPath, "-n", applicationNamespace)
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "Failed to apply statefulset")
	}).WithContext(ctx).Should(Succeed(), "failed to wait for statefulset to be applied")

	DeferCleanup(func(ctx context.Context) {
		Eventually(func(g Gomega, ctx context.Context) {
			cmd := exec.CommandContext(ctx, "kubectl", "delete", "--wait", "--ignore-not-found", "-f", ts.statefulsetPath, "-n", applicationNamespace)
			_, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred(), "Failed to delete statefulset")
		}).WithContext(ctx).Should(Succeed(), "failed to wait for statefulset to be deleted")
		Eventually(func(g Gomega, ctx context.Context) {
			cmd = exec.CommandContext(ctx, "kubectl", "delete", "ns", applicationNamespace, "--wait", "--ignore-not-found")
			_, err = utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred(), "Failed to delete application namespace")
		}).WithContext(ctx).Should(Succeed(), "failed to wait for application namespace to be deleted")
	})

	Eventually(func(g Gomega, ctx context.Context) {
		cmd := exec.CommandContext(ctx, "kubectl", "rollout", "status", "--timeout=5m", "-f", ts.statefulsetPath, "-n", applicationNamespace)
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "Failed to wait for statefulset")
	}).WithContext(ctx).WithTimeout(5*time.Minute).Should(Succeed(), "failed to wait for statefulset rollout to complete")

	monitorCtx, cancelMonitors := context.WithCancel(context.Background())

	err = filesystem.AddMonitor(monitorCtx, tc.cache)
	Expect(err).NotTo(HaveOccurred(), "failed to watch filesystem")

	err = node.AddMonitor(monitorCtx, tc, tc.cache, []string{namespace, applicationNamespace})
	Expect(err).NotTo(HaveOccurred(), "failed to watch node")

	podRestartCounts, err := pod.AddMonitor(monitorCtx, tc.cache, applicationNamespace)
	Expect(err).NotTo(HaveOccurred(), "failed to watch pod")

	errCh := make(chan error)
	go func() {
		// blocks, so start in goroutine
		errCh <- tc.cache.Start(monitorCtx)
	}()

	Expect(err).NotTo(HaveOccurred(), "failed to start cache")
	Expect(tc.cache.WaitForCacheSync(ctx)).To(BeTrue(), "failed to wait for cache sync")

	DeferCleanup(func() {
		cancelMonitors()
		Expect(<-errCh).NotTo(HaveOccurred(), "failed to start cache")
		Expect(podRestartCounts).To(BeEmpty(), "pod restart counts should be empty")
	})
}

// afterEach ensures the cluster is in a consistent state after the test completes.
//
// This function performs post-test verification and cleanup:
//  1. If the test failed or was skipped, afterEach is skipped. No need to check.
//  2. Otherwise, it verifies that:
//     - All cluster nodes are in Ready condition
//     - All CNS system pods are running properly
//     - All DiskPools are ready and not degraded
//     - All application StatefulSets are running with expected replica counts
//     - All application pods can write to their mounted volumes
func (ts *testsuite) afterEach(ctx context.Context) {
	if CurrentSpecReport().Failed() || CurrentSpecReport().State == types.SpecStateSkipped {
		_, _ = fmt.Fprintf(GinkgoWriter, "skipping afterEach because the test failed\n")
		return
	}
	By("waiting for the cluster to be ready")
	Eventually(func(g Gomega, ctx context.Context) {
		var nodes corev1.NodeList
		g.Expect(tc.List(ctx, &nodes)).To(Succeed(), "failed to list nodes")
		for _, node := range nodes.Items {
			for _, condition := range node.Status.Conditions {
				if condition.Type == corev1.NodeReady {
					g.Expect(condition.Status).To(Equal(corev1.ConditionTrue), "node %s is not ready", node.Name)
				}
			}
		}
	}).WithContext(ctx).Should(Succeed(), "failed to wait for cluster to be ready")

	By("Waiting for all pods in the " + namespace + " to be ready")
	Eventually(func(g Gomega, ctx context.Context) {
		var pods corev1.PodList
		g.Expect(tc.List(ctx, &pods, client.InNamespace(namespace))).To(Succeed(), "failed to list pods")
		g.Expect(pods.Items).NotTo(BeEmpty(), "no pods found")
		for _, pod := range pods.Items {
			g.Expect(pod.Status.Phase).To(Equal(corev1.PodRunning), "pod %s is not running", pod.Name)
		}

		var daemonsets appsv1.DaemonSetList
		g.Expect(tc.List(ctx, &daemonsets, client.InNamespace(namespace))).To(Succeed(), "failed to list daemonsets")
		for _, ds := range daemonsets.Items {
			g.Expect(ds.Status.DesiredNumberScheduled).To(Equal(ds.Status.NumberReady), "daemonset %s is not ready, %d/%d ready", ds.Name, ds.Status.NumberReady, ds.Status.DesiredNumberScheduled)
		}
	}).WithContext(ctx).WithTimeout(5*time.Minute).Should(Succeed(), "failed to wait for pods to be ready")

	By("Waiting for all statefulset to be ready")
	Eventually(func(g Gomega, ctx context.Context) {
		g.Consistently(ts.verifyApplicationRunning).WithContext(ctx).WithTimeout(time.Minute).Should(Succeed(), "failed to wait for statefulsets to be ready")
	}).WithContext(ctx).WithTimeout(5*time.Minute).Should(Succeed(), "failed to wait for statefulsets to be ready")
}

// Verify that application is running and pods have writable mounts
// - list all the statefulsets in the application namespace
// - check if all the statefulsets are ready
// - list all the pods in the application namespace
// - check if all the pods are running
// - check if all the pods have writable mounts.
func (ts *testsuite) verifyApplicationRunning(g Gomega, ctx context.Context) {
	var statefulsets appsv1.StatefulSetList
	g.Expect(tc.List(ctx, &statefulsets, client.InNamespace(applicationNamespace))).To(Succeed(), "failed to list statefulsets")
	g.Expect(statefulsets.Items).NotTo(BeEmpty(), "no statefulsets found")
	for _, sts := range statefulsets.Items {
		g.Expect(sts.Status.ReadyReplicas).To(Equal(*sts.Spec.Replicas), "statefulset %s is not ready", sts.Name)
	}
	var pods corev1.PodList
	g.Expect(tc.List(ctx, &pods, client.InNamespace(applicationNamespace))).To(Succeed(), "failed to list pods")
	g.Expect(pods.Items).NotTo(BeEmpty(), "no pods found")
	for _, pod := range pods.Items {
		// check if all the pods are running
		g.Expect(pod.Status.Phase).To(Equal(corev1.PodRunning), "pod %s is not running", pod.Name)
		// check if pod mount is writable, have a timeout in case requests is stuck.
		// This can happen when the nexus is paused
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(writeCtx, "kubectl", "exec", pod.Name, "-n", applicationNamespace, "--", "sh", "-c", fmt.Sprintf("echo '%s' > /mnt/cns/check", time.Now().UTC().Format(time.RFC3339)))
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "Failed to write to pod mount")
	}
}

// handleAzureError checks if the error we see from azure isn't a drain. If so, we
// skip the test. If it is a drain error, we fail the test.
func handleAzureError(err error, msg string) {
	if err == nil {
		return
	}
	if isDrainFailure(err) {
		Expect(err).NotTo(HaveOccurred(), msg)
	}
	Skip(msg + ": " + err.Error())
}

// isDrainFailure checks if the error is a drain failure.
func isDrainFailure(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "Drain node") && strings.Contains(err.Error(), "Eviction forbidden")
}

// pvAffinityForExistingNode checks if the given node affinity matches any of the existing nodes.
func pvAffinityForExistingNode(nodeAffinity *corev1.VolumeNodeAffinity, nodes map[string]struct{}) bool {
	if nodeAffinity == nil || nodeAffinity.Required == nil {
		// No node affinity, so we can use any node
		return true
	}

	for _, term := range nodeAffinity.Required.NodeSelectorTerms {
		for _, expr := range term.MatchExpressions {
			if expr.Key == "topology.localdisk.csi.acstor.io/node" && expr.Operator == corev1.NodeSelectorOpIn {
				for _, value := range expr.Values {
					if _, exists := nodes[value]; exists {
						return true
					}
				}
			}
		}
	}
	// No matching node found
	return false
}
