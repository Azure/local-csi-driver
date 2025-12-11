// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package pvcleanup

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"local-csi-driver/internal/csi/core/lvm"
)

var (
	cfg       *rest.Config
	testEnv   *envtest.Environment
	k8sClient client.Client
	recorder  *record.FakeRecorder
	ctx       context.Context
	cancel    context.CancelFunc
)

func TestPVCleanupController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "PV Cleanup Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	ctx, cancel = context.WithCancel(context.TODO())

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: false,
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	err = scheme.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	recorder = record.NewFakeRecorder(100)

	// Start the controller
	k8sManager, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	Expect(err).ToNot(HaveOccurred())

	err = (&PVCleanupReconciler{
		Client:   k8sManager.GetClient(),
		Recorder: recorder,
	}).SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	go func() {
		defer GinkgoRecover()
		err = k8sManager.Start(ctx)
		Expect(err).ToNot(HaveOccurred(), "failed to run manager")
	}()
})

var _ = AfterSuite(func() {
	cancel()
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})

var _ = Describe("PV Cleanup Controller", func() {
	var (
		node1 *corev1.Node
		node2 *corev1.Node
	)

	BeforeEach(func() {
		// Create test nodes
		node1 = &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-node-1",
			},
			Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{
					{
						Type:   corev1.NodeReady,
						Status: corev1.ConditionTrue,
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, node1)).To(Succeed())

		node2 = &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-node-2",
			},
			Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{
					{
						Type:   corev1.NodeReady,
						Status: corev1.ConditionTrue,
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, node2)).To(Succeed())
	})

	AfterEach(func() {
		// Clean up nodes
		if node1 != nil {
			_ = k8sClient.Delete(ctx, node1)
		}
		if node2 != nil {
			_ = k8sClient.Delete(ctx, node2)
		}
	})

	Context("When a PV is released", func() {
		It("Should remove finalizers when node is not available", func() {
			pv := &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-no-node",
					Finalizers: []string{
						PVProtectionFinalizer,
						ExternalProvisionerFinalizer,
					},
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("10Gi"),
					},
					PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteOnce,
					},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver:       lvm.DriverName,
							VolumeHandle: "test-volume-1",
						},
					},
					NodeAffinity: &corev1.VolumeNodeAffinity{
						Required: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{
											Key:      lvm.TopologyKey,
											Operator: corev1.NodeSelectorOpIn,
											Values:   []string{"non-existent-node"},
										},
									},
								},
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, pv)).To(Succeed())

			// Update status to Released to trigger reconciliation
			pv.Status.Phase = corev1.VolumeReleased
			Expect(k8sClient.Status().Update(ctx, pv)).To(Succeed())

			// Wait for controller to process, remove finalizers or delete the PV
			Eventually(func() bool {
				var updatedPV corev1.PersistentVolume
				err := k8sClient.Get(ctx, types.NamespacedName{Name: pv.Name}, &updatedPV)
				if err != nil {
					// PV not found means it was successfully deleted (finalizers removed)
					return apierrors.IsNotFound(err)
				}
				// PV still exists, check if finalizers are removed
				return len(updatedPV.Finalizers) == 0
			}, time.Second*10, time.Millisecond*250).Should(BeTrue())

			// Clean up (if PV still exists)
			_ = k8sClient.Delete(ctx, pv)
		})

		It("Should keep finalizers when node is available", func() {
			pv := &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-with-node",
					Finalizers: []string{
						PVProtectionFinalizer,
						ExternalProvisionerFinalizer,
					},
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("10Gi"),
					},
					PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteOnce,
					},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver:       lvm.DriverName,
							VolumeHandle: "test-volume-2",
						},
					},
					NodeAffinity: &corev1.VolumeNodeAffinity{
						Required: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{
											Key:      lvm.TopologyKey,
											Operator: corev1.NodeSelectorOpIn,
											Values:   []string{"test-node-1"},
										},
									},
								},
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, pv)).To(Succeed())

			// Update status to Released to trigger reconciliation
			pv.Status.Phase = corev1.VolumeReleased
			Expect(k8sClient.Status().Update(ctx, pv)).To(Succeed())

			// Wait a bit to ensure controller has time to process
			Consistently(func() bool {
				var updatedPV corev1.PersistentVolume
				err := k8sClient.Get(ctx, types.NamespacedName{Name: pv.Name}, &updatedPV)
				if err != nil {
					return false
				}
				return len(updatedPV.Finalizers) == 2 &&
					ctrlutil.ContainsFinalizer(&updatedPV, PVProtectionFinalizer) &&
					ctrlutil.ContainsFinalizer(&updatedPV, ExternalProvisionerFinalizer)
			}, time.Second*2, time.Millisecond*250).Should(BeTrue())

			// Clean up
			_ = k8sClient.Delete(ctx, pv)
		})

		It("Should not process PV with Retain reclaim policy", func() {
			pv := &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-retain",
					Finalizers: []string{
						PVProtectionFinalizer,
					},
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("10Gi"),
					},
					PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteOnce,
					},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver:       lvm.DriverName,
							VolumeHandle: "test-volume-3",
						},
					},
					NodeAffinity: &corev1.VolumeNodeAffinity{
						Required: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{
											Key:      lvm.TopologyKey,
											Operator: corev1.NodeSelectorOpIn,
											Values:   []string{"non-existent-node"},
										},
									},
								},
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, pv)).To(Succeed())

			// Update status to Released to trigger reconciliation check
			pv.Status.Phase = corev1.VolumeReleased
			Expect(k8sClient.Status().Update(ctx, pv)).To(Succeed())

			// Verify finalizers are still present (not removed)
			Consistently(func() bool {
				var updatedPV corev1.PersistentVolume
				err := k8sClient.Get(ctx, types.NamespacedName{Name: pv.Name}, &updatedPV)
				if err != nil {
					return false
				}
				return ctrlutil.ContainsFinalizer(&updatedPV, PVProtectionFinalizer)
			}, time.Second*2, time.Millisecond*250).Should(BeTrue())

			// Clean up
			_ = k8sClient.Delete(ctx, pv)
		})

		It("Should not process PV that is not Released", func() {
			pv := &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-bound",
					Finalizers: []string{
						PVProtectionFinalizer,
					},
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("10Gi"),
					},
					PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteOnce,
					},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver:       lvm.DriverName,
							VolumeHandle: "test-volume-4",
						},
					},
					NodeAffinity: &corev1.VolumeNodeAffinity{
						Required: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{
											Key:      lvm.TopologyKey,
											Operator: corev1.NodeSelectorOpIn,
											Values:   []string{"non-existent-node"},
										},
									},
								},
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, pv)).To(Succeed())

			// Wait a bit to ensure controller has time to process
			Consistently(func() bool {
				var updatedPV corev1.PersistentVolume
				err := k8sClient.Get(ctx, types.NamespacedName{Name: pv.Name}, &updatedPV)
				if err != nil {
					return false
				}
				return ctrlutil.ContainsFinalizer(&updatedPV, PVProtectionFinalizer)
			}, time.Second*2, time.Millisecond*250).Should(BeTrue())

			// Clean up
			_ = k8sClient.Delete(ctx, pv)
		})

		It("Should not process PV from different CSI driver", func() {
			pv := &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-different-driver",
					Finalizers: []string{
						PVProtectionFinalizer,
					},
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("10Gi"),
					},
					PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteOnce,
					},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver:       "some-other-csi-driver",
							VolumeHandle: "test-volume-5",
						},
					},
					NodeAffinity: &corev1.VolumeNodeAffinity{
						Required: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{
											Key:      lvm.TopologyKey,
											Operator: corev1.NodeSelectorOpIn,
											Values:   []string{"non-existent-node"},
										},
									},
								},
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, pv)).To(Succeed())

			// Update status to Released to trigger reconciliation check
			pv.Status.Phase = corev1.VolumeReleased
			Expect(k8sClient.Status().Update(ctx, pv)).To(Succeed())

			// Verify finalizers are still present (not removed)
			Consistently(func() bool {
				var updatedPV corev1.PersistentVolume
				err := k8sClient.Get(ctx, types.NamespacedName{Name: pv.Name}, &updatedPV)
				if err != nil {
					return false
				}
				return ctrlutil.ContainsFinalizer(&updatedPV, PVProtectionFinalizer)
			}, time.Second*2, time.Millisecond*250).Should(BeTrue())

			// Clean up
			_ = k8sClient.Delete(ctx, pv)
		})

		It("Should keep finalizers when one of multiple nodes is available", func() {
			pv := &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-multiple-nodes",
					Finalizers: []string{
						PVProtectionFinalizer,
						ExternalProvisionerFinalizer,
					},
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("10Gi"),
					},
					PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteOnce,
					},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver:       lvm.DriverName,
							VolumeHandle: "test-volume-6",
						},
					},
					NodeAffinity: &corev1.VolumeNodeAffinity{
						Required: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{
											Key:      lvm.TopologyKey,
											Operator: corev1.NodeSelectorOpIn,
											Values:   []string{"non-existent-node", "test-node-1"},
										},
									},
								},
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, pv)).To(Succeed())

			// Update status to Released to trigger reconciliation
			pv.Status.Phase = corev1.VolumeReleased
			Expect(k8sClient.Status().Update(ctx, pv)).To(Succeed())

			// Wait a bit to ensure controller has time to process
			Consistently(func() bool {
				var updatedPV corev1.PersistentVolume
				err := k8sClient.Get(ctx, types.NamespacedName{Name: pv.Name}, &updatedPV)
				if err != nil {
					return false
				}
				return len(updatedPV.Finalizers) == 2 &&
					ctrlutil.ContainsFinalizer(&updatedPV, PVProtectionFinalizer) &&
					ctrlutil.ContainsFinalizer(&updatedPV, ExternalProvisionerFinalizer)
			}, time.Second*2, time.Millisecond*250).Should(BeTrue())

			// Clean up
			_ = k8sClient.Delete(ctx, pv)
		})

		It("Should remove finalizers when node exists but is not ready", func() {
			// Create a not-ready node
			notReadyNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node-not-ready",
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{
							Type:   corev1.NodeReady,
							Status: corev1.ConditionFalse,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, notReadyNode)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, notReadyNode)
			}()

			pv := &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-node-not-ready",
					Finalizers: []string{
						PVProtectionFinalizer,
						ExternalProvisionerFinalizer,
					},
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("10Gi"),
					},
					PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteOnce,
					},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver:       lvm.DriverName,
							VolumeHandle: "test-volume-7",
						},
					},
					NodeAffinity: &corev1.VolumeNodeAffinity{
						Required: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{
											Key:      lvm.TopologyKey,
											Operator: corev1.NodeSelectorOpIn,
											Values:   []string{"test-node-not-ready"},
										},
									},
								},
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, pv)).To(Succeed())

			// Update status to Released to trigger reconciliation
			pv.Status.Phase = corev1.VolumeReleased
			Expect(k8sClient.Status().Update(ctx, pv)).To(Succeed())

			// Wait for controller to process and either remove finalizers or delete the PV
			// After the fix, the controller issues a delete and removes finalizers,
			// so the PV will be fully deleted
			Eventually(func() bool {
				var updatedPV corev1.PersistentVolume
				err := k8sClient.Get(ctx, types.NamespacedName{Name: pv.Name}, &updatedPV)
				if err != nil {
					// PV not found means it was successfully deleted (finalizers removed)
					return apierrors.IsNotFound(err)
				}
				// PV still exists, check if finalizers are removed
				return len(updatedPV.Finalizers) == 0
			}, time.Second*10, time.Millisecond*250).Should(BeTrue())

			// Clean up (if PV still exists)
			_ = k8sClient.Delete(ctx, pv)
		})
	})
})
