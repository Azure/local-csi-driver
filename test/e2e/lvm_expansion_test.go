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
	// expansionPVCTargetSize is the size we resize the PVC to. The fixtures
	// request 10Gi, so doubling to 20Gi (a 100% increase) makes the growth
	// unambiguous: even after filesystem metadata overhead the post-expansion
	// capacity observed by the workload (~19+ GiB) cannot be confused with the
	// original ~10 GiB volume. The kind e2e VG is 100GiB and AKS NVMe disks are
	// far larger, so this comfortably fits.
	expansionPVCTargetSize = "20Gi"
	// expansionPVCTargetBytes is expansionPVCTargetSize expressed in bytes.
	// 20 * 1024^3 == 21474836480. Kept in sync with expansionPVCTargetSize.
	expansionPVCTargetBytes int64 = 20 * 1024 * 1024 * 1024
)

// lvmExpansionTest exercises the full PVC -> CSI -> lvextend resize path. It
// works for both filesystem (ext4, xfs) and raw block volumes, since the
// assertions only look at the on-disk LV size, the PVC/PV capacity and the
// expanded-capacity annotation, none of which depend on the volume mode:
//
//  1. create the PVC + pod and wait for it to be Running so that the LV exists
//     on disk;
//  2. patch the PVC storage request to a larger size;
//  3. assert that the LV on the owning node actually grew (the original bug
//     was that lvextend was never invoked because the early-return branch
//     swallowed the request);
//  4. assert that PVC.status, PV.spec.capacity, and the PV's
//     expanded-capacity annotation all reflect the new size so a subsequent
//     failover-driven re-provisioning recreates the LV at the expanded size.
//
// In addition to the control-plane and on-disk LV assertions, it execs into
// the workload pod and checks the capacity the container itself observes - the
// filesystem total (df) for Filesystem volumes or the block device size for
// Block volumes - so that a regression where the LV grows but the resize never
// reaches the workload (e.g. a missing online fs resize) is caught.
//
// volMode must be "Filesystem" or "Block" and podPath is the in-pod mount path
// (Filesystem) or device path (Block) declared by the pod fixture.
//
// The storageclass(es) referenced by the fixtures must already exist (created
// in the enclosing context's storageclass spec).
func lvmExpansionTest(name, pvcFixture, podFixture, volMode, podPath string) {
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
		podName := waitForE2EPodRunning(ctx)

		By("looking up the PVC's bound PV and owning node")
		pvcName, err := getPVCName(ctx, pvcFixture)
		Expect(err).NotTo(HaveOccurred(), "Failed to read PVC name from fixture")

		pvName := getPVCVolumeName(ctx, pvcName)
		Expect(pvName).NotTo(BeEmpty(), "PVC should be bound to a PV")

		nodeName := getE2EPodNode(ctx)
		Expect(nodeName).NotTo(BeEmpty(), "Pod should be scheduled on a node")

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

		By("recording the capacity observed by the workload pod before expansion")
		originalPodBytes, err := getWorkloadVisibleCapacityBytes(ctx, podName, volMode, podPath)
		Expect(err).NotTo(HaveOccurred(), "Failed to read workload-visible capacity before expansion")
		Expect(originalPodBytes).To(BeNumerically(">", int64(0)), "Workload-visible capacity must be positive")
		_, _ = fmt.Fprintf(GinkgoWriter, "Original workload-visible capacity (%s %s) = %d bytes\n", volMode, podPath, originalPodBytes)

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
			g.Expect(getExpandedCapacityAnnotation(ctx, pvName)).To(BeNumerically(">=", expansionPVCTargetBytes),
				"expanded-capacity annotation should be >= requested expansion size (%d)", expansionPVCTargetBytes)
		}).WithContext(ctx).Should(Succeed(), "PV expanded-capacity annotation never set")

		By("verifying the workload is still Running after the expand")
		out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "get", "pod", "-l", "part-of=e2e-test", "-o", "jsonpath={.items[0].status.phase}"))
		Expect(err).NotTo(HaveOccurred(), "Failed to re-check pod phase after expand")
		Expect(strings.TrimSpace(out)).To(Equal("Running"), "Pod should remain Running after PVC expansion")

		By("verifying the workload pod observes the expanded capacity")
		// The filesystem total is always a little smaller than the backing
		// device because of metadata overhead, so allow a margin for
		// Filesystem volumes. A Block volume is the device itself, so it must
		// reach the full expanded size.
		minPodBytes := expansionPVCTargetBytes
		if volMode != "Block" {
			minPodBytes = expansionPVCTargetBytes * 9 / 10
		}
		Eventually(func(g Gomega, ctx context.Context) {
			sz, err := getWorkloadVisibleCapacityBytes(ctx, podName, volMode, podPath)
			g.Expect(err).NotTo(HaveOccurred(), "Failed to read workload-visible capacity during expansion")
			_, _ = fmt.Fprintf(GinkgoWriter, "Workload-visible capacity (%s %s) = %d bytes (want >= %d, was %d)\n", volMode, podPath, sz, minPodBytes, originalPodBytes)
			g.Expect(sz).To(BeNumerically(">", originalPodBytes), "Workload-visible capacity should have grown after expansion")
			g.Expect(sz).To(BeNumerically(">=", minPodBytes), "Workload should observe the expanded capacity")
		}).WithContext(ctx).Should(Succeed(), "workload pod never observed the expanded capacity")
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

// getWorkloadVisibleCapacityBytes returns the capacity, in bytes, of the volume
// as observed from inside the workload pod. For Filesystem volumes it reports
// the filesystem total at the mount path (df); for Block volumes it reports the
// block device size at the device path (blockdev). Both tools ship with the
// busybox image used by the fixtures.
func getWorkloadVisibleCapacityBytes(ctx context.Context, podName, volMode, path string) (int64, error) {
	var script string
	if volMode == "Block" {
		// blockdev reports the device size directly in bytes.
		script = fmt.Sprintf("blockdev --getsize64 %s", path)
	} else {
		// df -B1 reports the filesystem total in bytes in the second column.
		script = fmt.Sprintf("df -P -B1 %s | awk 'NR==2 {print $2}'", path)
	}
	out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "exec", podName, "--", "/bin/sh", "-c", script))
	if err != nil {
		return 0, fmt.Errorf("reading volume capacity in pod %s: %w", podName, err)
	}
	raw := strings.TrimSpace(out)
	if raw == "" {
		return 0, fmt.Errorf("empty capacity output from pod %s", podName)
	}
	return strconv.ParseInt(raw, 10, 64)
}
