// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"local-csi-driver/internal/csi/core/lvm"
	"local-csi-driver/test/pkg/common"
	"local-csi-driver/test/pkg/utils"
)

// chaosReplicas is the number of churned application pods (each with its own
// ephemeral volume) the chaos workload scales between.
const chaosReplicas = "100"

// testLvmChaos stresses the driver by churning a large number of ephemeral
// volumes while repeatedly force-deleting the csi-local-node DaemonSet pods.
// Killing the node pod mid-flight aborts in-progress lvcreate/lvremove
// operations, which is the classic way to leave behind stale device-mapper
// nodes, dangling /dev/<vg> symlinks, and orphaned LVM metadata. The test
// asserts the driver and its garbage collection controllers recover: all
// churned volumes are eventually removed (verified via lvs, /dev/mapper and
// /dev/<vg>), and a fresh volume still provisions cleanly.
//
// Labeled "chaos" so it can be opted into/out of via LABEL_FILTER. Unlike the
// storagepool tests, it does not assert disk/stripe counts, so it runs on
// loopback-backed clusters (e.g. kind) as well as AKS.
var testLvmChaos = func() {
	Context("lvm chaos", Label("chaos"), func() {
		It("should recover from killing csi-local-node pods during volume churn", func(ctx context.Context) {
			chaosDuration := durationFromEnv("CHAOS_DURATION", 5*time.Minute)
			killInterval := durationFromEnv("CHAOS_KILL_INTERVAL", 10*time.Second)
			churnInterval := durationFromEnv("CHAOS_CHURN_INTERVAL", 30*time.Second)

			By("Applying the storageclass")
			cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", common.LvmStorageClassFixture)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "applying storageclass")

			By("Applying the chaos statefulset")
			cmd = exec.CommandContext(ctx, "kubectl", "apply", "-f", common.LvmChaosStatefulSetFixture)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "applying chaos statefulset")

			DeferCleanup(func(ctx SpecContext) {
				By("Deleting the chaos statefulset and its PVCs")
				cmd := exec.CommandContext(ctx, "kubectl", "delete", "--wait", "--ignore-not-found", "-f", common.LvmChaosStatefulSetFixture)
				_, err := utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred(), "deleting chaos statefulset")

				cmd = exec.CommandContext(ctx, "kubectl", "delete", "--wait", "--ignore-not-found", "pvc", "-l", "app=lcd-chaos")
				_, err = utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred(), "deleting chaos PVCs")
			})

			By("Waiting for the chaos statefulset to come up at least once")
			cmd = exec.CommandContext(ctx, "kubectl", "rollout", "status", "--timeout=10m", "-f", common.LvmChaosStatefulSetFixture)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "waiting for chaos statefulset rollout")

			// chaosCtx bounds both the killer and the churner. It is canceled
			// either when the duration elapses or when the spec context is done.
			chaosCtx, cancel := context.WithTimeout(ctx, chaosDuration)
			defer cancel()

			var wg sync.WaitGroup

			By(fmt.Sprintf("Starting csi-local-node pod killer loop (every %s)", killInterval))
			wg.Add(1)
			go func() {
				defer GinkgoRecover()
				defer wg.Done()
				for {
					if err := utils.Sleep(chaosCtx, killInterval); err != nil {
						return
					}
					if err := killNodePods(chaosCtx); err != nil {
						_, _ = fmt.Fprintf(GinkgoWriter, "killing csi-local-node pods failed: %v\n", err)
					}
				}
			}()

			By(fmt.Sprintf("Churning ephemeral volumes (scale 0<->%s every %s) for %s", chaosReplicas, churnInterval, chaosDuration))
			wg.Add(1)
			go func() {
				defer GinkgoRecover()
				defer wg.Done()
				down := true
				for {
					if err := utils.Sleep(chaosCtx, churnInterval); err != nil {
						return
					}
					replicas := chaosReplicas
					if down {
						replicas = "0"
					}
					down = !down
					cmd := exec.CommandContext(chaosCtx, "kubectl", "scale", "statefulset", "statefulset-lcd-chaos", "--replicas", replicas)
					if _, err := utils.Run(cmd); err != nil {
						_, _ = fmt.Fprintf(GinkgoWriter, "scale to %s failed: %v\n", replicas, err)
					}
				}
			}()

			wg.Wait()

			By("Scaling the chaos statefulset down to release all volumes")
			cmd = exec.CommandContext(ctx, "kubectl", "scale", "statefulset", "statefulset-lcd-chaos", "--replicas", "0")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "scaling chaos statefulset to zero")

			cmd = exec.CommandContext(ctx, "kubectl", "delete", "--wait", "--ignore-not-found", "pvc", "-l", "app=lcd-chaos")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "deleting chaos PVCs")

			By("Verifying no stale LVs, /dev/mapper nodes or /dev/containerstorage links remain after recovery")
			Eventually(func(g Gomega, ctx context.Context) {
				pods, err := getNodePods(ctx)
				g.Expect(err).NotTo(HaveOccurred(), "listing csi-local-node pods")
				g.Expect(pods).NotTo(BeEmpty(), "expected at least one csi-local-node pod")

				for _, pod := range pods {
					lvs, err := getLvs(ctx, pod)
					g.Expect(err).NotTo(HaveOccurred(), "listing LVs for pod %s", pod)

					mapper, err := getOrphanMapperDevices(ctx, pod)
					g.Expect(err).NotTo(HaveOccurred(), "listing /dev/mapper for pod %s", pod)

					links, err := getOrphanDeviceLinks(ctx, pod)
					g.Expect(err).NotTo(HaveOccurred(), "listing /dev/%s links for pod %s", lvm.DefaultVolumeGroup, pod)

					_, _ = fmt.Fprintf(GinkgoWriter, "recovery check pod %s: lvs=%v, orphanMapper=%v, orphanLinks=%v\n", pod, lvs, mapper, links)

					g.Expect(lvs).To(BeEmpty(), "no LVs should remain after churn for pod %s", pod)
					g.Expect(mapper).To(BeEmpty(), "no orphaned /dev/mapper nodes should remain after churn for pod %s", pod)
					g.Expect(links).To(BeEmpty(), "no stale /dev/%s device links should remain after churn for pod %s", lvm.DefaultVolumeGroup, pod)
				}
			}).WithContext(ctx).WithTimeout(10*time.Minute).Should(Succeed(), "driver did not converge to a clean state after chaos")

			By("Verifying the driver still provisions volumes after the chaos")
			cmd = exec.CommandContext(ctx, "kubectl", "scale", "statefulset", "statefulset-lcd-chaos", "--replicas", chaosReplicas)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "scaling chaos statefulset back up")

			cmd = exec.CommandContext(ctx, "kubectl", "rollout", "status", "--timeout=10m", "-f", common.LvmChaosStatefulSetFixture)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "chaos statefulset should provision cleanly after chaos")
		})
	})
}

// durationFromEnv reads a time.Duration from the named environment variable,
// falling back to def when unset or unparsable.
func durationFromEnv(name string, def time.Duration) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "invalid %s=%q, using default %s: %v\n", name, v, def, err)
		return def
	}
	return d
}

// getNodePods returns the names of the csi-local-node DaemonSet pods.
func getNodePods(ctx context.Context) ([]string, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "pods", "-n", "kube-system",
		"-l", "app.kubernetes.io/component=csi-local-node",
		"-o", "jsonpath={.items[*].metadata.name}")
	out, err := utils.Run(cmd)
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// killNodePods force-deletes the csi-local-node DaemonSet pods with no
// graceful shutdown (--grace-period=0 --force), so any in-flight
// lvcreate/lvremove is aborted abruptly. The DaemonSet immediately recreates
// the pods. --wait=false returns without blocking on recreation.
func killNodePods(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "kubectl", "delete", "pods", "-n", "kube-system",
		"-l", "app.kubernetes.io/component=csi-local-node",
		"--grace-period=0", "--force", "--wait=false", "--ignore-not-found")
	out, err := utils.Run(cmd)
	if out = strings.TrimSpace(out); out != "" {
		_, _ = fmt.Fprintf(GinkgoWriter, "killed node pods: %s\n", strings.ReplaceAll(out, "\n", ", "))
	}
	return err
}

// getOrphanMapperDevices lists /dev/mapper inside the node pod and returns the
// device-mapper nodes belonging to the default VG that have no corresponding
// active logical volume. These are the stale dm nodes left behind when an
// lvremove is aborted by a node-pod kill. /dev/mapper names LVM devices as
// "<vg>-<lv>" with literal hyphens doubled, matching dmsetup output, so the
// same parser is used.
func getOrphanMapperDevices(ctx context.Context, podName string) ([]string, error) {
	out, err := lsDir(ctx, podName, "/dev/mapper")
	if err != nil {
		return nil, err
	}
	live, err := liveLVNames(ctx, podName)
	if err != nil {
		return nil, err
	}
	return parseOrphanDmDevices(out, live, lvm.DefaultVolumeGroup), nil
}

// parseOrphanDmDevices parses a device listing ("dmsetup ls" or "ls
// /dev/mapper") and returns devices in the given VG that have no matching live
// LV. LVM names these devices as "<vg>-<lv>" and doubles literal hyphens within
// each component, so we undo that doubling before comparing against the live LV
// list. The "control" node and other VGs are ignored.
func parseOrphanDmDevices(listing string, live map[string]struct{}, vg string) []string {
	prefix := vg + "-"
	var orphans []string
	for _, line := range strings.Split(strings.TrimSpace(listing), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		if name == "No" { // "No devices found"
			continue
		}
		if name == "control" { // /dev/mapper control node
			continue
		}
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		lvName := strings.ReplaceAll(strings.TrimPrefix(name, prefix), "--", "-")
		if _, ok := live[lvName]; ok {
			continue
		}
		orphans = append(orphans, name)
	}
	return orphans
}

// getOrphanDeviceLinks returns entries under /dev/<defaultVG> (the per-VG
// directory of device symlinks LVM maintains) that have no matching live LV.
// When lvremove is interrupted these symlinks can be left dangling even after
// the LV metadata is gone, so they are a direct signal of a leaked volume
// device node.
func getOrphanDeviceLinks(ctx context.Context, podName string) ([]string, error) {
	out, err := lsDir(ctx, podName, "/dev/"+lvm.DefaultVolumeGroup)
	if err != nil {
		return nil, err
	}

	live, err := liveLVNames(ctx, podName)
	if err != nil {
		return nil, err
	}
	return parseOrphanDeviceLinks(out, live), nil
}

// lsDir runs "ls -1A <dir>" inside the node pod, returning one entry per line.
// A missing directory (e.g. no volume ever provisioned) is tolerated and
// reported as empty output rather than an error.
func lsDir(ctx context.Context, podName, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "exec", "-n", "kube-system", podName, "--",
		"sh", "-c", "ls -1A "+dir+" 2>/dev/null || true")
	return utils.Run(cmd)
}

// parseOrphanDeviceLinks returns the listed device link names that do not
// correspond to a live LV. Entries in /dev/<vg> are named exactly after their
// LV (no hyphen doubling), so the comparison is direct.
func parseOrphanDeviceLinks(lsOutput string, live map[string]struct{}) []string {
	var orphans []string
	for _, line := range strings.Split(strings.TrimSpace(lsOutput), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		if _, ok := live[name]; ok {
			continue
		}
		orphans = append(orphans, name)
	}
	return orphans
}

// liveLVNames returns the set of LV names currently present in the default VG
// for the given node pod.
func liveLVNames(ctx context.Context, podName string) (map[string]struct{}, error) {
	lvs, err := getLvs(ctx, podName)
	if err != nil {
		return nil, err
	}
	live := make(map[string]struct{}, len(lvs))
	for _, l := range lvs {
		live[l.Name] = struct{}{}
	}
	return live, nil
}
