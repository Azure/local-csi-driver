// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package enforceEphemeral

import (
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	testStorageClassName = "test-sc"
)

var _ = Describe("When EnforceEphemeral webhook is enabled", Serial, func() {
	const (
		timeout  = time.Minute * 3
		interval = time.Millisecond * 250
	)

	var (
		options client.ListOptions
		pvc     *corev1.PersistentVolumeClaim
		sc      *storagev1.StorageClass
	)

	BeforeEach(func() {
		options = client.ListOptions{
			Namespace: Namespace,
		}
		pvc = &corev1.PersistentVolumeClaim{}
		sc = &storagev1.StorageClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: testStorageClassName,
			},
		}
	})

	AfterEach(func() {
		// cleanup pvcs
		pvcs := &corev1.PersistentVolumeClaimList{}
		Expect(k8sClient.List(ctx, pvcs, &options)).Should(Succeed())
		for _, pvc := range pvcs.Items {
			pvc.Finalizers = []string{}
			Expect(k8sClient.Update(ctx, &pvc)).To(Succeed())
			Expect(k8sClient.Delete(ctx, &pvc)).To(Succeed())
		}
		Eventually(func() int {
			Expect(k8sClient.List(ctx, pvcs, &options)).Should(Succeed())
			return len(pvcs.Items)
		}, timeout, interval).Should(BeNumerically("==", 0))

		// cleanup storage class
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, sc))).Should(Succeed())
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: testStorageClassName}, sc))
		}, timeout, interval).Should(BeTrue())

		// Reset the recorder.
		recorder.Events = make(chan string, 10)
	})

	Context("When using non-acstor storage classes", func() {
		It("Should allow the pvc", func() {
			By("Creating a non-acstor storage class")
			sc = GenStorageClass(testStorageClassName, "disk.csi.azure.com", nil)
			Expect(k8sClient.Create(ctx, sc)).Should(Succeed())
			Eventually(storageClassExists(sc.Name)).Should(Succeed(), "StorageClass %s should exist before proceeding", sc.Name)

			By("Creating a pvc")
			pvc = GenPVC(testStorageClassName, Namespace, "1Gi")
			Expect(k8sClient.Create(ctx, pvc)).Should(Succeed())
		})
	})

	Context("When the pvc's owner's Kind isn't Pod and EphemeralDiskVolumeType is EphemeralVolumeOnly", func() {
		It("Should disallow the pvc", func() {
			By("Creating an acstor storage class")
			sc = GenStorageClass(testStorageClassName, DriverName, nil)
			Expect(k8sClient.Create(ctx, sc)).Should(Succeed())
			Eventually(storageClassExists(sc.Name)).Should(Succeed(), "StorageClass %s should exist before proceeding", sc.Name)
			By("Creating a pvc")
			pvc = GenPVC(testStorageClassName, Namespace, "1Gi")
			err := k8sClient.Create(ctx, pvc)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).Should(ContainSubstring("only generic ephemeral volumes are allowed for localdisk.csi.acstor.io provisioner"))

		})
	})

	Context("When the pvc is missing the annotation of accept-ephemeral-storage and it's owner's Kind isn't Pod and EphemeralDiskVolumeType is PersistentVolumeWithAnnotation", func() {
		It("Should disallow the pvc", func() {
			By("Creating an acstor storage class")
			sc = GenStorageClass(testStorageClassName, DriverName, nil)
			Expect(k8sClient.Create(ctx, sc)).Should(Succeed())
			Eventually(storageClassExists(sc.Name)).Should(Succeed(), "StorageClass %s should exist before proceeding", sc.Name)
			By("Creating a pvc")
			pvc = GenPVC(testStorageClassName, Namespace, "1Gi")
			err := k8sClient.Create(ctx, pvc)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).Should(ContainSubstring("only generic ephemeral volumes are allowed for localdisk.csi.acstor.io provisioner"))
		})
	})

	Context("When the pvc's owner's Kind is Pod", func() {
		It("Should allow the pvc", func() {
			By("Creating an acstor storage class")
			sc = GenStorageClass(testStorageClassName, DriverName, nil)
			Expect(k8sClient.Create(ctx, sc)).Should(Succeed())
			Eventually(storageClassExists(sc.Name)).Should(Succeed(), "StorageClass %s should exist before proceeding", sc.Name)

			By("Creating a pvc with owner of Kind Pod")
			pvc = GenPVC(testStorageClassName, Namespace, "1Gi")
			pvc.SetOwnerReferences([]metav1.OwnerReference{
				{
					APIVersion: "v1",
					Kind:       "Pod",
					Name:       "test-pvc",
					UID:        "1234567890",
				},
			})
			Expect(k8sClient.Create(ctx, pvc)).Should(Succeed())
		})
	})

	Context("When the pvc has the annotation of accept-ephemeral-storage and EphemeralDiskVolumeType is PersistentVolumeWithAnnotation", func() {
		It("Should allow the pvc", func() {
			By("Creating an acstor storage class")
			sc = GenStorageClass(testStorageClassName, DriverName, nil)
			Expect(k8sClient.Create(ctx, sc)).Should(Succeed())
			Eventually(storageClassExists(sc.Name)).Should(Succeed(), "StorageClass %s should exist before proceeding", sc.Name)
			By("Creating a pvc with annotation of accept-ephemeral-storage")
			pvc = GenPVC(testStorageClassName, Namespace, "1Gi")
			pvc.SetAnnotations(map[string]string{
				acceptEphemeralStorageAnnotationKey: "true",
			})
			Expect(k8sClient.Create(ctx, pvc)).Should(Succeed())
		})
	})
})

func GenStorageClass(scName, provisioner string, params map[string]string) *storagev1.StorageClass {
	return &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: scName,
		},
		Provisioner: provisioner,
		Parameters:  params,
	}
}

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

func storageClassExists(name string) func() error {
	sc := &storagev1.StorageClass{}
	return func() error {
		return k8sClient.Get(ctx, types.NamespacedName{Name: name}, sc)
	}
}
