// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"local-csi-driver/internal/pkg/lvm"
)

const (
	// DefaultReaperInterval is how often the reaper sweeps for quarantined
	// volumes when it has not been signalled.
	//
	// Deletions signal the reaper directly, so the timer only has to cover
	// dropped signals and volumes inherited from a previous process. Sweeps
	// are local lvs calls with no API server traffic, so the interval can be
	// short without needing jitter.
	DefaultReaperInterval = 60 * time.Second

	// DefaultReaperConcurrency is how many volumes are wiped at once.
	//
	// Zeroing is bound by device write bandwidth, so wiping volumes
	// concurrently does not increase aggregate throughput; it only multiplies
	// interference with foreground I/O on a node that is still serving
	// workloads.
	DefaultReaperConcurrency = 1

	// reaperBackoffBase and reaperBackoffMax bound the retry delay for a
	// volume that fails to wipe, so that a persistently failing device does
	// not spin.
	reaperBackoffBase = 30 * time.Second
	reaperBackoffMax  = 30 * time.Minute

	// Event reasons for the wipe reaper.
	wipedLogicalVolume    = "WipedLogicalVolume"
	wipeLogicalVolumeFail = "WipeLogicalVolumeFailed"
)

// ReaperConfig configures the wipe reaper.
type ReaperConfig struct {
	// Interval is the backstop sweep interval.
	Interval time.Duration
	// Concurrency is the number of volumes wiped at once.
	Concurrency int
}

// Reaper zeroes and removes quarantined logical volumes.
//
// Quarantine and wiping are split because DeleteVolume runs under the
// csi-provisioner sidecar's timeout, which is measured in seconds, while
// zeroing a volume is measured in minutes. Quarantine keeps the extents
// allocated so they cannot be handed to another tenant; the reaper does the
// slow part afterwards, with no deadline.
//
// The reaper is node-local: it acts on the LVM state of the node it runs on
// and never talks to the API server, so it must not take part in leader
// election.
type Reaper struct {
	core        *LVM
	recorder    kevents.EventRecorder
	interval    time.Duration
	concurrency int

	// mu guards backoff.
	mu sync.Mutex
	// backoff records when a volume that failed to wipe may next be retried,
	// keyed by "vg/lv".
	backoff map[string]*backoffEntry
}

// backoffEntry tracks retry state for a single failing volume.
type backoffEntry struct {
	failures int
	next     time.Time
}

// NewReaper creates a wipe reaper for the given LVM core.
func NewReaper(core *LVM, recorder kevents.EventRecorder, config ReaperConfig) (*Reaper, error) {
	if core == nil {
		return nil, fmt.Errorf("lvm core must not be nil")
	}
	if recorder == nil {
		return nil, fmt.Errorf("recorder must not be nil")
	}
	if config.Interval <= 0 {
		config.Interval = DefaultReaperInterval
	}
	if config.Concurrency <= 0 {
		config.Concurrency = DefaultReaperConcurrency
	}
	return &Reaper{
		core:        core,
		recorder:    recorder,
		interval:    config.Interval,
		concurrency: config.Concurrency,
		backoff:     make(map[string]*backoffEntry),
	}, nil
}

// Signal asks the reaper to sweep as soon as it can.
//
// The signal is owned by the LVM core rather than the reaper, so that the
// deletion path can signal unconditionally without knowing whether a reaper
// exists.
func (r *Reaper) Signal() {
	r.core.SignalWipe()
}

// Start runs the reaper until the context is cancelled, implementing
// manager.Runnable.
func (r *Reaper) Start(ctx context.Context) error {
	log := log.FromContext(ctx).WithName("lvm-wipe-reaper")
	log.V(2).Info("starting wipe reaper", "interval", r.interval, "concurrency", r.concurrency)

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	// Sweep unconditionally at startup. This is what makes quarantine
	// crash-safe: volumes tagged by a previous process, which will never be
	// signalled, are picked up here.
	if err := r.Reconcile(ctx); err != nil {
		log.Error(err, "initial wipe sweep failed")
	}

	for {
		select {
		case <-ctx.Done():
			log.V(2).Info("stopping wipe reaper")
			return nil
		case <-ticker.C:
		case <-r.core.wipeSignal:
		}

		if err := r.Reconcile(ctx); err != nil {
			log.Error(err, "wipe sweep failed")
		}
	}
}

// NeedLeaderElection returns false because the reaper acts only on node-local
// LVM state.
func (r *Reaper) NeedLeaderElection() bool {
	return false
}

// SetupWithManager registers the reaper with the controller manager.
func (r *Reaper) SetupWithManager(mgr ctrl.Manager) error {
	return mgr.Add(r)
}

// Reconcile wipes every quarantined volume that is due an attempt.
//
// It returns an error only when the sweep itself could not be performed.
// Failures to wipe an individual volume are recorded and retried with backoff,
// and do not fail the sweep, because one bad volume must not stop the others
// from being cleared.
func (r *Reaper) Reconcile(ctx context.Context) error {
	ctx, span := r.core.tracer.Start(ctx, "volume.lvm.csi/Reaper.Reconcile")
	defer span.End()

	log := log.FromContext(ctx).WithName("lvm-wipe-reaper")

	vgNames, err := r.volumeGroups(ctx)
	if err != nil {
		span.RecordError(err)
		return err
	}

	var pending []lvm.LogicalVolume
	swept := make([]string, 0, len(vgNames))
	for _, vgName := range vgNames {
		lvs, err := r.core.ListQuarantined(ctx, vgName)
		if err != nil {
			// Keep going: a group that cannot be read now is picked up by the
			// next sweep, and the others still get cleared.
			log.Error(err, "failed to list quarantined volumes", "vg", vgName)
			continue
		}
		swept = append(swept, vgName)
		pending = append(pending, lvs...)
	}

	r.pruneBackoff(pending, swept)

	due := make([]lvm.LogicalVolume, 0, len(pending))
	for _, lv := range pending {
		if r.isDue(lv) {
			due = append(due, lv)
		}
	}

	span.SetAttributes(
		attribute.Int("vol.quarantined_count", len(pending)),
		attribute.Int("vol.due_count", len(due)),
	)

	if len(due) == 0 {
		return nil
	}

	log.V(2).Info("wiping quarantined volumes", "due", len(due), "quarantined", len(pending))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(r.concurrency)
	for _, lv := range due {
		g.Go(func() error {
			if err := r.wipe(gctx, lv); err != nil {
				r.recordFailure(lv, err)
				log.Error(err, "failed to wipe quarantined volume", "vg", lv.VGName, "lv", lv.Name)
				return nil
			}
			r.recordSuccess(lv)
			log.Info("wiped and removed quarantined volume", "vg", lv.VGName, "lv", lv.Name)
			return nil
		})
	}

	// The goroutines above never return an error, so this only surfaces
	// context cancellation.
	return g.Wait()
}

// wipe clears a single quarantined volume and removes it.
//
// The order is activate, sanitize, remove. Activation comes first because a
// volume can be quarantined while deactivated, and a volume with no device
// node under /dev/<vg>/<lv> cannot be written to. Removal comes last because
// once lvremove has run the extents may be reallocated at any time and can no
// longer be reached, so a volume is only ever removed after it has been
// zeroed in full.
func (r *Reaper) wipe(ctx context.Context, lv lvm.LogicalVolume) error {
	ctx, span := r.core.tracer.Start(ctx, "volume.lvm.csi/Reaper.wipe", trace.WithAttributes(
		attribute.String("vol.group", lv.VGName),
		attribute.String("vol.name", lv.Name),
	))
	defer span.End()

	fullName := lv.VGName + "/" + lv.Name

	if err := r.core.lvm.UpdateLogicalVolume(ctx, lvm.UpdateLVOptions{
		Name:     fullName,
		Activate: lvm.Yes,
	}); err != nil {
		if lvm.IgnoreNotFound(err) == nil {
			// The volume was removed by something else between the listing and
			// now. There is nothing left to wipe.
			return nil
		}
		span.RecordError(err)
		return fmt.Errorf("failed to activate logical volume %s for wiping: %w", fullName, err)
	}

	// Refuse to write to a volume anything still has open.
	//
	// The exclusive open in SanitizeLogicalVolume is not sufficient on its
	// own. It conflicts with a mount, which covers filesystem volumes, but a
	// raw block volume is bind-mounted as a device node and a pod holding it
	// open takes no exclusive claim, so the open would succeed and the wipe
	// would corrupt a running workload. LVM's own open count sees that case.
	//
	// The volume is re-read here rather than trusting the listing, both
	// because the listing is stale by now and because lv_device_open is only
	// meaningful once the volume is active.
	current, err := r.core.lvm.GetLogicalVolume(ctx, lv.VGName, lv.Name)
	if err != nil {
		if lvm.IgnoreNotFound(err) == nil {
			return nil
		}
		span.RecordError(err)
		return fmt.Errorf("failed to re-read logical volume %s before wiping: %w", fullName, err)
	}
	if current == nil {
		return nil
	}
	if !IsQuarantineCommitted(*current) {
		// The volume was reinstated, or is not the volume that was listed.
		span.AddEvent("volume is no longer committed to destruction, skipping")
		return nil
	}
	if isDeviceOpen(*current) {
		span.AddEvent("volume is still open, deferring wipe")
		return fmt.Errorf("logical volume %s is still open and cannot be wiped yet", fullName)
	}

	if err := r.core.lvm.SanitizeLogicalVolume(ctx, lv.VGName, lv.Name); err != nil {
		span.RecordError(err)
		// Fail closed: the volume keeps its tag and its extents, and is
		// retried. It is never removed on a failed or partial wipe.
		return fmt.Errorf("failed to sanitize logical volume %s: %w", fullName, err)
	}

	if err := r.core.lvm.RemoveLogicalVolume(ctx, lvm.RemoveLVOptions{Name: fullName}); err != nil {
		if lvm.IgnoreNotFound(err) == nil {
			// Already gone, and it was sanitized before it went.
			return nil
		}
		span.RecordError(err)
		return fmt.Errorf("failed to remove sanitized logical volume %s: %w", fullName, err)
	}

	r.event(corev1.EventTypeNormal, wipedLogicalVolume,
		"Sanitized and removed logical volume %s on node %s", fullName, r.core.nodeName)

	return nil
}

// volumeGroups returns the volume groups to sweep.
func (r *Reaper) volumeGroups(ctx context.Context) ([]string, error) {
	return ManagedVolumeGroups(ctx, r.core.lvm)
}

// isDue reports whether a volume may be attempted now.
func (r *Reaper) isDue(lv lvm.LogicalVolume) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.backoff[lv.VGName+"/"+lv.Name]
	return !ok || !time.Now().Before(entry.next)
}

// recordFailure schedules the next attempt for a volume that failed to wipe.
func (r *Reaper) recordFailure(lv lvm.LogicalVolume, err error) {
	key := lv.VGName + "/" + lv.Name

	r.mu.Lock()
	entry, ok := r.backoff[key]
	if !ok {
		entry = &backoffEntry{}
		r.backoff[key] = entry
	}
	entry.failures++
	delay := reaperBackoffBase << min(entry.failures-1, 16)
	if delay > reaperBackoffMax || delay <= 0 {
		delay = reaperBackoffMax
	}
	entry.next = time.Now().Add(delay)
	failures := entry.failures
	r.mu.Unlock()

	r.event(corev1.EventTypeWarning, wipeLogicalVolumeFail,
		"Failed to sanitize logical volume %s on node %s (attempt %d, retrying in %s); "+
			"the volume is retained and its capacity is not reclaimed until it is wiped: %v",
		key, r.core.nodeName, failures, delay, err)
}

// recordSuccess clears retry state for a volume that has been wiped.
func (r *Reaper) recordSuccess(lv lvm.LogicalVolume) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.backoff, lv.VGName+"/"+lv.Name)
}

// pruneBackoff drops retry state for volumes that are no longer quarantined,
// so that the map cannot grow without bound over the lifetime of the process.
//
// Only volume groups that were listed successfully are pruned. Pruning a group
// whose listing failed would clear the retry state of every volume in it, so
// an intermittently unreadable device would reset its own backoff on every
// sweep and be retried at the base interval forever, which is the spin the
// backoff exists to prevent.
func (r *Reaper) pruneBackoff(pending []lvm.LogicalVolume, sweptGroups []string) {
	live := make(map[string]struct{}, len(pending))
	for _, lv := range pending {
		live[lv.VGName+"/"+lv.Name] = struct{}{}
	}
	swept := make(map[string]struct{}, len(sweptGroups))
	for _, vg := range sweptGroups {
		swept[vg] = struct{}{}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.backoff {
		vg, _, ok := strings.Cut(key, "/")
		if !ok {
			continue
		}
		if _, sweptOK := swept[vg]; !sweptOK {
			continue
		}
		if _, liveOK := live[key]; !liveOK {
			delete(r.backoff, key)
		}
	}
}

// event records an event against the node the reaper is running on. There is
// no PersistentVolume to attach to: by the time a volume is quarantined its PV
// is already gone.
func (r *Reaper) event(eventType, reason, messageFmt string, args ...any) {
	r.recorder.Eventf(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: r.core.nodeName}}, nil,
		eventType, reason, reason, messageFmt, args...,
	)
}

// lvAttrOpenIndex is the position of the open indicator in lv_attr.
const lvAttrOpenIndex = 5

// isDeviceOpen reports whether anything currently has the volume's device
// open, erring towards "open" when LVM's answer cannot be trusted.
//
// Two fields are consulted because each fails differently. lv_device_open
// decodes to false both when the device is closed and when the field is
// missing or unparsable, so on its own a renamed or dropped field would
// silently degrade into "safe to wipe". lv_attr carries the same information
// positionally, and an attribute string too short to contain it is treated as
// unknown, and therefore as open.
func isDeviceOpen(lv lvm.LogicalVolume) bool {
	if bool(lv.DeviceOpen) {
		return true
	}
	if len(lv.Attributes) <= lvAttrOpenIndex {
		return true
	}
	return lv.Attributes[lvAttrOpenIndex] != '-'
}
