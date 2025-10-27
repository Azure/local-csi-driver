// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package hyperconverged

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"local-csi-driver/internal/csi"
	"local-csi-driver/internal/csi/core/lvm"
)

const (
	NodeLabel = "kubernetes.io/hostname"
)

var _ = Describe("When Hyperconverged controller is running", Serial, func() {

	var (
		testNamespace = GenNamespace("test")
		options       = client.ListOptions{
			Namespace: testNamespace.Name,
		}
		pods  = &corev1.PodList{}
		nodes = &corev1.NodeList{}
		scs   = &storagev1.StorageClassList{}
		pvcs  = &corev1.PersistentVolumeClaimList{}
		pvs   = &corev1.PersistentVolumeList{}
	)

	AfterEach(func() {
		// Delete any remaining pods
		Expect(k8sClient.List(ctx, pods, &options)).Should(Succeed())

		for _, pod := range pods.Items {
			options := client.DeleteOptions{GracePeriodSeconds: new(int64)}
			Expect(k8sClient.Delete(ctx, &pod, &options)).To(Succeed())
		}

		Eventually(func() bool {
			Expect(k8sClient.List(ctx, pods, &options)).Should(Succeed())
			return len(pods.Items) == 0
		}, 10*time.Second).Should(BeTrue())

		// Delete any remaining pvs
		Expect(k8sClient.List(ctx, pvs, &options)).Should(Succeed())

		for _, pv := range pvs.Items {
			pv.Finalizers = []string{}
			Expect(k8sClient.Update(ctx, &pv)).To(Succeed())
			Expect(k8sClient.Delete(ctx, &pv)).To(Succeed())
		}

		Eventually(func() bool {
			Expect(k8sClient.List(ctx, pvs, &options)).Should(Succeed())
			return len(pvs.Items) == 0
		}, 10*time.Second).Should(BeTrue())

		// Delete any remaining pvcs
		Expect(k8sClient.List(ctx, pvcs, &options)).Should(Succeed())

		for _, pvc := range pvcs.Items {
			pvc.Finalizers = []string{}
			Expect(k8sClient.Update(ctx, &pvc)).To(Succeed())
			Expect(k8sClient.Delete(ctx, &pvc)).To(Succeed())
		}

		Eventually(func() bool {
			Expect(k8sClient.List(ctx, pvcs, &options)).Should(Succeed())
			return len(pvcs.Items) == 0
		}, 10*time.Second).Should(BeTrue())

		// Delete any remaining Nodes
		Expect(k8sClient.List(ctx, nodes, &options)).Should(Succeed())

		for _, node := range nodes.Items {
			Expect(k8sClient.Delete(ctx, &node)).To(Succeed())
		}

		Eventually(func() bool {
			Expect(k8sClient.List(ctx, nodes, &options)).Should(Succeed())
			return len(nodes.Items) == 0
		}, 10*time.Second).Should(BeTrue())

		// Delete any remaining StorageClasses
		Expect(k8sClient.List(ctx, scs, &options)).Should(Succeed())

		for _, sc := range scs.Items {
			Expect(k8sClient.Delete(ctx, &sc)).To(Succeed())
		}

		Eventually(func() bool {
			Expect(k8sClient.List(ctx, scs, &options)).Should(Succeed())
			return len(scs.Items) == 0
		}, 10*time.Second).Should(BeTrue())
	})

	Context("When pod has no volumes", func() {
		It("Should allow pod to be created", func() {
			var (
				pod = GenPod(testNamespace.Name, nil, nil)
			)

			// Create Test Pod.
			By("Creating Test Pod")
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, pod).Should(Succeed())
		})
	})

	Context("When pod has no hyperconverged volumes", func() {
		It("Should allow pod to be created", func() {
			var (
				storageClass = GenStorageClass("test-sc", lvm.DriverName, nil)
				pvc          = GenPVC(storageClass.Name, testNamespace.Name, "32Gi")
				pod          *corev1.Pod
				volumes      []corev1.Volume
				volumeMounts []corev1.VolumeMount
			)

			By("Creating Test Storage Class")
			Expect(k8sClient.Create(ctx, storageClass)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Name: storageClass.Name}, storageClass).Should(Succeed())

			By("Creating Test PVC")
			Expect(k8sClient.Create(ctx, pvc)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Namespace: pvc.Namespace, Name: pvc.Name}, pvc).Should(Succeed())

			volumes = []corev1.Volume{
				{
					Name: "test-volume",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: pvc.Name,
						},
					},
				},
			}
			volumeMounts = []corev1.VolumeMount{
				{
					Name:      volumes[0].Name,
					MountPath: "/mnt/test",
				},
			}
			pod = GenPod(testNamespace.Name, volumes, volumeMounts)

			// Create Test Pod.
			By("Creating Test Pod")
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, pod).Should(Succeed())
		})
	})

	Context("When pod has hyperconverged volume", func() {
		It("Should allow pod to be created and mutate pod with node affinity", func() {
			var (
				firstParams = map[string]string{
					HyperconvergedParam: "true",
				}
				numNodes          = 3
				firstStorageClass = GenStorageClass("test-sc", lvm.DriverName, firstParams)
				firstPVC          = GenPVC(firstStorageClass.Name, testNamespace.Name, "32Gi")
				firstPod          *corev1.Pod
				secondPod         *corev1.Pod
				volumes           []corev1.Volume
				volumeMounts      []corev1.VolumeMount
			)

			for i := 1; i <= numNodes; i++ {
				By(fmt.Sprintf("Creating Test Node %d", i))
				node := GenNode()
				Expect(k8sClient.Create(ctx, node)).To(Succeed())
				Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Name: node.Name}, node).Should(Succeed())
			}

			By("Creating First Test Storage Class")
			Expect(k8sClient.Create(ctx, firstStorageClass)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Name: firstStorageClass.Name}, firstStorageClass).Should(Succeed())

			By("Creating First Test PVC")
			Expect(k8sClient.Create(ctx, firstPVC)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Namespace: firstPVC.Namespace, Name: firstPVC.Name}, firstPVC).Should(Succeed())

			volumes = []corev1.Volume{
				{
					Name: "test-volume-1",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: firstPVC.Name,
						},
					},
				},
			}
			volumeMounts = []corev1.VolumeMount{
				{
					Name:      volumes[0].Name,
					MountPath: "/mnt/test1",
				},
			}
			firstPod = GenPod(testNamespace.Name, volumes, volumeMounts)

			// Create Test Pod.
			By("Creating Test Pod")
			Expect(k8sClient.Create(ctx, firstPod)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Namespace: firstPod.Namespace, Name: firstPod.Name}, firstPod).Should(Succeed())

			// Verify pod has node no affinity on initial create.
			By("Verifying Pod has no Node Affinity")
			foundNodes := GetPreferredNodeAffinityValues(firstPod.Spec.Affinity)
			Expect(foundNodes).To(BeEmpty())

			firstPV := GenPersistentVolume(firstPVC.Spec.VolumeName, testNamespace.Name, "containerstorage.csi.azure.com", "test-node")

			By("Creating PV")
			Expect(k8sClient.Create(ctx, firstPV)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Name: firstPV.Name}, firstPV).Should(Succeed())

			Expect(k8sClient.Delete(ctx, firstPod)).To(Succeed())

			secondPod = GenPod(testNamespace.Name, volumes, volumeMounts)

			// Create Test Pod.
			By("Creating Second Test Pod")
			Expect(k8sClient.Create(ctx, secondPod)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Namespace: secondPod.Namespace, Name: secondPod.Name}, secondPod).Should(Succeed())

			// Verify pod has node affinity.
			By("Verifying Second Pod has Node Affinity")
			foundNodes = GetPreferredNodeAffinityValues(secondPod.Spec.Affinity)
			Expect(foundNodes).To(HaveLen(1))
		})
	})

	Context("When pod has hyperconverged volume with availability failover mode", func() {
		It("Should create pod with preferred node affinity", func() {
			// Test verifies that when failover-mode is set to "availability",
			// the webhook applies preferred node affinity, allowing pods to be
			// scheduled on other nodes if the preferred storage nodes are unavailable
			var (
				availabilityParams = map[string]string{
					HyperconvergedParam: "true",
					FailoverModeParam:   FailoverModeAvailability,
				}
				numNodes                 = 2
				availabilityStorageClass = GenStorageClass("test-sc-availability", lvm.DriverName, availabilityParams)
				availabilityPVC          = GenPVC(availabilityStorageClass.Name, testNamespace.Name, "16Gi")
				availabilityPod          *corev1.Pod
				volumes                  []corev1.Volume
				volumeMounts             []corev1.VolumeMount
			)

			for i := 1; i <= numNodes; i++ {
				By(fmt.Sprintf("Creating Test Node %d for availability mode", i))
				node := GenNode()
				Expect(k8sClient.Create(ctx, node)).To(Succeed())
				Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Name: node.Name}, node).Should(Succeed())
			}

			By("Creating Availability Mode Storage Class")
			Expect(k8sClient.Create(ctx, availabilityStorageClass)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Name: availabilityStorageClass.Name}, availabilityStorageClass).Should(Succeed())

			By("Creating Availability Mode PVC")
			Expect(k8sClient.Create(ctx, availabilityPVC)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Namespace: availabilityPVC.Namespace, Name: availabilityPVC.Name}, availabilityPVC).Should(Succeed())

			// Create corresponding PV with failover mode in volume attributes
			availabilityPV := GenPersistentVolume(availabilityPVC.Spec.VolumeName, testNamespace.Name, lvm.DriverName, "test-node-1")
			availabilityPV.Spec.CSI.VolumeAttributes[FailoverModeParam] = FailoverModeAvailability

			By("Creating PV with availability failover mode")
			Expect(k8sClient.Create(ctx, availabilityPV)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Name: availabilityPV.Name}, availabilityPV).Should(Succeed())

			volumes = []corev1.Volume{
				{
					Name: "test-volume-availability",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: availabilityPVC.Name,
						},
					},
				},
			}
			volumeMounts = []corev1.VolumeMount{
				{
					Name:      volumes[0].Name,
					MountPath: "/mnt/availability",
				},
			}
			availabilityPod = GenPod(testNamespace.Name, volumes, volumeMounts)

			By("Creating Pod with availability mode volume")
			Expect(k8sClient.Create(ctx, availabilityPod)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Namespace: availabilityPod.Namespace, Name: availabilityPod.Name}, availabilityPod).Should(Succeed())

			By("Verifying Pod has preferred node affinity (not required)")
			preferredNodes := GetPreferredNodeAffinityValues(availabilityPod.Spec.Affinity)
			requiredNodes := GetRequiredNodeAffinityValues(availabilityPod.Spec.Affinity)

			Expect(preferredNodes).To(HaveLen(1))
			Expect(preferredNodes).To(HaveKey("test-node-1"))
			Expect(requiredNodes).To(BeEmpty()) // Should not have required affinity
		})
	})

	Context("When pod has hyperconverged volume with durability failover mode", func() {
		It("Should create pod with required node affinity", func() {
			// Test verifies that when failover-mode is set to "durability",
			// the webhook applies required node affinity, ensuring pods can only
			// be scheduled on nodes that have the required storage available
			var (
				durabilityParams = map[string]string{
					HyperconvergedParam: "true",
					FailoverModeParam:   FailoverModeDurability,
				}
				numNodes               = 2
				durabilityStorageClass = GenStorageClass("test-sc-durability", lvm.DriverName, durabilityParams)
				durabilityPVC          = GenPVC(durabilityStorageClass.Name, testNamespace.Name, "16Gi")
				durabilityPod          *corev1.Pod
				volumes                []corev1.Volume
				volumeMounts           []corev1.VolumeMount
			)

			for i := 1; i <= numNodes; i++ {
				By(fmt.Sprintf("Creating Test Node %d for durability mode", i))
				node := GenNode()
				Expect(k8sClient.Create(ctx, node)).To(Succeed())
				Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Name: node.Name}, node).Should(Succeed())
			}

			By("Creating Durability Mode Storage Class")
			Expect(k8sClient.Create(ctx, durabilityStorageClass)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Name: durabilityStorageClass.Name}, durabilityStorageClass).Should(Succeed())

			By("Creating Durability Mode PVC")
			Expect(k8sClient.Create(ctx, durabilityPVC)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Namespace: durabilityPVC.Namespace, Name: durabilityPVC.Name}, durabilityPVC).Should(Succeed())

			// Create corresponding PV with failover mode in volume attributes
			durabilityPV := GenPersistentVolume(durabilityPVC.Spec.VolumeName, testNamespace.Name, lvm.DriverName, "test-node-2")
			durabilityPV.Spec.CSI.VolumeAttributes[FailoverModeParam] = FailoverModeDurability

			By("Creating PV with durability failover mode")
			Expect(k8sClient.Create(ctx, durabilityPV)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Name: durabilityPV.Name}, durabilityPV).Should(Succeed())

			volumes = []corev1.Volume{
				{
					Name: "test-volume-durability",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: durabilityPVC.Name,
						},
					},
				},
			}
			volumeMounts = []corev1.VolumeMount{
				{
					Name:      volumes[0].Name,
					MountPath: "/mnt/durability",
				},
			}
			durabilityPod = GenPod(testNamespace.Name, volumes, volumeMounts)

			By("Creating Pod with durability mode volume")
			Expect(k8sClient.Create(ctx, durabilityPod)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Namespace: durabilityPod.Namespace, Name: durabilityPod.Name}, durabilityPod).Should(Succeed())

			By("Verifying Pod has required node affinity (not preferred)")
			preferredNodes := GetPreferredNodeAffinityValues(durabilityPod.Spec.Affinity)
			requiredNodes := GetRequiredNodeAffinityValues(durabilityPod.Spec.Affinity)

			Expect(requiredNodes).To(HaveLen(1))
			Expect(requiredNodes).To(ContainElement("test-node-2"))
			Expect(preferredNodes).To(BeEmpty()) // Should not have preferred affinity
		})
	})

	Context("When pod has hyperconverged volume with invalid failover mode", func() {
		It("Should create pod with preferred node affinity (default behavior)", func() {
			// Test verifies that when an invalid failover-mode is specified,
			// the webhook falls back to the default behavior of preferred node affinity
			var (
				invalidParams = map[string]string{
					HyperconvergedParam: "true",
					FailoverModeParam:   "invalid-mode",
				}
				numNodes            = 2
				invalidStorageClass = GenStorageClass("test-sc-invalid", lvm.DriverName, invalidParams)
				invalidPVC          = GenPVC(invalidStorageClass.Name, testNamespace.Name, "16Gi")
				invalidPod          *corev1.Pod
				volumes             []corev1.Volume
				volumeMounts        []corev1.VolumeMount
			)

			for i := 1; i <= numNodes; i++ {
				By(fmt.Sprintf("Creating Test Node %d for invalid mode", i))
				node := GenNode()
				Expect(k8sClient.Create(ctx, node)).To(Succeed())
				Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Name: node.Name}, node).Should(Succeed())
			}

			By("Creating Invalid Mode Storage Class")
			Expect(k8sClient.Create(ctx, invalidStorageClass)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Name: invalidStorageClass.Name}, invalidStorageClass).Should(Succeed())

			By("Creating Invalid Mode PVC")
			Expect(k8sClient.Create(ctx, invalidPVC)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Namespace: invalidPVC.Namespace, Name: invalidPVC.Name}, invalidPVC).Should(Succeed())

			// Create corresponding PV with invalid failover mode in volume attributes
			invalidPV := GenPersistentVolume(invalidPVC.Spec.VolumeName, testNamespace.Name, lvm.DriverName, "test-node-3")
			invalidPV.Spec.CSI.VolumeAttributes[FailoverModeParam] = "invalid-mode"

			By("Creating PV with invalid failover mode")
			Expect(k8sClient.Create(ctx, invalidPV)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Name: invalidPV.Name}, invalidPV).Should(Succeed())

			volumes = []corev1.Volume{
				{
					Name: "test-volume-invalid",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: invalidPVC.Name,
						},
					},
				},
			}
			volumeMounts = []corev1.VolumeMount{
				{
					Name:      volumes[0].Name,
					MountPath: "/mnt/invalid",
				},
			}
			invalidPod = GenPod(testNamespace.Name, volumes, volumeMounts)

			By("Creating Pod with invalid mode volume")
			Expect(k8sClient.Create(ctx, invalidPod)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Namespace: invalidPod.Namespace, Name: invalidPod.Name}, invalidPod).Should(Succeed())

			By("Verifying Pod falls back to preferred node affinity (default behavior)")
			preferredNodes := GetPreferredNodeAffinityValues(invalidPod.Spec.Affinity)
			requiredNodes := GetRequiredNodeAffinityValues(invalidPod.Spec.Affinity)

			Expect(preferredNodes).To(HaveLen(1))
			Expect(preferredNodes).To(HaveKey("test-node-3"))
			Expect(requiredNodes).To(BeEmpty()) // Should fallback to preferred (default)
		})
	})

	Context("When pod has multiple hyperconverged volumes with mixed failover modes", func() {
		It("Should handle mixed failover modes correctly", func() {
			// Test verifies that when a pod has multiple volumes with different failover modes,
			// the webhook correctly processes each volume and applies the appropriate affinity.
			// When durability mode is present, it should result in required affinity
			var (
				availabilityParams = map[string]string{
					HyperconvergedParam: "true",
					FailoverModeParam:   FailoverModeAvailability,
				}
				durabilityParams = map[string]string{
					HyperconvergedParam: "true",
					FailoverModeParam:   FailoverModeDurability,
				}
				numNodes                 = 3
				availabilityStorageClass = GenStorageClass("test-sc-mixed-avail", lvm.DriverName, availabilityParams)
				durabilityStorageClass   = GenStorageClass("test-sc-mixed-dur", lvm.DriverName, durabilityParams)
				availabilityPVC          = GenPVC(availabilityStorageClass.Name, testNamespace.Name, "8Gi")
				durabilityPVC            = GenPVC(durabilityStorageClass.Name, testNamespace.Name, "8Gi")
				mixedPod                 *corev1.Pod
				volumes                  []corev1.Volume
				volumeMounts             []corev1.VolumeMount
			)

			for i := 1; i <= numNodes; i++ {
				By(fmt.Sprintf("Creating Test Node %d for mixed modes", i))
				node := GenNode()
				Expect(k8sClient.Create(ctx, node)).To(Succeed())
				Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Name: node.Name}, node).Should(Succeed())
			}

			By("Creating Mixed Mode Storage Classes")
			Expect(k8sClient.Create(ctx, availabilityStorageClass)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Name: availabilityStorageClass.Name}, availabilityStorageClass).Should(Succeed())
			Expect(k8sClient.Create(ctx, durabilityStorageClass)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Name: durabilityStorageClass.Name}, durabilityStorageClass).Should(Succeed())

			By("Creating Mixed Mode PVCs")
			Expect(k8sClient.Create(ctx, availabilityPVC)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Namespace: availabilityPVC.Namespace, Name: availabilityPVC.Name}, availabilityPVC).Should(Succeed())
			Expect(k8sClient.Create(ctx, durabilityPVC)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Namespace: durabilityPVC.Namespace, Name: durabilityPVC.Name}, durabilityPVC).Should(Succeed())

			// Create corresponding PVs with different failover modes
			availabilityPV := GenPersistentVolume(availabilityPVC.Spec.VolumeName, testNamespace.Name, lvm.DriverName, "test-node-avail")
			availabilityPV.Spec.CSI.VolumeAttributes[FailoverModeParam] = FailoverModeAvailability

			durabilityPV := GenPersistentVolume(durabilityPVC.Spec.VolumeName, testNamespace.Name, lvm.DriverName, "test-node-dur")
			durabilityPV.Spec.CSI.VolumeAttributes[FailoverModeParam] = FailoverModeDurability

			By("Creating PVs with mixed failover modes")
			Expect(k8sClient.Create(ctx, availabilityPV)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Name: availabilityPV.Name}, availabilityPV).Should(Succeed())
			Expect(k8sClient.Create(ctx, durabilityPV)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Name: durabilityPV.Name}, durabilityPV).Should(Succeed())

			volumes = []corev1.Volume{
				{
					Name: "test-volume-mixed-avail",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: availabilityPVC.Name,
						},
					},
				},
				{
					Name: "test-volume-mixed-dur",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: durabilityPVC.Name,
						},
					},
				},
			}
			volumeMounts = []corev1.VolumeMount{
				{
					Name:      volumes[0].Name,
					MountPath: "/mnt/mixed-avail",
				},
				{
					Name:      volumes[1].Name,
					MountPath: "/mnt/mixed-dur",
				},
			}
			mixedPod = GenPod(testNamespace.Name, volumes, volumeMounts)

			By("Creating Pod with mixed failover mode volumes")
			Expect(k8sClient.Create(ctx, mixedPod)).To(Succeed())
			Eventually(k8sClient.Get).WithArguments(ctx, types.NamespacedName{Namespace: mixedPod.Namespace, Name: mixedPod.Name}, mixedPod).Should(Succeed())

			By("Verifying Pod has required node affinity (durability mode takes precedence)")
			requiredNodes := GetRequiredNodeAffinityValues(mixedPod.Spec.Affinity)

			// When mixed modes exist, durability (required) should take precedence
			Expect(requiredNodes).ToNot(BeEmpty())
			Expect(requiredNodes).To(ContainElement("test-node-dur"))
		})
	})
})

// Generate Node Affinity.
func GenNodeAffinity(nodeNames map[string]int) *corev1.Affinity {
	schedulingTerms := []corev1.PreferredSchedulingTerm{}
	for nodeName, weight := range nodeNames {
		schedulingTerm := corev1.PreferredSchedulingTerm{
			Weight: int32(weight),
			Preference: corev1.NodeSelectorTerm{
				MatchExpressions: []corev1.NodeSelectorRequirement{
					{
						Key:      KubernetesNodeHostNameLabel,
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{nodeName},
					},
				},
			},
		}
		schedulingTerms = append(schedulingTerms, schedulingTerm)
	}

	return &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: schedulingTerms,
		},
	}
}

// Get Values of MatchExpression with key NodeLabel from affinity.
func GetPreferredNodeAffinityValues(affinity *corev1.Affinity) map[string]int {
	nodeNames := make(map[string]int)
	if affinity == nil || affinity.NodeAffinity == nil {
		return nodeNames
	}
	for _, preferredSchedulingTerm := range affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution {
		for _, matchExpression := range preferredSchedulingTerm.Preference.MatchExpressions {
			if matchExpression.Key == NodeLabel {
				for _, nodeName := range matchExpression.Values {
					nodeNames[nodeName] = int(preferredSchedulingTerm.Weight)
				}
			}
		}
	}

	return nodeNames
}

// Get Values of MatchExpression with key NodeLabel from affinity.
func GetRequiredNodeAffinityValues(affinity *corev1.Affinity) []string {
	nodeNames := []string{}
	if affinity != nil && affinity.NodeAffinity != nil && affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		for _, requiredSchedulingTerm := range affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
			for _, matchExpression := range requiredSchedulingTerm.MatchExpressions {
				if matchExpression.Key == NodeLabel {
					nodeNames = append(nodeNames, matchExpression.Values...)
				}
			}
		}
	}
	return nodeNames
}

// GenNamespace Test Namespace.
func GenNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
}

// GenNode generates a node for testing.
func GenNode() *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "node-",
			Labels:       map[string]string{},
		},
	}
}

// GenStorageClass generates a storage class for testing.
func GenStorageClass(scName, provisioner string, params map[string]string) *storagev1.StorageClass {
	return &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: scName,
		},
		Provisioner: provisioner,
		Parameters:  params,
	}
}

// GenPVC generates a persistent volume claim for testing.
func GenPVC(scName, namespace, requestStorage string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "pvc-",
			Namespace:    namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(requestStorage),
				},
			},
			StorageClassName: &scName,
			VolumeName:       uuid.NewString(),
		},
	}
}

// GenPod generates a pod for testing.
func GenPod(namespace string, volumes []corev1.Volume, volumeMounts []corev1.VolumeMount) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "pod-",
			Namespace:    namespace,
		},
		Spec: corev1.PodSpec{
			Volumes: volumes,
			Containers: []corev1.Container{
				{
					Name:  "test",
					Image: "busybox",
					Args: []string{
						"tail",
						"-f",
						"/dev/null",
					},
					VolumeMounts: volumeMounts,
				},
			},
		},
	}
}

// GenPersistentVolume generates a persistent volume for testing.
func GenPersistentVolume(name, namespace, driver, nodeName string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver: driver,
					VolumeAttributes: map[string]string{
						"csi.storage.k8s.io/pvc/namespace": namespace,
						csi.SelectedInitialNodeParam:       nodeName,
					},
					VolumeHandle: uuid.NewString(),
				},
			},
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse("1Gi"),
			},
			ClaimRef: &corev1.ObjectReference{
				Namespace: namespace,
				Name:      name,
			},
		},
	}
}
