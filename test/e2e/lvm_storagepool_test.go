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
	"local-csi-driver/test/pkg/common"
	"local-csi-driver/test/pkg/utils"
)

var testLvmStoragePool = func() {
	Context("lvm controllers", Label("lvm"), func() {
		It("should create storageclass", func(ctx context.Context) {
			cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", common.LvmStorageClassFixture)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply storageclass")

			Eventually(func(g Gomega, ctx context.Context) {
				cmd := exec.CommandContext(ctx, "kubectl", "get", "storageclass", "local")
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "local storageclass does not exist")
			}).WithContext(ctx).Should(Succeed(), "Failed to create storageclass")
		})

		lvmStatefulsetTest("should create statefulset with local storagepool", common.LvmStatefulSetFixture)
		lvmStatefulsetTest("should create annotation statefulset with local storagepool", common.LvmAnnotationStatefulSetFixture)
		lvmWebhookRejectTest("should reject statefulset with non-ephemeral local storagepool", common.LvmPvcNoAnnotationFixture)
		lvmHyperconvergedTest("should create hyperconverged pod with local storagepool", common.LvmPvcAnnotationFixure, common.LvmPodAnnotationFixture)
	})

}

// lvmStatefulsetTest applies a statefulset fixture and waits for it to be ready.
func lvmStatefulsetTest(name, statefulsetFixture string) {
	It(name, Label("aks"), func(ctx context.Context) {
		cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", statefulsetFixture)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to apply statefulset")

		DeferCleanup(func(ctx context.Context) {
			By("Deleting statefulset")
			cmd := exec.CommandContext(ctx, "kubectl", "delete", "--wait", "--ignore-not-found", "-f", statefulsetFixture)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete statefulset")

			cmd = exec.CommandContext(ctx, "kubectl", "delete", "--wait", "--ignore-not-found", "pvc", "-l", "part-of=e2e-test")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete pvc")
		})

		By("Waiting for statefulset to be ready")
		cmd = exec.CommandContext(ctx, "kubectl", "rollout", "status", "--timeout=5m", "-f", statefulsetFixture)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to wait for statefulset")

		By("Checking that the lvm components match kubernetes")
		Eventually(func(g Gomega, ctx context.Context) {
			cmd := exec.CommandContext(ctx, "kubectl", "get", "pods", "-n", "kube-system", "-l", "app.kubernetes.io/component=csi-local-node", "-o", "jsonpath={.items[*].metadata.name}")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred(), "Failed to list pods with kubectl")
			names := strings.Fields(out)
			g.Expect(names).NotTo(BeEmpty(), "No pods found with label app.kubernetes.io/component=csi-local-node")

			for _, pod := range names {
				appPodCount, err := getPodCountPerDriver(ctx, pod)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to get pod count per driver")

				disks, err := getDiskCount(ctx, pod)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to get disk count")

				pvs, err := getPhysicalVolumeCount(ctx, pod)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to get pv count")

				lvs, err := getLvs(ctx, pod)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to get lv count")

				_, _ = fmt.Fprintf(GinkgoWriter, "Driver pod %s: appPodCount=%d, disks=%d, pvs=%d, lvs=%d\n", pod, appPodCount, disks, pvs, len(lvs))

				g.Expect(lvs).To(HaveLen(appPodCount), "Number of LVs should match number of pods using the driver")
				// if no application pods are scheduled on this node, there may be no PV+VG provisioned
				if appPodCount == 0 {
					g.Expect(pvs).To(SatisfyAny(Equal(0), Equal(disks)), "If no app pods, PVs should be 0 or equal to disks")
				} else {
					g.Expect(pvs).To(Equal(disks), "Number of PVs should match number of disks")
				}

				for _, lv := range lvs {
					g.Expect(lv.VGName).To(Equal(lvm.DefaultVolumeGroup), "LV should be in the default VG")
					g.Expect(lv.Stripes).To(Equal(disks), "LV should have stripes equal to number of disks")
				}

			}

		}).WithContext(ctx).Should(Succeed(), "Failed to list pods with label app.kubernetes.io/component=csi-local-node")

	})
}

// lvmWebhookRejectTest tests that the webhook rejects PVCs that are not ephemeral.
func lvmWebhookRejectTest(name, fixture string) {
	It(name, func(ctx context.Context) {
		cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", fixture)
		out, err := utils.Run(cmd)
		Expect(err).To(HaveOccurred(), "pvc should not be created")

		// Check that the error message matches the expected webhook rejection
		expectedErr := "denied the request: only generic ephemeral volumes are allowed for"
		Expect(out).To(ContainSubstring(expectedErr), "Error should indicate webhook rejection for non-ephemeral PVC")

		// If the PVC was created due to bug by us, it should be deleted.
		DeferCleanup(func(ctx context.Context) {
			By("Deleting pvc")
			cmd := exec.CommandContext(ctx, "kubectl", "delete", "--wait", "--ignore-not-found", "-f", fixture)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete pvc")
		})
	})
}

// lvmHyperconvergedTest make sure that pod scheduled second time for
// annotation PVC ends up on the same node.
func lvmHyperconvergedTest(name, pvcFixture, podFixture string) {
	It(name, Label("aks"), func(ctx context.Context) {
		cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", pvcFixture)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "pvc should be created")

		DeferCleanup(func(ctx context.Context) {
			By("Deleting pvc")
			cmd := exec.CommandContext(ctx, "kubectl", "delete", "--wait", "--ignore-not-found", "-f", pvcFixture)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete pvc")
		})

		cmd = exec.CommandContext(ctx, "kubectl", "apply", "-f", podFixture)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "pod should be created")

		DeferCleanup(func(ctx context.Context) {
			By("Deleting pod")
			cmd := exec.CommandContext(ctx, "kubectl", "delete", "--wait", "--ignore-not-found", "-f", podFixture)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete pod")
		})

		By("Waiting for pod to be Running")
		Eventually(func(g Gomega, ctx context.Context) {
			cmd := exec.CommandContext(ctx, "kubectl", "get", "pod", "-l", "part-of=e2e-test", "-o", "jsonpath={.items[0].status.phase}")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred(), "Failed to get pod status")
			g.Expect(out).To(Equal("Running"), "Pod should be in Running state")
		}).WithContext(ctx).Should(Succeed(), "Failed to wait for pod to be Running")

		By("Deleting pod to test recreation with the same PVC")
		cmd = exec.CommandContext(ctx, "kubectl", "delete", "--wait", "--ignore-not-found", "-f", podFixture)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to delete pod")

		By("Creating pod again with the same PVC")
		cmd = exec.CommandContext(ctx, "kubectl", "apply", "-f", podFixture)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to apply pod fixture")

		By("Waiting for pod to be Running after recreation")
		Eventually(func(g Gomega, ctx context.Context) {
			cmd := exec.CommandContext(ctx, "kubectl", "get", "pod", "-l", "part-of=e2e-test", "-o", "jsonpath={.items[0].status.phase}")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred(), "Failed to get pod status")
			g.Expect(out).To(Equal("Running"), "Pod should be in Running state after recreation")
		}).WithContext(ctx).Should(Succeed(), "Failed to wait for pod to be Running after recreation")

		// check thate spec.affinity.nodeAffinity is set
		cmd = exec.CommandContext(ctx, "kubectl", "get", "pod", "-l", "part-of=e2e-test", "-o", "jsonpath={.items[0].spec.affinity.nodeAffinity}")
		out, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to get pod node affinity")
		Expect(out).NotTo(BeEmpty(), "Node affinity should be set on the pod")
		Expect(out).To(ContainSubstring("preferredDuringSchedulingIgnoredDuringExecution"), "Node affinity should be required during scheduling")
	})

}

// get the numbder of application pods that are on the same node as the pod passed in.
func getPodCountPerDriver(ctx context.Context, podName string) (int, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "pod", podName, "-n", "kube-system", "-o", "jsonpath={.spec.nodeName}")
	out, err := utils.Run(cmd)
	if err != nil {
		return 0, err
	}
	nodeName := strings.TrimSpace(out)
	if nodeName == "" {
		return 0, fmt.Errorf("could not determine node for pod %s", podName)
	}

	cmd = exec.CommandContext(ctx, "kubectl", "get", "pod", "-l", "part-of=e2e-test", "-o", "jsonpath={.items[*].spec.nodeName}")
	out, err = utils.Run(cmd)
	if err != nil {
		return 0, err
	}
	count := 0
	for n := range strings.FieldsSeq(out) {
		if n == nodeName {
			count++
		}
	}
	return count, nil
}

// getDiskCount returns the number of NVMe disks usable for this csi driver pod.
func getDiskCount(ctx context.Context, podName string) (int, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "exec", "-n", "kube-system", podName, "--", "lsblk", "-o", "model")
	out, err := utils.Run(cmd)
	if err != nil {
		return 0, err
	}
	count := 0
	for line := range strings.SplitSeq(out, "\n") {
		if !strings.Contains(line, "Microsoft NVMe Direct Disk") {
			continue
		}
		count++
	}
	return count, nil
}

// getVgPvCount returns the number of PVs in the default VG for this csi driver pod.
func getPhysicalVolumeCount(ctx context.Context, podName string) (int, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "exec", "-n", "kube-system", podName, "--", "vgs", "--no-headings", "--options", "vg_name,pv_count")
	out, err := utils.Run(cmd)
	if err != nil {
		return 0, err
	}

	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		vgName := fields[0]
		vgCount := fields[1]
		if vgName != lvm.DefaultVolumeGroup {
			continue
		}
		count, err := strconv.Atoi(vgCount)
		if err != nil {
			return 0, err
		}
		return count, nil
	}

	return 0, nil
}

type lv struct {
	Name    string
	VGName  string
	Stripes int
}

// getLvs= returns the number of LVs in the default VG for this csi driver pod.
// getLvs returns the logical volumes in the default VG for this csi driver pod.
func getLvs(ctx context.Context, podName string) ([]lv, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "exec", "-n", "kube-system", podName, "--", "lvs", "--no-headings", "--options", "vg_name,lv_name,stripes")
	out, err := utils.Run(cmd)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	lvs := make([]lv, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		vgName := fields[0]
		lvName := fields[1]
		stripes, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, err
		}

		if vgName != lvm.DefaultVolumeGroup {
			continue
		}

		lvs = append(lvs, lv{
			VGName:  vgName,
			Name:    lvName,
			Stripes: stripes,
		})
	}

	return lvs, nil
}
