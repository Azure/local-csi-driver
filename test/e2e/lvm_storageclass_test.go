// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package e2e

import (
	"context"
	"os/exec"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"local-csi-driver/test/pkg/common"
	"local-csi-driver/test/pkg/utils"
)

var testLocalStorageClass = func() {
	Context("local csi driver", Label("lvm"), func() {
		BeforeAll(func(ctx context.Context) {
			fixtures := []string{
				common.LvmStorageClassFixture,
				common.LvmStorageClassXfsFixture,
			}
			for _, fixture := range fixtures {
				cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", fixture)
				_, err := utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred(), "Failed to apply storageclass")

				DeferCleanup(func(ctx context.Context) {
					By("Deleting storageclass")
					Eventually(func(g Gomega, ctx context.Context) {
						cmd := exec.CommandContext(ctx, "kubectl", "delete", "--wait", "--ignore-not-found", "-f", fixture)
						_, err := utils.Run(cmd)
						g.Expect(err).NotTo(HaveOccurred(), "Failed to delete storageclass")
					}).WithContext(ctx).Should(Succeed(), "Failed to eventually delete storageclass")
				})
			}
		})

		localStatefulsetTest("should create statefulset with local storageclass", common.LvmStatefulSetFixture)
		localStatefulsetTest("should create statefulset with local storageclass and xfs fs", common.LvmStatefulSetXfsFixture)
		localStatefulsetTest("should create annotation statefulset with local storageclass", common.LvmAnnotationStatefulSetFixture)
		lvmWebhookRejectTest("should reject statefulset with non-ephemeral local storageclass", common.LvmPvcNoAnnotationFixture)
		lvmHyperconvergedTest("should create hyperconverged pod with local storageclass", common.LvmPvcAnnotationFixure, common.LvmPodAnnotationFixture)
	})

}

// localStatefulsetTest applies a statefulset fixture and waits for it to be
// ready.
func localStatefulsetTest(name, statefulsetFixture string) {
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
	})
}

// lvmWebhookRejectTest tests that the webhook rejects PVCs that are not
// ephemeral.
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
