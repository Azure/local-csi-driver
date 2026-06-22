// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"local-csi-driver/internal/csi/core/lvm"
	"local-csi-driver/test/pkg/utils"
)

// lvmFailoverExpansionTest verifies that after a PVC is expanded and its pod is
// failed over to a different node, the volume is recreated on the new node at
// the EXPANDED size.
//
// Local volumes are ephemeral: when a pod fails over to another node it gets a
// fresh, empty volume (the stale LV on the old node is garbage collected). The
// correctness property that must survive the failover is therefore the volume
// SIZE, not its data. NodeStageVolume on the destination node reads the PV's
// expanded-capacity annotation (set after a successful NodeExpandVolume) and
// recreates the LV at that size rather than the immutable create-time capacity
// - this test exercises exactly that path end to end.
//
// The test cordons the original node to force the pod onto a different one, so
// it requires a multi-node cluster where the PV is not hard-pinned to its node
// (the availability failover configuration). It self-skips when those
// preconditions are not met.
//
// The storageclass referenced by the fixtures must already exist (created in
// the enclosing context's storageclass spec).
func lvmFailoverExpansionTest(name, pvcFixture, podFixture string) {
	It(name, func(ctx context.Context) {
		By("applying the PVC fixture")
		_, err := utils.Run(exec.CommandContext(ctx, "kubectl", "apply", "-f", pvcFixture))
		Expect(err).NotTo(HaveOccurred(), "Failed to apply PVC fixture")

		DeferCleanup(func(ctx context.Context) {
			By("deleting PVC")
			_, _ = utils.Run(exec.CommandContext(ctx, "kubectl", "delete", "--wait", "--ignore-not-found", "-f", pvcFixture))
		})

		By("applying the pod fixture")
		_, err = utils.Run(exec.CommandContext(ctx, "kubectl", "apply", "-f", podFixture))
		Expect(err).NotTo(HaveOccurred(), "Failed to apply pod fixture")

		DeferCleanup(func(ctx context.Context) {
			By("deleting pod")
			_, _ = utils.Run(exec.CommandContext(ctx, "kubectl", "delete", "--wait", "--ignore-not-found", "-f", podFixture))
		})

		By("waiting for the pod to be Running")
		_ = waitForE2EPodRunning(ctx)

		By("recording the bound PV and owning node")
		pvcName, err := getPVCName(ctx, pvcFixture)
		Expect(err).NotTo(HaveOccurred(), "Failed to read PVC name from fixture")
		pvName := getPVCVolumeName(ctx, pvcName)
		Expect(pvName).NotTo(BeEmpty(), "PVC should be bound to a PV")
		originalNode := getE2EPodNode(ctx)
		Expect(originalNode).NotTo(BeEmpty(), "Pod should be scheduled on a node")

		if pvHasRequiredNodeAffinity(ctx, pvName) {
			Skip("PV is hard-pinned to its node (durability configuration); cross-node failover is not possible")
		}

		targetNode := pickDifferentDriverNode(ctx, originalNode)
		if targetNode == "" {
			Skip("no other driver node available to fail over to; need a multi-node cluster")
		}

		By(fmt.Sprintf("expanding PVC %s to %s", pvcName, expansionPVCTargetSize))
		originalDriverPod, err := getDriverPodOnNode(ctx, originalNode)
		Expect(err).NotTo(HaveOccurred(), "Failed to locate csi-local-node pod on %s", originalNode)
		Expect(originalDriverPod).NotTo(BeEmpty(), "No csi-local-node pod found on node %s", originalNode)

		originalLVBytes, err := getLVSizeBytes(ctx, originalDriverPod, pvName)
		Expect(err).NotTo(HaveOccurred(), "Failed to read original LV size")
		Expect(originalLVBytes).To(BeNumerically("<", expansionPVCTargetBytes),
			"Test precondition: original LV size (%d) must be smaller than target (%d)", originalLVBytes, expansionPVCTargetBytes)

		patch := fmt.Sprintf(`{"spec":{"resources":{"requests":{"storage":"%s"}}}}`, expansionPVCTargetSize)
		_, err = utils.Run(exec.CommandContext(ctx, "kubectl", "patch", "pvc", pvcName, "--type=merge", "-p", patch))
		Expect(err).NotTo(HaveOccurred(), "Failed to patch PVC storage request")

		By("waiting for the LV on the original node to reach the expanded size")
		Eventually(func(g Gomega, ctx context.Context) {
			sz, err := getLVSizeBytes(ctx, originalDriverPod, pvName)
			g.Expect(err).NotTo(HaveOccurred(), "Failed to read LV size during expansion")
			g.Expect(sz).To(BeNumerically(">=", expansionPVCTargetBytes), "LV should have grown to the requested size")
		}).WithContext(ctx).Should(Succeed(), "LV was never extended on disk")

		By("waiting for the PV expanded-capacity annotation to be recorded")
		Eventually(func(g Gomega, ctx context.Context) {
			g.Expect(getExpandedCapacityAnnotation(ctx, pvName)).To(BeNumerically(">=", expansionPVCTargetBytes),
				"expanded-capacity annotation should record the post-expansion size")
		}).WithContext(ctx).Should(Succeed(), "PV expanded-capacity annotation never set")

		By(fmt.Sprintf("cordoning the original node %s to force a failover", originalNode))
		_, err = utils.Run(exec.CommandContext(ctx, "kubectl", "cordon", originalNode))
		Expect(err).NotTo(HaveOccurred(), "Failed to cordon node %s", originalNode)
		DeferCleanup(func(ctx context.Context) {
			By(fmt.Sprintf("uncordoning node %s", originalNode))
			_, _ = utils.Run(exec.CommandContext(ctx, "kubectl", "uncordon", originalNode))
		})

		By("deleting the pod to trigger failover to a different node")
		_, err = utils.Run(exec.CommandContext(ctx, "kubectl", "delete", "--wait", "--ignore-not-found", "-f", podFixture))
		Expect(err).NotTo(HaveOccurred(), "Failed to delete pod")

		By("recreating the pod")
		_, err = utils.Run(exec.CommandContext(ctx, "kubectl", "apply", "-f", podFixture))
		Expect(err).NotTo(HaveOccurred(), "Failed to recreate pod fixture")

		By("waiting for the pod to be Running on a different node")
		var newNode, newPodName string
		Eventually(func(g Gomega, ctx context.Context) {
			newPodName = waitForE2EPodRunning(ctx)
			g.Expect(newPodName).NotTo(BeEmpty(), "pod should be Running")
			newNode = getE2EPodNode(ctx)
			g.Expect(newNode).NotTo(BeEmpty(), "pod should be scheduled on a node")
			g.Expect(newNode).NotTo(Equal(originalNode), "pod should have failed over to a different node")
		}).WithContext(ctx).Should(Succeed(), "pod did not fail over to a different node")
		_, _ = fmt.Fprintf(GinkgoWriter, "Pod failed over from %s to %s\n", originalNode, newNode)

		By("verifying the volume was recreated on the new node at the expanded size")
		newDriverPod, err := getDriverPodOnNode(ctx, newNode)
		Expect(err).NotTo(HaveOccurred(), "Failed to locate csi-local-node pod on %s", newNode)
		Expect(newDriverPod).NotTo(BeEmpty(), "No csi-local-node pod found on node %s", newNode)

		Eventually(func(g Gomega, ctx context.Context) {
			sz, err := getLVSizeBytes(ctx, newDriverPod, pvName)
			g.Expect(err).NotTo(HaveOccurred(), "Failed to read LV size on the new node")
			_, _ = fmt.Fprintf(GinkgoWriter, "Failed-over LV %s on %s = %d bytes (want >= %d)\n", pvName, newDriverPod, sz, expansionPVCTargetBytes)
			g.Expect(sz).To(BeNumerically(">=", expansionPVCTargetBytes),
				"failover volume must be recreated at the expanded size, not the original create-time size")
		}).WithContext(ctx).Should(Succeed(), "failover volume was not recreated at the expanded size")

		By("verifying the workload pod on the new node observes the expanded capacity")
		// This fixture is a Filesystem (ext4) volume mounted at /mnt/lcd; the
		// filesystem total is a little under the device size, so allow a
		// margin for metadata overhead.
		minPodBytes := expansionPVCTargetBytes * 9 / 10
		Eventually(func(g Gomega, ctx context.Context) {
			sz, err := getWorkloadVisibleCapacityBytes(ctx, newPodName, "Filesystem", "/mnt/lcd")
			g.Expect(err).NotTo(HaveOccurred(), "Failed to read workload-visible capacity after failover")
			_, _ = fmt.Fprintf(GinkgoWriter, "Failed-over workload-visible capacity = %d bytes (want >= %d)\n", sz, minPodBytes)
			g.Expect(sz).To(BeNumerically(">=", minPodBytes),
				"workload on the new node should observe the expanded capacity")
		}).WithContext(ctx).Should(Succeed(), "workload pod never observed the expanded capacity after failover")
	})
}

// waitForE2EPodRunning blocks until the single pod labeled part-of=e2e-test is
// Running and returns its name.
func waitForE2EPodRunning(ctx context.Context) string {
	var podName string
	Eventually(func(g Gomega, ctx context.Context) {
		out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "get", "pod", "-l", "part-of=e2e-test",
			"-o", "jsonpath={.items[0].status.phase}"))
		g.Expect(err).NotTo(HaveOccurred(), "Failed to get pod phase")
		g.Expect(strings.TrimSpace(out)).To(Equal("Running"), "Pod should be Running")

		out, err = utils.Run(exec.CommandContext(ctx, "kubectl", "get", "pod", "-l", "part-of=e2e-test",
			"-o", "jsonpath={.items[0].metadata.name}"))
		g.Expect(err).NotTo(HaveOccurred(), "Failed to get pod name")
		podName = strings.TrimSpace(out)
		g.Expect(podName).NotTo(BeEmpty(), "Pod name should not be empty")
	}).WithContext(ctx).Should(Succeed(), "Failed to wait for pod to be Running")
	return podName
}

// getE2EPodNode returns the node name of the single pod labeled
// part-of=e2e-test.
func getE2EPodNode(ctx context.Context) string {
	out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "get", "pod", "-l", "part-of=e2e-test",
		"-o", "jsonpath={.items[0].spec.nodeName}"))
	Expect(err).NotTo(HaveOccurred(), "Failed to get pod nodeName")
	return strings.TrimSpace(out)
}

// getPVCVolumeName returns the PV name bound to the given PVC, waiting until the
// binding exists.
func getPVCVolumeName(ctx context.Context, pvcName string) string {
	var pvName string
	Eventually(func(g Gomega, ctx context.Context) {
		out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "get", "pvc", pvcName,
			"-o", "jsonpath={.spec.volumeName}"))
		g.Expect(err).NotTo(HaveOccurred(), "Failed to get PVC volumeName")
		pvName = strings.TrimSpace(out)
		g.Expect(pvName).NotTo(BeEmpty(), "PVC should be bound to a PV")
	}).WithContext(ctx).Should(Succeed(), "PVC was never bound to a PV")
	return pvName
}

// getExpandedCapacityAnnotation returns the integer value of the PV's
// expanded-capacity annotation, or 0 if it is absent.
func getExpandedCapacityAnnotation(ctx context.Context, pvName string) int64 {
	jsonpath := fmt.Sprintf("jsonpath={.metadata.annotations.%s}", strings.ReplaceAll(lvm.ExpandedCapacityParam, ".", "\\."))
	out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "get", "pv", pvName, "-o", jsonpath))
	Expect(err).NotTo(HaveOccurred(), "Failed to get PV expanded-capacity annotation")
	raw := strings.TrimSpace(out)
	if raw == "" {
		return 0
	}
	var v int64
	_, err = fmt.Sscan(raw, &v)
	Expect(err).NotTo(HaveOccurred(), "expanded-capacity annotation should be a valid integer")
	return v
}

// pvHasRequiredNodeAffinity reports whether the PV carries a required node
// affinity, which would hard-pin its pod to a single node and prevent
// cross-node failover.
func pvHasRequiredNodeAffinity(ctx context.Context, pvName string) bool {
	out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "get", "pv", pvName,
		"-o", "jsonpath={.spec.nodeAffinity.required}"))
	Expect(err).NotTo(HaveOccurred(), "Failed to get PV node affinity")
	return strings.TrimSpace(out) != ""
}

// pickDifferentDriverNode returns the name of a csi-local-node driver node that
// is not exclude, or "" if there is no other driver node.
func pickDifferentDriverNode(ctx context.Context, exclude string) string {
	out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "get", "pods", "-n", "kube-system",
		"-l", "app.kubernetes.io/component=csi-local-node",
		"-o", "jsonpath={range .items[*]}{.spec.nodeName}{\"\\n\"}{end}"))
	Expect(err).NotTo(HaveOccurred(), "Failed to list csi-local-node pods")
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		node := strings.TrimSpace(line)
		if node != "" && node != exclude {
			return node
		}
	}
	return ""
}
