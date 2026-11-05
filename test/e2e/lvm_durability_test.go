// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"local-csi-driver/internal/csi/core"
	"local-csi-driver/internal/csi/core/lvm"
	"local-csi-driver/test/pkg/utils"
)

var testLvmDurability = func() {
	Context("durability policy", Label("durability"), func() {
		It("should preserve the checksum and LV identity when restaged on its owner", func(ctx context.Context) {
			f := newDurabilityFixture(ctx)
			checksum := f.writeChecksum(ctx)
			f.deletePod(ctx)
			f.createPod(ctx, f.owner)
			f.waitRunning(ctx)
			Expect(f.checksum(ctx)).To(Equal(checksum))
			Expect(f.lvUUID(ctx, f.driver)).To(Equal(f.originalLVUUID))
		})

		It("should leave a replacement Pending rather than recover empty data on another eligible node", func(ctx context.Context) {
			f := newDurabilityFixture(ctx)
			target := f.verifyAlternativeNode(ctx)
			checksum := f.writeChecksum(ctx)
			owner, err := durabilityGet[corev1.Node](ctx, "node", f.owner)
			Expect(err).NotTo(HaveOccurred())
			Expect(owner.Spec.Unschedulable).To(BeFalse(), "must not undo a preexisting cordon")
			DeferCleanup(func(ctx context.Context) {
				durabilityKubectl(ctx, "uncordon", f.owner)
			})
			durabilityKubectl(ctx, "cordon", f.owner)
			f.deletePod(ctx)
			f.createPod(ctx, "")
			pod := f.waitEvent(ctx, "FailedScheduling", "node affinity")
			Expect(pod.Spec.NodeName).To(BeEmpty())
			Consistently(func(g Gomega, ctx context.Context) {
				pod, err := durabilityGet[corev1.Pod](ctx, "-n", f.namespace, "pod", "workload")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(pod.Status.Phase).To(Equal(corev1.PodPending))
				g.Expect(pod.Spec.NodeName).To(BeEmpty())
				g.Expect(f.lvUUID(ctx, f.driver)).To(Equal(f.originalLVUUID))
				f.expectNoLV(ctx, target)
			}).WithContext(ctx).WithTimeout(30 * time.Second).Should(Succeed())
			By("restoring the owner and checking the original data")
			durabilityKubectl(ctx, "uncordon", f.owner)
			f.waitRunning(ctx)
			Expect(f.checksum(ctx)).To(Equal(checksum))
		})

		It("should fail staging a missing owner LV without creating an empty replacement", Label("destructive"), func(ctx context.Context) {
			// A kind-looking context alone is not permission to remove storage.
			if os.Getenv("DURABILITY_DISPOSABLE_KIND") != "true" {
				Skip("requires an explicitly disposable kind fixture: DURABILITY_DISPOSABLE_KIND=true")
			}
			Expect(strings.TrimSpace(durabilityKubectl(ctx, "config", "current-context"))).To(Equal("kind-kind"))
			f := newDurabilityFixture(ctx)
			f.verifyKindNode(ctx)
			_ = f.writeChecksum(ctx)
			f.deletePod(ctx)
			f.removeBackingLV(ctx)
			f.createPod(ctx, f.owner)
			pod := f.waitEvent(ctx, "FailedMount", "MountVolume.MountDevice", "code = NotFound")
			Expect(pod.Spec.NodeName).To(Equal(f.owner))
			Consistently(func(g Gomega, ctx context.Context) {
				pod, err := durabilityGet[corev1.Pod](ctx, "-n", f.namespace, "pod", "workload")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(pod.Spec.NodeName).To(Equal(f.owner))
				g.Expect(pod.Status.Phase).To(Equal(corev1.PodPending))
				for _, status := range pod.Status.ContainerStatuses {
					g.Expect(status.Ready).To(BeFalse())
					g.Expect(status.State.Running).To(BeNil())
				}
				f.expectNoLV(ctx, f.driver)
			}).WithContext(ctx).WithTimeout(30 * time.Second).Should(Succeed())
		})
	})
}

type durabilityFixture struct {
	namespace      string
	storageClass   string
	pvc            corev1.PersistentVolumeClaim
	pv             corev1.PersistentVolume
	owner          string
	driver         string
	originalLVUUID string
	deletedPodUID  string
}

func newDurabilityFixture(ctx context.Context) *durabilityFixture {
	var ns corev1.Namespace
	durabilityCreate(ctx, &corev1.Namespace{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{GenerateName: "lvm-durability-"},
	}, &ns)
	f := &durabilityFixture{namespace: ns.Name, storageClass: ns.Name}
	DeferCleanup(func(ctx context.Context) {
		durabilityKubectl(ctx, "delete", "storageclass", f.storageClass, "--ignore-not-found")
	})
	DeferCleanup(func(ctx context.Context) {
		durabilityKubectl(ctx, "delete", "namespace", f.namespace, "--wait=true", "--timeout=2m")
	})
	DeferCleanup(func(ctx context.Context) {
		durabilityKubectl(ctx, "-n", f.namespace, "delete", "pvc", "--all", "--wait=true", "--timeout=2m")
		if f.pv.Name != "" {
			durabilityKubectl(ctx, "wait", "--for=delete", "pv/"+f.pv.Name, "--timeout=2m")
		}
	})
	DeferCleanup(func(ctx context.Context) {
		durabilityKubectl(ctx, "-n", f.namespace, "delete", "pod", "--all", "--wait=true", "--timeout=2m")
	})

	By("creating a dedicated StorageClass with an explicit durability policy")
	binding, reclaim := storagev1.VolumeBindingWaitForFirstConsumer, corev1.PersistentVolumeReclaimDelete
	durabilityCreate(ctx, &storagev1.StorageClass{
		TypeMeta:          metav1.TypeMeta{APIVersion: "storage.k8s.io/v1", Kind: "StorageClass"},
		ObjectMeta:        metav1.ObjectMeta{Name: f.storageClass},
		Provisioner:       lvm.DriverName,
		Parameters:        map[string]string{core.FailoverModeParam: core.FailoverModeDurability},
		VolumeBindingMode: &binding,
		ReclaimPolicy:     &reclaim,
	}, nil)
	durabilityCreate(ctx, &corev1.PersistentVolumeClaim{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "data", Namespace: f.namespace,
			Annotations: map[string]string{lvm.DriverName + "/accept-ephemeral-storage": "true"},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: &f.storageClass,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
		},
	}, &f.pvc)
	f.createPod(ctx, "")
	f.waitRunning(ctx)
	var err error
	f.pvc, err = durabilityGet[corev1.PersistentVolumeClaim](ctx, "-n", f.namespace, "pvc", "data")
	Expect(err).NotTo(HaveOccurred())
	Expect(f.pvc.Status.Phase).To(Equal(corev1.ClaimBound))
	f.pv, err = durabilityGet[corev1.PersistentVolume](ctx, "pv", f.pvc.Spec.VolumeName)
	Expect(err).NotTo(HaveOccurred())
	Expect(f.pv.Spec.CSI).NotTo(BeNil())
	Expect(f.pv.Spec.CSI.Driver).To(Equal(lvm.DriverName))
	Expect(f.pv.Spec.CSI.VolumeAttributes[core.FailoverModeParam]).To(Equal(core.FailoverModeDurability))
	Expect(f.pv.Spec.ClaimRef).NotTo(BeNil())
	Expect(f.pv.Spec.ClaimRef.UID).To(Equal(f.pvc.UID))
	Expect(f.pv.Name).To(Equal("pvc-" + string(f.pvc.UID)))
	Expect(f.pv.Spec.CSI.VolumeHandle).To(Equal(lvm.DefaultVolumeGroup + "#" + f.pv.Name))
	pod, err := durabilityGet[corev1.Pod](ctx, "-n", f.namespace, "pod", "workload")
	Expect(err).NotTo(HaveOccurred())
	f.owner = pod.Spec.NodeName
	Expect(f.owner).NotTo(BeEmpty())
	Expect(f.pv.Spec.NodeAffinity).NotTo(BeNil())
	Expect(f.pv.Spec.NodeAffinity.Required).NotTo(BeNil())
	terms := f.pv.Spec.NodeAffinity.Required.NodeSelectorTerms
	Expect(terms).NotTo(BeEmpty())
	for _, term := range terms {
		Expect(term.MatchExpressions).To(ContainElement(corev1.NodeSelectorRequirement{
			Key: lvm.TopologyKey, Operator: corev1.NodeSelectorOpIn, Values: []string{f.owner},
		}), "every OR term must restrict the PV to its owner")
	}
	f.driver, err = getDriverPodOnNode(ctx, f.owner)
	Expect(err).NotTo(HaveOccurred())
	Expect(f.driver).NotTo(BeEmpty())
	f.originalLVUUID = f.lvUUID(ctx, f.driver)
	Expect(f.originalLVUUID).NotTo(BeEmpty())
	return f
}

func (f *durabilityFixture) createPod(ctx context.Context, node string) {
	pod := &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: f.namespace},
		Spec: corev1.PodSpec{
			NodeSelector: map[string]string{"kubernetes.io/os": "linux"},
			Containers: []corev1.Container{{
				Name: "workload", Image: "mcr.microsoft.com/azurelinux/busybox:1.36",
				Command:      []string{"/bin/sh", "-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
				VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/mnt/lcd"}},
			}},
			Volumes: []corev1.Volume{{
				Name: "data", VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"},
				},
			}},
		},
	}
	if node != "" {
		// Still use the scheduler, including its PV affinity checks.
		pod.Spec.NodeSelector[lvm.TopologyKey] = node
	}
	durabilityCreate(ctx, pod, nil)
}

func (f *durabilityFixture) waitRunning(ctx context.Context) {
	durabilityKubectl(ctx, "-n", f.namespace, "wait", "--for=condition=Ready", "pod/workload", "--timeout=2m")
}

func (f *durabilityFixture) deletePod(ctx context.Context) {
	pod, err := durabilityGet[corev1.Pod](ctx, "-n", f.namespace, "pod", "workload")
	Expect(err).NotTo(HaveOccurred())
	f.deletedPodUID = string(pod.UID)
	durabilityKubectl(ctx, "-n", f.namespace, "delete", "pod", "workload", "--wait=true", "--timeout=2m")
}

func (f *durabilityFixture) writeChecksum(ctx context.Context) string {
	durabilityKubectl(ctx, "-n", f.namespace, "exec", "workload", "--", "/bin/sh", "-c",
		"dd if=/dev/urandom of=/mnt/lcd/payload bs=4096 count=256 && sync")
	return f.checksum(ctx)
}

func (f *durabilityFixture) checksum(ctx context.Context) string {
	sum := strings.Fields(durabilityKubectl(ctx, "-n", f.namespace, "exec", "workload", "--", "sha256sum", "/mnt/lcd/payload"))
	Expect(sum).To(HaveLen(2))
	Expect(sum[0]).To(HaveLen(64))
	return sum[0]
}

func (f *durabilityFixture) lvUUID(ctx context.Context, driver string) string {
	return strings.TrimSpace(durabilityKubectl(ctx, "-n", namespace, "exec", driver, "--",
		"lvs", "--noheadings", "-o", "lv_uuid", lvm.DefaultVolumeGroup+"/"+f.pv.Name))
}

func (f *durabilityFixture) expectNoLV(ctx context.Context, driver string) {
	// A successful empty selection proves absence; command errors do not.
	out := durabilityKubectl(ctx, "-n", namespace, "exec", driver, "--",
		"lvs", "--noheadings", "-o", "lv_name", "--select", "lv_name="+f.pv.Name)
	Expect(strings.TrimSpace(out)).To(BeEmpty(), "must not create a replacement LV for the durability PV")
}

func (f *durabilityFixture) verifyAlternativeNode(ctx context.Context) string {
	nodes, err := durabilityGet[corev1.NodeList](ctx, "nodes")
	Expect(err).NotTo(HaveOccurred())
	for _, node := range nodes.Items {
		if node.Name == f.owner || node.Spec.Unschedulable || len(node.Spec.Taints) != 0 ||
			node.Labels["kubernetes.io/os"] != "linux" {
			continue
		}
		driver, err := getDriverPodOnNode(ctx, node.Name)
		Expect(err).NotTo(HaveOccurred())
		if driver == "" {
			continue
		}
		// A scheduled probe proves eligibility instead of merely counting nodes.
		probe := &corev1.Pod{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
			ObjectMeta: metav1.ObjectMeta{Name: "scheduler-probe", Namespace: f.namespace},
			Spec: corev1.PodSpec{
				NodeSelector: map[string]string{lvm.TopologyKey: node.Name},
				Containers: []corev1.Container{{
					Name: "probe", Image: "mcr.microsoft.com/azurelinux/busybox:1.36",
					Command: []string{"/bin/sh", "-c", "sleep 600"},
				}},
			},
		}
		durabilityCreate(ctx, probe, nil)
		durabilityKubectl(ctx, "-n", f.namespace, "wait", "--for=condition=Ready", "pod/scheduler-probe", "--timeout=2m")
		durabilityKubectl(ctx, "-n", f.namespace, "delete", "pod", "scheduler-probe", "--wait=true", "--timeout=2m")
		f.expectNoLV(ctx, driver)
		return driver
	}
	Skip("requires another schedulable, untainted Linux node running the CSI driver")
	return ""
}

func (f *durabilityFixture) waitEvent(ctx context.Context, reason string, messages ...string) corev1.Pod {
	var pod corev1.Pod
	Eventually(func(g Gomega, ctx context.Context) {
		var err error
		pod, err = durabilityGet[corev1.Pod](ctx, "-n", f.namespace, "pod", "workload")
		g.Expect(err).NotTo(HaveOccurred())
		events, err := durabilityGet[corev1.EventList](ctx, "-n", f.namespace, "events",
			"--field-selector", "involvedObject.uid="+string(pod.UID))
		g.Expect(err).NotTo(HaveOccurred())
		found := false
		for _, event := range events.Items {
			if event.Reason != reason {
				continue
			}
			matches := true
			for _, message := range messages {
				matches = matches && strings.Contains(event.Message, message)
			}
			found = found || matches
		}
		g.Expect(found).To(BeTrue(), "expected %s containing %q for replacement pod UID %s", reason, messages, pod.UID)
	}).WithContext(ctx).Should(Succeed())
	return pod
}

func (f *durabilityFixture) verifyKindNode(ctx context.Context) {
	out, err := utils.Run(exec.CommandContext(ctx, "docker", "inspect", "--format",
		`{{index .Config.Labels "io.x-k8s.kind.cluster"}}`, f.owner))
	Expect(err).NotTo(HaveOccurred())
	Expect(strings.TrimSpace(out)).To(Equal("kind"), "LV removal is allowed only inside the disposable kind node")
}

func (f *durabilityFixture) removeBackingLV(ctx context.Context) {
	By("waiting for workload unpublish, then dropping only this LV's cached staging mount")
	// NodeUnstageVolume retains a staging mount. Once kubelet removes the old
	// pod directory, release that test-only cache before simulating disk loss.
	Eventually(func(g Gomega, ctx context.Context) {
		_, err := utils.Run(exec.CommandContext(ctx, "docker", "exec", f.owner,
			"test", "!", "-e", "/var/lib/kubelet/pods/"+f.deletedPodUID))
		g.Expect(err).NotTo(HaveOccurred())
	}).WithContext(ctx).Should(Succeed())
	pvc, err := durabilityGet[corev1.PersistentVolumeClaim](ctx, "-n", f.namespace, "pvc", "data")
	Expect(err).NotTo(HaveOccurred())
	Expect(pvc.UID).To(Equal(f.pvc.UID))
	Expect(pvc.Spec.VolumeName).To(Equal(f.pv.Name))
	Expect(f.pv.Name).To(Equal("pvc-" + string(pvc.UID)))
	Expect(f.lvUUID(ctx, f.driver)).To(Equal(f.originalLVUUID))
	script := `set -eu
device="$1"
expected_uuid="$2"
test "$(lvs --noheadings -o lv_uuid "$device" | tr -d ' ')" = "$expected_uuid"
targets="$(findmnt -rn -S "$device" -o TARGET || test "$?" -eq 1)"
for target in $targets; do
  case "$target" in
    /var/lib/kubelet/plugins/kubernetes.io/csi/*/globalmount) ;;
    *) echo "Refusing to unmount non-staging target: $target" >&2; exit 1 ;;
  esac
done
for target in $targets; do umount "$target"; done
test -z "$(findmnt -rn -S "$device" -o TARGET || test "$?" -eq 1)"
attrs="$(lvs --noheadings -o lv_attr "$device" | tr -d ' ')"
test "$(printf '%s' "$attrs" | cut -c6)" != o
test "$(lvs --noheadings -o lv_uuid "$device" | tr -d ' ')" = "$expected_uuid"
lvremove --yes "$device"
`
	_, err = utils.Run(exec.CommandContext(ctx, "docker", "exec", f.owner, "sh", "-c", script,
		"--", "/dev/"+lvm.DefaultVolumeGroup+"/"+f.pv.Name, f.originalLVUUID))
	Expect(err).NotTo(HaveOccurred(), "refuse LV removal unless the freshly allocated test LV is fully unstaged and closed")
	f.expectNoLV(ctx, f.driver)
}

func durabilityKubectl(ctx context.Context, args ...string) string {
	out, err := utils.Run(exec.CommandContext(ctx, "kubectl", args...))
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return out
}

func durabilityGet[T any](ctx context.Context, args ...string) (T, error) {
	var obj T
	args = append([]string{"get"}, args...)
	args = append(args, "-o", "json")
	out, err := utils.Run(exec.CommandContext(ctx, "kubectl", args...), utils.Silent())
	if err != nil {
		return obj, fmt.Errorf("kubectl %v: %w: %s", args, err, out)
	}
	err = json.Unmarshal([]byte(out), &obj)
	return obj, err
}

func durabilityCreate(ctx context.Context, obj, result any) {
	data, err := json.Marshal(obj)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	cmd := exec.CommandContext(ctx, "kubectl", "create", "-f", "-", "-o", "json")
	cmd.Stdin = strings.NewReader(string(data))
	out, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	if result != nil {
		ExpectWithOffset(1, json.Unmarshal([]byte(out), result)).To(Succeed())
	}
}
