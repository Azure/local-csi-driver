// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package kwok

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/sync/errgroup"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"local-csi-driver/internal/csi/core/lvm"
	"local-csi-driver/test/pkg/kwok"
	"local-csi-driver/test/pkg/utils"
)

const (
	logInterval = 500
	parallelism = 100
	nodeCount   = 10000
	podCount    = 10000
	pvCount     = 10000
	pvcCount    = 10000
)

var _ = Describe("Kwok", Label("kwok"), Ordered, func() {

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)
	EnforceDefaultTimeoutsWhenUsingContexts()

	testNamespace := "workload-" + utils.RandomTag()

	Context("local-csi-driver pods", func() {
		BeforeAll(func(ctx context.Context) {

			By(fmt.Sprintf("creating namespace %q", testNamespace))
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed(), "creating namespace %q", testNamespace)
			DeferCleanup(func(ctx context.Context) {
				if *skipCleanup {
					By("skip-cleanup: leaving namespace")
					return
				}
				By("deleting namespace")
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, ns))).To(Succeed(), "deleting namespace %q", testNamespace)
			})

			By("creating storageclass")
			sc, err := kwok.NewStorageClass()
			Expect(err).NotTo(HaveOccurred(), "rendering storageclass")
			Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, sc))).To(Succeed(), "creating storageclass")
			DeferCleanup(func(ctx context.Context) {
				By("deleting storageclass")
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, sc))).To(Succeed(), "deleting storageclass")
			})

			By("creating kwok no-op storageclass")
			noopSC, err := kwok.NewNoopStorageClass()
			Expect(err).NotTo(HaveOccurred(), "rendering kwok no-op storageclass")
			Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, noopSC))).To(Succeed(), "creating kwok no-op storageclass")
			DeferCleanup(func(ctx context.Context) {
				By("deleting kwok no-op storageclass")
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, noopSC))).To(Succeed(), "deleting kwok no-op storageclass")
			})

			By("warming the csi driver via a real statefulset")
			warmup, err := kwok.NewWarmupStatefulSet(testNamespace)
			Expect(err).NotTo(HaveOccurred(), "rendering warmup statefulset")
			Expect(k8sClient.Create(ctx, warmup)).To(Succeed(), "creating warmup statefulset")
			Eventually(func(g Gomega) {
				var got appsv1.StatefulSet
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(warmup), &got)).To(Succeed(), "getting warmup statefulset")
				g.Expect(got.Status.ReadyReplicas).To(Equal(*warmup.Spec.Replicas), "warmup pods not ready")
			}).WithTimeout(5*time.Minute).WithPolling(5*time.Second).Should(Succeed(), "warmup statefulset never became ready")

			By("deleting the warmup statefulset and waiting for csi-driver volumes to drain")
			Expect(k8sClient.Delete(ctx, warmup)).To(Succeed(), "deleting warmup statefulset")
			Eventually(func(g Gomega) int {
				var pvs corev1.PersistentVolumeList
				g.Expect(k8sClient.List(ctx, &pvs)).To(Succeed(), "listing pvs")
				count := 0
				for _, pv := range pvs.Items {
					if pv.Spec.StorageClassName == sc.Name {
						count++
					}
				}
				return count
			}).WithTimeout(5*time.Minute).WithPolling(5*time.Second).Should(BeZero(), "warmup pvs not drained")

			By("triggering csi-local-manager Node informer via a Released real-driver PV")
			triggerPV := triggerNodeInformerPV("trigger-node-informer", "node-0")
			Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, triggerPV))).
				To(Succeed(), "creating trigger PV")
			// Phase is set on the status subresource - after Create the PV
			// is Available, so patch its status to Released to make
			// pvcleanup-controller reconcile and Get the Node. Re-Get and
			// retry on conflict: the in-memory object can be stale (e.g.
			// the PV already existed, or a controller added a finalizer
			// between Create and this status update).
			Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(triggerPV), triggerPV); err != nil {
					return err
				}
				triggerPV.Status.Phase = corev1.VolumeReleased
				return k8sClient.Status().Update(ctx, triggerPV)
			})).To(Succeed(), "patching trigger PV status to Released")
			DeferCleanup(func(ctx context.Context) {
				patch := []byte(`{"metadata":{"finalizers":null}}`)
				_ = client.IgnoreNotFound(k8sClient.Patch(ctx, triggerPV,
					client.RawPatch(types.MergePatchType, patch)))
				_ = client.IgnoreNotFound(k8sClient.Delete(ctx, triggerPV))
			})

			Eventually(func(g Gomega) int {
				var pvcs corev1.PersistentVolumeClaimList
				g.Expect(k8sClient.List(ctx, &pvcs, client.InNamespace(testNamespace))).To(Succeed(), "listing pvcs")
				return len(pvcs.Items)
			}).WithTimeout(2*time.Minute).WithPolling(5*time.Second).Should(BeZero(), "warmup pvcs not drained")

			DeferCleanup(func(ctx context.Context) {
				if *skipCleanup {
					By("skip-cleanup: leaving kwok nodes")
					return
				}
				deleteAllInParallel(ctx, "node",
					func() client.ObjectList { return &corev1.NodeList{} },
					[]client.ListOption{
						client.MatchingLabels{"type": "kwok"},
					},
					client.GracePeriodSeconds(0),
					client.PropagationPolicy(metav1.DeletePropagationBackground),
				)
			})

			By(fmt.Sprintf("creating %d nodes", nodeCount))
			createInParallel(ctx, nodeCount, "node", func(i int) client.Object {
				node, err := kwok.NewNode(fmt.Sprintf("node-%d", i))
				Expect(err).NotTo(HaveOccurred(), "rendering node %d", i)
				return node
			})

			DeferCleanup(func(ctx context.Context) {
				if *skipCleanup {
					By("skip-cleanup: leaving kwok pods")
					return
				}
				deleteAllInParallel(ctx, "pod",
					func() client.ObjectList { return &corev1.PodList{} },
					[]client.ListOption{
						client.InNamespace(testNamespace),
						client.MatchingLabels{"type": "kwok"},
					},
					client.GracePeriodSeconds(0),
					client.PropagationPolicy(metav1.DeletePropagationBackground),
				)
			})

			By(fmt.Sprintf("creating %d pods", podCount))
			createInParallel(ctx, podCount, "pod", func(i int) client.Object {
				pod, err := kwok.NewPod(fmt.Sprintf("pod-%d", i), testNamespace)
				Expect(err).NotTo(HaveOccurred(), "rendering pod %d", i)
				return pod
			})

			DeferCleanup(func(ctx context.Context) {
				if *skipCleanup {
					By("skip-cleanup: leaving kwok pvs")
					return
				}
				deleteAllInParallel(ctx, "pv",
					func() client.ObjectList { return &corev1.PersistentVolumeList{} },
					[]client.ListOption{
						client.MatchingLabels{"type": "kwok"},
					},
					client.PropagationPolicy(metav1.DeletePropagationBackground),
				)
			})

			By(fmt.Sprintf("creating %d persistent volumes", pvCount))
			createInParallel(ctx, pvCount, "pv", func(i int) client.Object {
				pv, err := kwok.NewPV(fmt.Sprintf("pv-%d", i))
				Expect(err).NotTo(HaveOccurred(), "rendering pv %d", i)
				return pv
			})

			DeferCleanup(func(ctx context.Context) {
				if *skipCleanup {
					By("skip-cleanup: leaving kwok pvcs")
					return
				}
				deleteAllInParallel(ctx, "pvc",
					func() client.ObjectList { return &corev1.PersistentVolumeClaimList{} },
					[]client.ListOption{
						client.InNamespace(testNamespace),
						client.MatchingLabels{"type": "kwok"},
					},
					client.PropagationPolicy(metav1.DeletePropagationBackground),
				)
			})

			By(fmt.Sprintf("creating %d persistent volume claims", pvcCount))
			createInParallel(ctx, pvcCount, "pvc", func(i int) client.Object {
				pvc, err := kwok.NewPVC(fmt.Sprintf("pvc-%d", i), testNamespace)
				Expect(err).NotTo(HaveOccurred(), "rendering pvc %d", i)
				return pvc
			})

			By("sleeping for 1 minute to allow resources to stabilize")
			time.Sleep(1 * time.Minute)

			By("capturing heap dumps from csi-local-node and csi-local-manager pods")
			captureHeapDumps(ctx, *supportBundleDir)
		})

		It("should not be oomkilled when many resources exist", func(ctx context.Context) {
			By("watching csi-local-node and csi-local-manager pods for OOMKilled")
			Consistently(ctx, func(g Gomega) {
				for _, sel := range []client.MatchingLabels{
					{"app.kubernetes.io/component": "csi-local-node"},
					{"app.kubernetes.io/component": "manager"},
				} {
					var pods corev1.PodList
					g.Expect(k8sClient.List(ctx, &pods, client.InNamespace("kube-system"), sel)).To(Succeed(), "listing pods %v", sel)
					real := pods.Items[:0]
					for _, p := range pods.Items {
						// Skip pods scheduled onto kwok-managed fake nodes.
						if strings.HasPrefix(p.Spec.NodeName, "node-") {
							continue
						}
						real = append(real, p)
					}
					g.Expect(real).NotTo(BeEmpty(), "no real-node pods matched %v", sel)
					for _, p := range real {
						for _, cs := range p.Status.ContainerStatuses {
							if cs.State.Terminated != nil {
								g.Expect(cs.State.Terminated.Reason).NotTo(Equal("OOMKilled"), "pod %s/%s container %s OOMKilled", p.Namespace, p.Name, cs.Name)
							}
							if cs.LastTerminationState.Terminated != nil {
								g.Expect(cs.LastTerminationState.Terminated.Reason).NotTo(Equal("OOMKilled"), "pod %s/%s container %s previously OOMKilled", p.Namespace, p.Name, cs.Name)
							}
						}
					}
				}
			}).WithTimeout(5*time.Minute).WithPolling(10*time.Second).Should(Succeed(), "csi-local pods should not be OOMKilled")
		})
	})
})

// deleteAllInParallel paginates a List of objects matching listOpts and
// deletes each item in parallel (capped at parallelism). For each item it
// issues a Delete and moves on; the outer loop re-lists until empty, so
// the function only returns after every targeted object has been fully
// removed from etcd (finalizers cleared, deletionTimestamp processed).
//
// The collection-level DeleteAllOf can hit the apiserver --request-timeout
// (504) at scale - this avoids that by issuing many small per-item DELETEs
// while still bounding total time.
//
// listFn must return a fresh empty list each call. delOpts apply to every
// Delete call.
func deleteAllInParallel(
	ctx context.Context,
	kind string,
	listFn func() client.ObjectList,
	listOpts []client.ListOption,
	delOpts ...client.DeleteOption,
) {
	GinkgoHelper()

	const pageSize = 500
	var totalDeleted int
	var rounds int

	for {
		rounds++
		list := listFn()
		opts := append([]client.ListOption{client.Limit(pageSize)}, listOpts...)
		err := retry.OnError(retry.DefaultBackoff, isTransient, func() error {
			return k8sClient.List(ctx, list, opts...)
		})
		Expect(err).NotTo(HaveOccurred(), "listing %ss for delete (round %d)", kind, rounds)

		items, err := meta.ExtractList(list)
		Expect(err).NotTo(HaveOccurred(), "extracting %s list items", kind)
		if len(items) == 0 {
			By(fmt.Sprintf("deleted %d %ss in %d rounds", totalDeleted, kind, rounds))
			return
		}

		By(fmt.Sprintf("deleting %ss: round %d page=%d total-so-far=%d", kind, rounds, len(items), totalDeleted))

		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(parallelism)
		for _, raw := range items {
			obj, ok := raw.(client.Object)
			Expect(ok).To(BeTrue(), "list item is not a client.Object: %T", raw)
			g.Go(func() error {
				// Strip finalizers first so kube protection controllers
				// (pvc-protection, pv-protection) don't serially gate
				// deletion. The pvc-protection-controller is a single-worker
				// loop in upstream KCM and cannot keep up at 50k+ scale.
				// Safe in test cleanup: we don't care about graceful drain.
				if len(obj.GetFinalizers()) > 0 {
					patch := []byte(`{"metadata":{"finalizers":null}}`)
					if err := retry.OnError(retry.DefaultBackoff, isTransient, func() error {
						return client.IgnoreNotFound(k8sClient.Patch(gctx, obj,
							client.RawPatch(types.MergePatchType, patch)))
					}); err != nil {
						return fmt.Errorf("clearing finalizers on %s %s: %w", kind, obj.GetName(), err)
					}
				}
				if err := retry.OnError(retry.DefaultBackoff, isTransient, func() error {
					return client.IgnoreNotFound(k8sClient.Delete(gctx, obj, delOpts...))
				}); err != nil {
					return fmt.Errorf("deleting %s %s: %w", kind, obj.GetName(), err)
				}
				return nil
			})
		}
		Expect(g.Wait()).To(Succeed(), "deleting %ss (round %d)", kind, rounds)
		totalDeleted += len(items)
	}
}

// createInParallel creates n objects via k8sClient.Create using an errgroup
// capped at parallelism. Already-existing objects are ignored to keep
// BeforeAll idempotent across re-runs.
//
//nolint:unparam // n is fixed at the current scale but kept parameterized for future callers.
func createInParallel(ctx context.Context, n int, kind string, build func(i int) client.Object) {
	GinkgoHelper()

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(parallelism)
	for i := range n {
		if gctx.Err() != nil {
			break
		}
		if i%logInterval == 0 {
			By(fmt.Sprintf("creating %s %d", kind, i))
		}
		obj := build(i)
		g.Go(func() error {
			err := retry.OnError(retry.DefaultBackoff, isTransient, func() error {
				return client.IgnoreAlreadyExists(k8sClient.Create(gctx, obj))
			})
			if err != nil {
				return fmt.Errorf("creating %s %s: %w", kind, obj.GetName(), err)
			}
			return nil
		})
	}
	Expect(g.Wait()).To(Succeed(), "creating %d %ss", n, kind)
}

// isTransient returns true for API errors that are worth retrying:
// rate limiting, timeouts, brief unavailability, and internal errors.
func isTransient(err error) bool {
	return apierrors.IsTooManyRequests(err) ||
		apierrors.IsServerTimeout(err) ||
		apierrors.IsServiceUnavailable(err) ||
		apierrors.IsTimeout(err) ||
		apierrors.IsInternalError(err)
}

// captureHeapDumps fetches /debug/pprof/heap from each csi-local-node and
// csi-local-manager pod via `kubectl get --raw` (API server proxy) and
// writes the gzipped profiles to outDir/heap-dumps/. Failures are logged
// to GinkgoWriter and do not fail the test - this is best-effort diagnostics.
func captureHeapDumps(ctx context.Context, outDir string) {
	GinkgoHelper()

	dir := filepath.Join(outDir, "heap-dumps")
	Expect(os.MkdirAll(dir, 0o755)).To(Succeed(), "creating heap dump dir %s", dir)

	selectors := map[string]client.MatchingLabels{
		"driver":  {"app.kubernetes.io/component": "csi-local-node"},
		"manager": {"app.kubernetes.io/component": "manager"},
	}

	for kind, sel := range selectors {
		var pods corev1.PodList
		if err := k8sClient.List(ctx, &pods, client.InNamespace("kube-system"), sel); err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "listing %s pods: %v\n", kind, err)
			continue
		}
		for _, p := range pods.Items {
			out := filepath.Join(dir, fmt.Sprintf("%s-%s.pb.gz", kind, p.Name))
			path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s:6060/proxy/debug/pprof/heap?gc=1", p.Namespace, p.Name)
			cmd := exec.CommandContext(ctx, "kubectl", "get", "--raw", path)
			data, err := cmd.Output()
			if err != nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "fetching heap for %s/%s: %v\n", p.Namespace, p.Name, err)
				continue
			}
			if err := os.WriteFile(out, data, 0o644); err != nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "writing heap dump %s: %v\n", out, err)
				continue
			}
			_, _ = fmt.Fprintf(GinkgoWriter, "wrote heap dump %s (%d bytes)\n", out, len(data))
		}
	}
}

// triggerNodeInformerPV returns a PV that the csi-local-manager
// pvcleanup-controller will reconcile (real driver name, Delete reclaim,
// PVProtection finalizer, hostname topology, and a claimRef to a
// non-existent PVC). The first reconcile does a cached Get(Node{Name:hostname})
// which lazily starts the manager's Node informer - causing it to stream
// the full node list (10k+ in the kwok scale test) into the
// controller-runtime cache.
//
// The claimRef is required: a Released PV with no claimRef is reverted to
// Available by the kube PV controller, which would make the pvcleanup phase
// guard short-circuit before the Node Get. Pointing at a PVC that does not
// exist keeps Released durable.
//
// Caller must patch Status.Phase=Released after Create so the controller's
// phase guard doesn't short-circuit.
func triggerNodeInformerPV(name, hostname string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Finalizers: []string{"kubernetes.io/pv-protection"},
		},
		Spec: corev1.PersistentVolumeSpec{
			AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Capacity:                      corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
			// A Released PV must reference a claim; without a claimRef the
			// kube PV controller forces Phase back to Available, so the
			// pvcleanup phase guard would bail before the Node Get. Point at
			// a PVC that does not exist (stale UID) so Released is durable.
			ClaimRef: &corev1.ObjectReference{
				Kind:      "PersistentVolumeClaim",
				Namespace: "kube-system",
				Name:      name + "-gone",
				UID:       types.UID(uuid.NewString()),
			},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       lvm.DriverName,
					VolumeHandle: name,
				},
			},
			NodeAffinity: &corev1.VolumeNodeAffinity{
				Required: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{{
						MatchExpressions: []corev1.NodeSelectorRequirement{{
							// Must match lvm.TopologyKey: extractHostnamesFromPV
							// in the pvcleanup controller only reads node names
							// from this key. Using kubernetes.io/hostname here
							// makes the controller skip the PV before it Gets the
							// Node, so the Node informer never starts.
							Key:      lvm.TopologyKey,
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{hostname},
						}},
					}},
				},
			},
		},
	}
}
