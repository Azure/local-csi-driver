// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sanity

import (
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-csi/csi-test/v5/pkg/sanity"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"local-csi-driver/test/pkg/kind"
	"local-csi-driver/test/pkg/utils"
)

var _ = sanity.DescribeSanity("LVM Controller Service [Controller Server]", func(sc *sanity.TestContext) {
	var r *sanity.Resources

	BeforeEach(func() {
		r = &sanity.Resources{
			Context:          sc,
			ControllerClient: csi.NewControllerClient(sc.ControllerConn),
		}
	})

	AfterEach(func() {
		r.Cleanup()
	})

	Describe("CreateVolume", func() {
		It("should honor a limit-only request on linear LVM", func(ctx SpecContext) {
			extent := linearFixtureExtent()
			limit := extent + extent/2

			By("creating a volume with a limit between two extents")
			req := capacityTestVolumeRequest(sc, "sanity-create-limit")
			req.CapacityRange = &csi.CapacityRange{LimitBytes: limit}
			volume := r.MustCreateVolume(ctx, req).GetVolume()

			By("checking that the LV size matches the reported capacity and fits the limit")
			Expect(volume.GetCapacityBytes()).To(BeNumerically(">", 0))
			Expect(volume.GetCapacityBytes()).To(BeNumerically("<=", limit))
			Expect(capacityTestLVSize(volume.GetVolumeId())).To(Equal(volume.GetCapacityBytes()))
		}, SpecTimeout(3*time.Minute))
	})
})

func linearFixtureExtent() int64 {
	GinkgoHelper()
	if !kind.IsCluster() {
		Skip("requires the disposable single-node Kind LVM fixture")
	}
	pods, err := utils.Run(exec.Command("kubectl", "get", "pods", "-n", namespace,
		"-l", "app.kubernetes.io/component=csi-local-node", "-o", "name"))
	Expect(err).NotTo(HaveOccurred(), "list driver pods for the forwarded CSI endpoints")
	podCount := len(strings.Fields(pods))
	Expect(podCount).To(BeNumerically(">", 0), "the Kind regression fixture needs a driver pod")
	if podCount > 1 {
		Skip("requires exactly one driver pod so LVM reports and the CSI endpoint refer to the same node")
	}
	out, err := execInPod("vgs", "--noheadings", "--nosuffix", "--units", "b",
		"-o", "pv_count,vg_extent_size", volumeGroupName)
	Expect(err).NotTo(HaveOccurred(), "read the fixture VG geometry")
	fields := strings.Fields(out)
	Expect(fields).To(HaveLen(2), "expected one VG report with PV count and extent size")
	Expect(fields[0]).To(Equal("1"), "the Kind regression fixture should allocate linear LVs")
	extent, err := strconv.ParseInt(fields[1], 10, 64)
	Expect(err).NotTo(HaveOccurred(), "parse the VG extent size in bytes")
	Expect(extent).To(BeNumerically(">", 1), "the fixture needs an extent large enough for an unaligned request")
	return extent
}

func capacityTestVolumeRequest(sc *sanity.TestContext, prefix string) *csi.CreateVolumeRequest {
	req := sanity.MakeCreateVolumeReq(sc, sanity.UniqueString(prefix))
	req.VolumeCapabilities = []*csi.VolumeCapability{{
		AccessType: &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}},
		AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
	}}
	return req
}

func capacityTestLVSize(id string) int64 {
	GinkgoHelper()
	out, err := execInPod("lvs", "--noheadings", "--nosuffix", "--units", "b", "-o", "lv_size",
		strings.Replace(id, "#", "/", 1))
	Expect(err).NotTo(HaveOccurred(), "read the test LV size for %s", id)
	size, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	Expect(err).NotTo(HaveOccurred(), "parse the test LV size in bytes")
	return size
}
