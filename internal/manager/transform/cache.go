// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package transform provides controller-runtime cache transforms that
// trim cached objects down to the field set the manager actually reads.
//
// IMPORTANT: callers must never pass a trimmed object to client.Update.
// Update sends the entire object body to the API server; sending a
// trimmed object would zero out every field the transform stripped on
// the server. Use client.Patch with MergeFrom (which diffs the trimmed
// snapshot against itself, so the wire payload contains only the
// intended change) or client.Delete (which only uses
// Name/UID/ResourceVersion).
package transform

import (
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ManagerCacheOptions returns the controller-runtime cache.Options used
// by the manager binary and its envtest suites. Centralising the config
// here ensures tests exercise the same trim transforms as production,
// so a consumer that reads a stripped field will fail in tests instead
// of silently misbehaving in the cluster.
//
// Why this shape:
//
//   - DefaultTransform strips ManagedFields from every cached object.
//     Manager controllers and webhooks do not use server-side apply,
//     and the per-writer entries from csi-provisioner, external-attacher,
//     kube-controller-manager, and kubelet are the dominant share of
//     cache memory at scale on PVs and PVCs.
//
//   - TrimNode is wired per-type because Node.Status.Images alone is
//     5-50 KB per node (one entry per cached container image). At a
//     thousand nodes that dwarfs the ManagedFields savings.
//
//   - PVs and PVCs intentionally have no per-type trimmer. The
//     residual savings beyond ManagedFields stripping are small
//     (~5-20 MB at 10K objects) and the coupling cost is high: any
//     future controller or webhook that reads a stripped field via
//     the cached client would silently see a zero value.
//
// Per-type transforms in ByObject replace (do not compose with)
// DefaultTransform for matched types, so TrimNode strips ManagedFields
// itself by constructing a fresh ObjectMeta.
func ManagerCacheOptions() cache.Options {
	return cache.Options{
		DefaultTransform: cache.TransformStripManagedFields(),
		ByObject: map[client.Object]cache.ByObject{
			&corev1.Node{}: {Transform: TrimNode},
		},
	}
}
