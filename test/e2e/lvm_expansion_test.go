// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"local-csi-driver/internal/csi/core/lvm"
	"local-csi-driver/test/pkg/utils"
)

const (
	// expansionPVCTargetSize is the size we resize the PVC to. A 1 GiB bump
	// is enough to be unambiguous (LVM aligns on 4 MiB extents) while staying
	// friendly to constrained test environments.
	expansionPVCTargetSize = "11Gi"
	// expansionPVCTargetBytes is expansionPVCTargetSize expressed in bytes.
	// 11 * 1024^3 == 11811160064. Kept in sync with expansionPVCTargetSize.
	expansionPVCTargetBytes int64 = 11 * 1024 * 1024 * 1024
)

// lvmExpansionTest exercises the full PVC -> CSI -> lvextend resize path:
//
//  1. create the annotation-style PVC + pod and wait for it to be Running so
//     that the LV exists on disk;
//  2. patch the PVC storage request to a larger size;
//  3. assert that the LV on the owning node actually grew (the original bug
//     was that lvextend was never invoked because the early-return branch
//     swallowed the request);
//  4. assert that PVC.status, PV.spec.capacity, and the PV's
//     expanded-capacity annotation all reflect the new size so a subsequent
//     failover-driven re-provisioning recreates the LV at the expanded size.
func lvmExpansionTest(name, pvcFixture, podFixture string) {
	It(name, func(ctx context.Context) {
		By("applying the PVC fixture")
		cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", pvcFixture)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to apply PVC fixture")

		DeferCleanup(func(ctx context.Context) {
			By("deleting PVC")
			cmd := exec.CommandContext(ctx, "kubectl", "delete", "--wait", "--ignore-not-found", "-f", pvcFixture)
			_, _ = utils.Run(cmd)
		})

		By("applying the pod fixture")
		cmd = exec.CommandContext(ctx, "kubectl", "apply", "-f", podFixture)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to apply pod fixture")

		DeferCleanup(func(ctx context.Context) {
			By("deleting pod")
			cmd := exec.CommandContext(ctx, "kubectl", "delete", "--wait", "--ignore-not-found", "-f", podFixture)
			_, _ = utils.Run(cmd)
		})

		By("waiting for the pod to be Running")
		Eventually(func(g Gomega, ctx context.Context) {
			out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "get", "pod", "-l", "part-of=e2e-test", "-o", "jsonpath={.items[0].status.phase}"))
			g.Expect(err).NotTo(HaveOccurred(), "Failed to get pod phase")
			g.Expect(out).To(Equal("Running"), "Pod should be Running before expansion")
		}).WithContext(ctx).Should(Succeed(), "Failed to wait for pod to be Running")

		By("looking up the PVC's bound PV and owning node")
		pvcName, err := getPVCName(ctx, pvcFixture)
		Expect(err).NotTo(HaveOccurred(), "Failed to read PVC name from fixture")

		var pvName, nodeName string
		Eventually(func(g Gomega, ctx context.Context) {
			out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "get", "pvc", pvcName, "-o", "jsonpath={.spec.volumeName}"))
			g.Expect(err).NotTo(HaveOccurred(), "Failed to get PVC volumeName")
			pvName = strings.TrimSpace(out)
			g.Expect(pvName).NotTo(BeEmpty(), "PVC should be bound to a PV")
		}).WithContext(ctx).Should(Succeed(), "PVC was never bound to a PV")

		Eventually(func(g Gomega, ctx context.Context) {
			out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "get", "pod", "-l", "part-of=e2e-test", "-o", "jsonpath={.items[0].spec.nodeName}"))
			g.Expect(err).NotTo(HaveOccurred(), "Failed to get pod nodeName")
			nodeName = strings.TrimSpace(out)
			g.Expect(nodeName).NotTo(BeEmpty(), "Pod should be scheduled on a node")
		}).WithContext(ctx).Should(Succeed(), "Pod did not have a nodeName")

		driverPod, err := getDriverPodOnNode(ctx, nodeName)
		Expect(err).NotTo(HaveOccurred(), "Failed to locate csi-local-node pod on %s", nodeName)
		Expect(driverPod).NotTo(BeEmpty(), "No csi-local-node pod found on node %s", nodeName)

		By("recording the LV size before expansion")
		originalLVBytes, err := getLVSizeBytes(ctx, driverPod, pvName)
		Expect(err).NotTo(HaveOccurred(), "Failed to read original LV size")
		Expect(originalLVBytes).To(BeNumerically(">", int64(0)), "Original LV size must be positive")
		Expect(originalLVBytes).To(BeNumerically("<", expansionPVCTargetBytes),
			"Test precondition: original LV size (%d) must be smaller than target (%d)", originalLVBytes, expansionPVCTargetBytes)
		_, _ = fmt.Fprintf(GinkgoWriter, "Original LV %s on %s = %d bytes\n", pvName, driverPod, originalLVBytes)

		By(fmt.Sprintf("patching PVC %s storage request to %s", pvcName, expansionPVCTargetSize))
		patch := fmt.Sprintf(`{"spec":{"resources":{"requests":{"storage":"%s"}}}}`, expansionPVCTargetSize)
		_, err = utils.Run(exec.CommandContext(ctx, "kubectl", "patch", "pvc", pvcName, "--type=merge", "-p", patch))
		Expect(err).NotTo(HaveOccurred(), "Failed to patch PVC storage request")

		By("waiting for the LV on disk to reach the new size")
		Eventually(func(g Gomega, ctx context.Context) {
			sz, err := getLVSizeBytes(ctx, driverPod, pvName)
			g.Expect(err).NotTo(HaveOccurred(), "Failed to read LV size during expansion")
			_, _ = fmt.Fprintf(GinkgoWriter, "Current LV %s = %d bytes (want >= %d)\n", pvName, sz, expansionPVCTargetBytes)
			g.Expect(sz).To(BeNumerically(">=", expansionPVCTargetBytes), "LV should have grown to the requested size")
		}).WithContext(ctx).Should(Succeed(), "LV was never extended on disk")

		By("waiting for PVC.status.capacity to reflect the new size")
		Eventually(func(g Gomega, ctx context.Context) {
			out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "get", "pvc", pvcName, "-o", "jsonpath={.status.capacity.storage}"))
			g.Expect(err).NotTo(HaveOccurred(), "Failed to get PVC status capacity")
			g.Expect(strings.TrimSpace(out)).To(Equal(expansionPVCTargetSize), "PVC.status.capacity should equal the new request")
		}).WithContext(ctx).Should(Succeed(), "PVC.status.capacity never updated")

		By("waiting for PV.spec.capacity to reflect the new size")
		Eventually(func(g Gomega, ctx context.Context) {
			out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "get", "pv", pvName, "-o", "jsonpath={.spec.capacity.storage}"))
			g.Expect(err).NotTo(HaveOccurred(), "Failed to get PV capacity")
			g.Expect(strings.TrimSpace(out)).To(Equal(expansionPVCTargetSize), "PV.spec.capacity should equal the new request")
		}).WithContext(ctx).Should(Succeed(), "PV.spec.capacity never updated")

		By("verifying the PV expanded-capacity annotation records the actual LV size")
		Eventually(func(g Gomega, ctx context.Context) {
			jsonpath := fmt.Sprintf("{.metadata.annotations.%s}", strings.ReplaceAll(lvm.ExpandedCapacityParam, ".", "\\."))
			out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "get", "pv", pvName, "-o", fmt.Sprintf("jsonpath=%s", jsonpath)))
			g.Expect(err).NotTo(HaveOccurred(), "Failed to get PV expanded-capacity annotation")
			raw := strings.TrimSpace(out)
			g.Expect(raw).NotTo(BeEmpty(), "PV should have the expanded-capacity annotation")
			annotationBytes, err := strconv.ParseInt(raw, 10, 64)
			g.Expect(err).NotTo(HaveOccurred(), "expanded-capacity annotation should be a valid integer")
			g.Expect(annotationBytes).To(BeNumerically(">=", expansionPVCTargetBytes),
				"expanded-capacity annotation (%d) should be >= requested expansion size (%d)", annotationBytes, expansionPVCTargetBytes)
		}).WithContext(ctx).Should(Succeed(), "PV expanded-capacity annotation never set")

		By("verifying the workload is still Running after the expand")
		out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "get", "pod", "-l", "part-of=e2e-test", "-o", "jsonpath={.items[0].status.phase}"))
		Expect(err).NotTo(HaveOccurred(), "Failed to re-check pod phase after expand")
		Expect(strings.TrimSpace(out)).To(Equal("Running"), "Pod should remain Running after PVC expansion")
	})
}

// getPVCName reads the metadata.name from a single-document PVC fixture using kubectl.
func getPVCName(ctx context.Context, pvcFixture string) (string, error) {
	out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "get", "-f", pvcFixture, "-o", "jsonpath={.metadata.name}"))
	if err != nil {
		return "", fmt.Errorf("kubectl get -f %s: %w", pvcFixture, err)
	}
	return strings.TrimSpace(out), nil
}

// getDriverPodOnNode returns the name of the csi-local-node pod scheduled on
// the given node, or "" if none is found.
func getDriverPodOnNode(ctx context.Context, node string) (string, error) {
	out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "get", "pods", "-n", "kube-system",
		"-l", "app.kubernetes.io/component=csi-local-node",
		"-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\t\"}{.spec.nodeName}{\"\\n\"}{end}"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 2 {
			continue
		}
		if strings.TrimSpace(fields[1]) == node {
			return strings.TrimSpace(fields[0]), nil
		}
	}
	return "", nil
}

// getLVSizeBytes returns the on-disk size in bytes of the LV named lvName in
// the driver's default volume group, by execing into a driver pod and running
// `lvs` with byte units.
func getLVSizeBytes(ctx context.Context, driverPod, lvName string) (int64, error) {
	lvPath := lvm.DefaultVolumeGroup + "/" + lvName
	out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "exec", "-n", "kube-system", driverPod, "--",
		"lvs", "--noheadings", "--nosuffix", "--units", "b", "--options", "lv_size", lvPath))
	if err != nil {
		return 0, fmt.Errorf("lvs %s: %w", lvPath, err)
	}
	raw := strings.TrimSpace(out)
	if raw == "" {
		return 0, fmt.Errorf("lvs %s returned empty output", lvPath)
	}
	return strconv.ParseInt(raw, 10, 64)
}
