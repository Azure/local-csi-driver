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
				firstParams       = map[string]string{}
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
