// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lvm

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"local-csi-driver/internal/pkg/lvm"
)

const (
	// WipePendingTag marks a logical volume that has been taken out of service
	// and is waiting to be sanitized.
	//
	// The tag is what makes quarantine recoverable: it is written to LVM
	// metadata, so it survives a driver restart or a node reboot and lets the
	// reaper find volumes abandoned by a previous process.
	//
	// It follows the short-tag convention set by DefaultVolumeGroupTag. LVM
	// restricts tags to a limited character set that excludes "/", so the full
	// driver name cannot be used directly.
	WipePendingTag = "local-csi-wipe-pending"

	// quarantineNamePrefix is applied to a logical volume when it is
	// quarantined, to move it out of the namespace used for live volumes.
	//
	// It is driver-specific rather than generic, because the name is the sole
	// marker of a volume that is committed to destruction. A generic prefix
	// would let an unrelated logical volume that happened to be named that way
	// be zeroed by the reaper.
	quarantineNamePrefix = "local-csi-wipe-"
)

// Quarantine takes a logical volume out of service pending sanitization, and
// returns the name it was renamed to.
//
// Deletion cannot zero a volume synchronously: DeleteVolume is called by the
// csi-provisioner sidecar under a timeout measured in seconds, while zeroing a
// volume is measured in minutes. Quarantine is what makes the split safe. The
// extents stay allocated to the renamed volume, so LVM cannot hand them to
// another tenant, and the reaper zeroes and removes the volume afterwards
// without a deadline.
//
// The rename alone is the commit point for destruction. The reaper wipes a
// volume if and only if its name is in the quarantine namespace, and the tag
// is not part of that test. Keying destruction on the name is what makes the
// two writes independently survivable: a committed volume may legitimately
// lose its tag, but a volume that is tagged while still under its own name has
// not been taken out of service, may still be referenced by a
// PersistentVolume, and must never be zeroed.
//
// The tag is nevertheless written first. Its only remaining consumer is the
// orphan scanner, which leaves a tagged volume alone so that a quarantine in
// progress is not concurrently reclaimed by another path. Writing it first
// means a crash between the two writes leaves a volume that is still in
// service and still protected. If the rename fails the tag is rolled back and
// the caller retries the whole operation.
//
// Renaming also frees the original name, so that a subsequent CreateVolume for
// the same volume ID cannot collide with a volume that is still waiting to be
// wiped.
func (l *LVM) Quarantine(ctx context.Context, vgName, lvName string) (string, error) {
	ctx, span := l.tracer.Start(ctx, "volume.lvm.csi/Quarantine", trace.WithAttributes(
		attribute.String("vol.group", vgName),
		attribute.String("vol.name", lvName),
	))
	defer span.End()

	if vgName == "" || lvName == "" {
		return "", fmt.Errorf("volume group and logical volume names cannot be empty")
	}

	fullName := vgName + "/" + lvName

	// Tag first, so a crash before the rename still leaves the volume
	// protected from the orphan scanner while it is in service.
	err := l.lvm.UpdateLogicalVolume(ctx, lvm.UpdateLVOptions{
		Name:    fullName,
		AddTags: []string{WipePendingTag},
	})
	if err != nil {
		span.RecordError(err)
		return "", fmt.Errorf("failed to tag logical volume %s for wiping: %w", fullName, err)
	}

	quarantinedName := quarantineNamePrefix + uuid.NewString()
	span.SetAttributes(attribute.String("vol.quarantined_name", quarantinedName))

	err = l.lvm.RenameLogicalVolume(ctx, lvm.RenameLVOptions{
		From: fullName,
		To:   vgName + "/" + quarantinedName,
	})
	if err != nil {
		span.RecordError(err)
		// Roll the tag back. Destruction is committed by the rename, so this
		// volume is still in service under its original name and must not be
		// left carrying a tag that suggests otherwise.
		if delErr := l.ClearQuarantineTag(ctx, vgName, lvName); delErr != nil {
			// The volume is still safe: it was never renamed, so the reaper
			// will not touch it. It is now hidden from the orphan scanner
			// though, so surface this rather than leaving it only on the span.
			log.FromContext(ctx).Error(delErr, "failed to roll back the wipe tag after a failed rename; "+
				"the volume remains in service but is hidden from orphan cleanup until the tag is cleared",
				"vg", vgName, "lv", lvName)
			span.RecordError(delErr)
		}
		return "", fmt.Errorf("failed to rename logical volume %s for wiping: %w", fullName, err)
	}

	span.AddEvent("quarantined logical volume")
	l.SignalWipe()
	return quarantinedName, nil
}

// ClearQuarantineTag removes the wipe-pending tag from a logical volume that
// is still under its original name.
//
// This reinstates a volume whose quarantine was started but never committed,
// which happens if the driver dies between tagging and renaming, or if the
// rename fails. Such a volume was never taken out of service and is still
// owned by the workload that created it, so returning it to service exposes
// nothing: it is the same volume, for the same volume ID, to the same tenant.
//
// It deliberately refuses to act on a volume that has already been renamed,
// because that volume is committed to destruction and must not be reinstated.
func (l *LVM) ClearQuarantineTag(ctx context.Context, vgName, lvName string) error {
	ctx, span := l.tracer.Start(ctx, "volume.lvm.csi/ClearQuarantineTag", trace.WithAttributes(
		attribute.String("vol.group", vgName),
		attribute.String("vol.name", lvName),
	))
	defer span.End()

	// The refusal set is exactly the committed set, so that a volume this
	// driver never committed cannot be made permanently unreinstateable by
	// merely resembling one.
	if isQuarantineName(lvName) {
		return fmt.Errorf("refusing to clear the wipe tag from quarantined logical volume %s/%s", vgName, lvName)
	}

	err := l.lvm.UpdateLogicalVolume(ctx, lvm.UpdateLVOptions{
		Name:    vgName + "/" + lvName,
		DelTags: []string{WipePendingTag},
	})
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to clear the wipe tag from logical volume %s/%s: %w", vgName, lvName, err)
	}
	return nil
}

// SignalWipe asks the wipe reaper to sweep as soon as it can.
//
// It never blocks. Callers are on the deletion path, holding a CSI request
// under a sidecar timeout, and must not be held up behind a wipe that is
// already running. It is also safe to call when no reaper is running: the
// signal is simply never observed, and the periodic sweep would pick the
// volume up anyway.
func (l *LVM) SignalWipe() {
	select {
	case l.wipeSignal <- struct{}{}:
	default:
	}
}

// ListQuarantined returns the logical volumes in a volume group that are
// committed to destruction and waiting to be sanitized.
//
// Discovery deliberately does not use the tag as a server-side selector. The
// tag can be absent from a committed volume: EnsureVolume clears the tag to
// reinstate an uncommitted quarantine, and if that lands between Quarantine's
// tag and rename the result is a renamed volume with no tag. Selecting on the
// tag would leave such a volume invisible forever, holding extents that still
// carry the tenant's data. The generated name is the durable marker, so the
// group is listed in full and filtered on that.
func (l *LVM) ListQuarantined(ctx context.Context, vgName string) ([]lvm.LogicalVolume, error) {
	ctx, span := l.tracer.Start(ctx, "volume.lvm.csi/ListQuarantined", trace.WithAttributes(
		attribute.String("vol.group", vgName),
	))
	defer span.End()

	lvs, err := l.lvm.ListLogicalVolumes(ctx, &lvm.ListLVOptions{
		Names: []string{vgName},
	})
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to list quarantined logical volumes in %s: %w", vgName, err)
	}

	quarantined := make([]lvm.LogicalVolume, 0, len(lvs))
	for _, lv := range lvs {
		if IsQuarantineCommitted(lv) {
			quarantined = append(quarantined, lv)
		}
	}

	span.SetAttributes(attribute.Int("vol.quarantined_count", len(quarantined)))
	return quarantined, nil
}

// IsQuarantineCommitted reports whether a logical volume has been committed to
// destruction and may therefore be zeroed and removed.
//
// This is the only predicate a caller may use to decide to destroy a volume.
// It tests the name, not the tag, because the rename is the commit point: a
// volume that was tagged but never taken out of service is not a wipe target,
// and a volume that was renamed but lost its tag still is. Relying on the tag
// here would make the latter invisible to the reaper, stranding its extents
// with the tenant's data still on them.
//
// The name is trustworthy because only Quarantine produces it, and the CSI
// layer rejects volume handles that name themselves into this namespace.
func IsQuarantineCommitted(lv lvm.LogicalVolume) bool {
	return isQuarantineName(lv.Name)
}

// isQuarantineName reports whether a logical volume name was generated by
// Quarantine.
//
// The full generated shape is required, prefix and UUID both, so that an
// arbitrary name merely beginning with the prefix is not mistaken for a volume
// the driver committed to destroying.
func isQuarantineName(name string) bool {
	suffix, ok := strings.CutPrefix(name, quarantineNamePrefix)
	if !ok {
		return false
	}
	_, err := uuid.Parse(suffix)
	return err == nil
}

// IsQuarantined reports whether a logical volume has been tagged for
// sanitization, whether or not that quarantine was committed.
//
// Callers that delete logical volumes must use this to skip tagged volumes.
// The orphan scanner in particular would otherwise treat every quarantined
// volume as an orphan, since by construction none of them has a corresponding
// PersistentVolume, and race the reaper to remove them without zeroing. Use
// IsQuarantineCommitted, not this, to decide whether a volume may be
// destroyed.
func IsQuarantined(lv lvm.LogicalVolume) bool {
	for _, tag := range strings.Split(lv.Tags, ",") {
		if strings.TrimSpace(tag) == WipePendingTag {
			return true
		}
	}
	return false
}

// VolumeGroupLister lists volume groups. It is the subset of lvm.Manager
// needed to discover the groups this driver manages.
type VolumeGroupLister interface {
	ListVolumeGroups(ctx context.Context, opts *lvm.ListVGOptions) ([]lvm.VolumeGroup, error)
}

// ManagedVolumeGroups returns the volume groups this driver is responsible
// for on the local node.
//
// Groups are found by tag. When none carry the tag the default group is
// returned anyway, so that groups created before the tag convention are still
// swept rather than silently ignored: a component that skipped them would
// leave quarantined volumes in them stranded forever.
func ManagedVolumeGroups(ctx context.Context, lister VolumeGroupLister) ([]string, error) {
	vgs, err := lister.ListVolumeGroups(ctx, &lvm.ListVGOptions{
		Select: fmt.Sprintf("vg_tags=%s", DefaultVolumeGroupTag),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list volume groups with tag %s: %w", DefaultVolumeGroupTag, err)
	}

	if len(vgs) == 0 {
		return []string{DefaultVolumeGroup}, nil
	}

	names := make([]string, 0, len(vgs))
	for _, vg := range vgs {
		names = append(names, vg.Name)
	}
	return names, nil
}
