// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sanity

import (
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-csi/csi-test/v5/pkg/sanity"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc/codes"
)

var _ = sanity.DescribeSanity("LVM Node Service", func(sc *sanity.TestContext) {
	var r *sanity.Resources

	BeforeEach(func() {
		r = &sanity.Resources{
			Context:          sc,
			ControllerClient: csi.NewControllerClient(sc.ControllerConn),
			NodeClient:       csi.NewNodeClient(sc.Conn),
		}
	})

	AfterEach(func() {
		r.Cleanup()
	})

	Describe("NodeExpandVolume", func() {
		It("should reject a request whose rounded size exceeds the limit on linear LVM", func(ctx SpecContext) {
			const miB = int64(1 << 20)
			extent := linearFixtureExtent()
			Expect(extent).To(BeNumerically(">", miB), "need room for a whole-MiB request inside an extent")
			Expect(extent%miB).To(BeZero(), "fixture extent should be a whole number of MiB")

			By("creating an extent-aligned block volume")
			createReq := capacityTestVolumeRequest(sc, "sanity-node-expand-limit")
			createReq.CapacityRange.RequiredBytes = 16 * extent
			volume := r.MustCreateVolume(ctx, createReq).GetVolume()
			initialSize := capacityTestLVSize(volume.GetVolumeId())
			Expect(initialSize).To(Equal(createReq.CapacityRange.RequiredBytes))
			capability := createReq.VolumeCapabilities[0]
			path := sc.TargetPath + "/target"

			By("staging the volume")
			_, err := r.NodeStageVolume(ctx, &csi.NodeStageVolumeRequest{
				VolumeId:          volume.GetVolumeId(),
				StagingTargetPath: sc.StagingPath,
				VolumeCapability:  capability,
				VolumeContext:     volume.GetVolumeContext(),
			})
			Expect(err).NotTo(HaveOccurred())

			By("publishing the volume")
			_, err = r.NodePublishVolume(ctx, &csi.NodePublishVolumeRequest{
				VolumeId:          volume.GetVolumeId(),
				StagingTargetPath: sc.StagingPath,
				TargetPath:        path,
				VolumeCapability:  capability,
				VolumeContext:     volume.GetVolumeContext(),
			})
			Expect(err).NotTo(HaveOccurred())

			By("rejecting expansion when the limit is below the next extent")
			// A whole-MiB request isolates the capacity check from the separate byte-unit fix.
			requested := initialSize + miB
			expandReq := &csi.NodeExpandVolumeRequest{
				VolumeId:         volume.GetVolumeId(),
				VolumePath:       path,
				VolumeCapability: capability,
				CapacityRange:    &csi.CapacityRange{RequiredBytes: requested, LimitBytes: requested},
			}
			resp, err := r.NodeExpandVolume(ctx, expandReq)
			sanity.ExpectErrorCode(resp, err, codes.OutOfRange)
			Expect(capacityTestLVSize(volume.GetVolumeId())).To(Equal(initialSize), "a rejected resize must leave the LV unchanged")

			By("retrying with a limit that permits the next extent")
			nextExtent := initialSize + extent
			expandReq.CapacityRange.LimitBytes = nextExtent
			resp, err = r.NodeExpandVolume(ctx, expandReq)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.GetCapacityBytes()).To(Equal(nextExtent))
			Expect(capacityTestLVSize(volume.GetVolumeId())).To(Equal(nextExtent))
		}, SpecTimeout(3*time.Minute))
	})
})
