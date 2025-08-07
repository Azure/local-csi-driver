// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package e2e

import (
	"context"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"local-csi-driver/test/pkg/utils"
)

var _ = Describe("Local CSI Driver", Label("e2e"), Ordered, func() {
	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)
	EnforceDefaultTimeoutsWhenUsingContexts()

	Context("Webhooks", func() {
		It("should provision certificates", func(ctx context.Context) {
			By("validating that the certificate Secret exists")
			verifySecret := func(g Gomega, ctx context.Context) {
				cmd := exec.CommandContext(ctx, "kubectl", "get", "secrets", *helmPrefix+"-webhook-cert", "-n", namespace)
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}
			Eventually(verifySecret).WithContext(ctx).Should(Succeed())
		})

		It("should have CA injection for mutating webhooks", func() {
			By("checking CA injection for mutating webhooks")
			verifyCAInjection := func(g Gomega) {
				cmd := exec.Command("kubectl", "get",
					"mutatingwebhookconfigurations.admissionregistration.k8s.io",
					*helmPrefix+"-hyperconverged-webhook",
					"-o", "go-template={{ range .webhooks }}{{ .clientConfig.caBundle }}{{ end }}")
				mwhOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(mwhOutput)).To(BeNumerically(">", 10))
			}
			Eventually(verifyCAInjection).Should(Succeed())
		})

		It("should have CA injection for validating webhooks", func() {
			By("checking CA injection for validating webhooks")
			verifyCAInjection := func(g Gomega) {
				cmd := exec.Command("kubectl", "get",
					"validatingwebhookconfigurations.admissionregistration.k8s.io",
					*helmPrefix+"-enforce-ephemeral",
					"-o", "go-template={{ range .webhooks }}{{ .clientConfig.caBundle }}{{ end }}")
				vwhOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(vwhOutput)).To(BeNumerically(">", 10))
			}
			Eventually(verifyCAInjection).Should(Succeed())
		})
	})

	// TODO: these tests require diskpools and storagepools to be created on cluster prior to running.
	// Context("CSI Controller", testLVMImmediate)
	// TODO(sc): WaitForFirstConsumer not passing yet.
	// Context("CSI Controller", testLVMWait)
	Context("Metrics", testMetrics)

	Context("LVM", Label("lvm"), testLvmStoragePool)
})
