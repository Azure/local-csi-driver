// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package scale

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/yaml"

	"local-csi-driver/test/pkg/common"
	"local-csi-driver/test/pkg/utils"
)

var (
	// scale is the number of replicas to scale to.
	scale = flag.Int("scale", 75, "Number of replicas to scale to")
	// c is the controller-runtime client.
	c client.Client
	// scheme is the runtime scheme.
	scheme = runtime.NewScheme()
	// ephemeralTemplate is the path to the StatefulSet template with ephemeral volumes.
	ephemeralTemplate = filepath.Join("test", "scale", "fixtures", "ephemeral_statefulset.yaml")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
}

type testData struct {
	scaleOutData []scaleData
	scaleInData  []scaleData
}

type scaleData struct {
	TimeElapsed time.Duration
	Pods        int
	PVs         int
}

// scaleTest is a function that performs a scale test on a Kubernetes StatefulSet with ephemeral volumes.
// It scales the StatefulSet up to a specified number of replicas and then scales it back down, measuring
// the time taken and the number of resources (Pods, PersistentVolumes, and Volumes) at each step.
// The test data is collected and can be used to generate reports or charts for analysis.
var _ = Describe("CSI Scale", Label("scale", "aks"), func() {
	SetDefaultEventuallyTimeout(10 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)
	EnforceDefaultTimeoutsWhenUsingContexts()

	BeforeEach(func(ctx context.Context) {
		kubeconfig := os.Getenv(clientcmd.RecommendedConfigPathEnvVar)
		Expect(kubeconfig).NotTo(BeEmpty(), "KUBECONFIG env var must be set")
		config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		Expect(err).NotTo(HaveOccurred())
		config.QPS = 500
		config.Burst = 1000
		c, err = client.New(config, client.Options{Scheme: scheme})
		Expect(err).NotTo(HaveOccurred())

		By("Creating storageclass")
		cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", common.LvmStorageClassFixture)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to apply storageclass")

		DeferCleanup(func(ctx context.Context) {
			By("Deleting storageclass")
			cmd := exec.CommandContext(ctx, "kubectl", "delete", "--wait", "--ignore-not-found", "-f", common.LvmStorageClassFixture)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete storageclass")
		})

	})

	Context("LVM CSI Driver", Label("lvm"), func() {
		statefulSetEphemeralDiskScaleTest("local", int32(*scale))
	})
})

func statefulSetEphemeralDiskScaleTest(storageClass string, scale int32) {

	var td testData

	BeforeEach(func() {
		td = testData{}
	})

	It("should scale statefulset with ephemeral volumes", func(ctx context.Context) {
		// Read the StatefulSet definition from the YAML file
		parentDir, err := utils.GetProjectDir()
		Expect(err).NotTo(HaveOccurred(), "failed to get project directory")
		data, err := os.ReadFile(filepath.Join(parentDir, ephemeralTemplate))
		Expect(err).NotTo(HaveOccurred(), "failed to read file %s", ephemeralTemplate)

		var ss appsv1.StatefulSet
		err = yaml.Unmarshal(data, &ss)
		Expect(err).NotTo(HaveOccurred(), "failed to unmarshal YAML")

		// Modify the StatefulSet as needed
		ss.Namespace = "scale-" + utils.RandomTag()
		ss.Spec.Replicas = new(scale)
		for i := range ss.Spec.Template.Spec.Volumes {
			if ss.Spec.Template.Spec.Volumes[i].Ephemeral != nil {
				ss.Spec.Template.Spec.Volumes[i].Ephemeral.VolumeClaimTemplate.Spec.StorageClassName = &storageClass
			}
		}

		// create ns
		ns := corev1.Namespace{
			ObjectMeta: ctrl.ObjectMeta{
				Name: ss.Namespace,
			},
		}
		err = c.Create(ctx, &ns)
		Expect(err).NotTo(HaveOccurred(), "failed to create Namespace")

		DeferCleanup(func(ctx context.Context) {
			err := c.Delete(ctx, &ns)
			Expect(err).NotTo(HaveOccurred(), "failed to delete Namespace")
		})

		DeferCleanup(func(ctx context.Context) {
			scaleIn(ctx, &ss, storageClass, &td)
		}, NodeTimeout(10*time.Minute))

		scaleOut(ctx, &ss, storageClass, &td)
	})

	ReportAfterEach(func(ctx SpecContext, r SpecReport) {
		// var timeElapsed = make([]float64, 0, len(td.scaleOutData))
		// var podCount = make([]opts.LineData, 0, len(td.scaleOutData))
		// var pvCount = make([]opts.LineData, 0, len(td.scaleOutData))
		// var volumeCount = make([]opts.LineData, 0, len(td.scaleOutData))
		// for _, entry := range td.scaleOutData {
		// 	timeElapsed = append(timeElapsed, entry.TimeElapsed.Seconds())
		// 	podCount = append(podCount, opts.LineData{Value: entry.Pods})
		// 	pvCount = append(pvCount, opts.LineData{Value: entry.PVs})
		// 	volumeCount = append(volumeCount, opts.LineData{Value: entry.Volumes})
		// }

		// _, _ = fmt.Fprintf(GinkgoWriter, "Scale Out Data: %v\n", timeElapsed)
		// _, _ = fmt.Fprintf(GinkgoWriter, "Pods: %v\n", podCount)
		// _, _ = fmt.Fprintf(GinkgoWriter, "PVs: %v\n", pvCount)
		// _, _ = fmt.Fprintf(GinkgoWriter, "Volumes: %v\n", volumeCount)
		// TODO(jlong): generate some charts for scale-out and scale-in data

	})

}

func scaleOut(ctx context.Context, ss *appsv1.StatefulSet, storageClass string, td *testData) {
	scaleStart := time.Now()
	err := c.Create(ctx, ss)
	Expect(err).NotTo(HaveOccurred(), "failed to create StatefulSet")
	scale := *ss.Spec.Replicas
	Eventually(func(g Gomega, ctx context.Context) {
		var pods corev1.PodList
		err := c.List(ctx, &pods, client.InNamespace(ss.Namespace))
		g.Expect(err).NotTo(HaveOccurred(), "failed to list Pods")
		runningCount := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning {
				runningCount++
			}
		}

		var pvList corev1.PersistentVolumeList
		err = c.List(ctx, &pvList)
		g.Expect(err).NotTo(HaveOccurred(), "failed to list PVs")
		boundCount := 0
		for _, pv := range pvList.Items {
			if pv.Status.Phase == corev1.VolumeBound && pv.Spec.StorageClassName == storageClass {
				boundCount++
			}
		}

		elapsed := time.Since(scaleStart)
		_, _ = fmt.Fprintf(GinkgoWriter, "Bound PVs: %d/%d, Running Pods: %d/%d, time elapsed: %s\n", boundCount, scale, runningCount, scale, elapsed)
		td.scaleOutData = append(td.scaleOutData, scaleData{elapsed, runningCount, boundCount})
		g.Expect(runningCount).Should(BeEquivalentTo(scale), "expected %d running Pods", scale)
		g.Expect(boundCount).Should(BeEquivalentTo(scale), "expected %d bound PVs", scale)
	}).WithContext(ctx).Should(Succeed(), "failed to scale StatefulSet")

}

func scaleIn(ctx context.Context, ss *appsv1.StatefulSet, storageClass string, td *testData) {
	scaleStart := time.Now()
	err := c.Delete(ctx, ss)
	Expect(err).NotTo(HaveOccurred(), "failed to delete StatefulSet")
	scale := *ss.Spec.Replicas
	Eventually(func(g Gomega, ctx context.Context) {
		var pods corev1.PodList
		err := c.List(ctx, &pods, client.InNamespace(ss.Namespace))
		g.Expect(err).NotTo(HaveOccurred(), "failed to list Pods")
		podCount := len(pods.Items)

		// wait for pv with storageclass to disappear
		var pvList corev1.PersistentVolumeList
		err = c.List(ctx, &pvList)
		g.Expect(err).NotTo(HaveOccurred(), "failed to list PVs")
		pvCount := 0
		for _, pv := range pvList.Items {
			if pv.Spec.StorageClassName == storageClass {
				pvCount++
			}
		}

		elapsed := time.Since(scaleStart)
		_, _ = fmt.Fprintf(GinkgoWriter, "PVs: %d/%d, Pods: %d/%d, time elapsed: %s\n", pvCount, scale, podCount, scale, elapsed)
		td.scaleInData = append(td.scaleInData, scaleData{elapsed, podCount, pvCount})
		g.Expect(podCount).Should(BeZero(), "expected no Pods")
		g.Expect(pvCount).Should(BeZero(), "expected no PVs with storageclass %s", storageClass)
	}).WithContext(ctx).Should(Succeed(), "failed to delete PVs")
}
