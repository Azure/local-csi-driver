# Persistent Volume (PV) Cleanup

Status: implemented

## Background

PVCs and PVs can get stuck, because they can't be deleted if the node they
were provisioned on no longer exists. Pods referring to PVs will also be stuck
since they can only start on the deleted node.

Normally, it requires manual intervention to remove the PVC and PVs. This is the
guidance from [external-provisioner]:

> When an administrator is sure that the node is never going to come back, then
> the local volumes can be removed manually:
>
> <!-- markdownlint-disable MD033 -->
> - force-delete objects: kubectl delete pv <pv> --wait=false --grace-period=0 --force
> - remove all finalizers: kubectl patch pv <pv> -p '{"metadata":{"finalizers":null}}'
> <!-- markdownlint-enable MD033 -->
>
> It may also be necessary to scrub disks before reusing them because the CSI
> driver had no chance to do that.
>
> If there still was a PVC which was bound to that PV, it then will be moved to
> phase "Lost". It has to be deleted and re-created if still needed because no
> new volume will be created for it. Editing the PVC to revert it to phase
> "Unbound" is not allowed by the Kubernetes API server.

## Goals

- Allow Pods to recover on another node without manual intervention.
- Garbage collect orphaned PVs.

## Non-goals

- Persistence/recovery of data when a Pod starts on another node. The assumption
  is that when the node and its data is lost, the application can re-hydrate any
  required data.

## Design

### PV Cleanup

In local-csi-driver, the CSI external-provisioner is deployed with the
`--node-deployment` flag. This changes the default behaviour where a single
driver instance in a Deployment processes all create/delete requests. Instead,
with the `--node-deployment` flag, each instance in the DaemonSet sees the
request and decides whether to process it.

When a PV Delete request is made, all nodes that match the volumes's generated
volume affinity (including node topology) process the request.

Normally, accessible topology is set on the volume that restricts access to only
the node where the volume was provisioned. The scheduler uses this for Pod
placement. In this case, only the node with the volume will process the Delete
volume request. When this node no longer exists, the PV becomes stuck because no
node responds to the request.

However, when the local-csi-driver's [Hyperconverged
webhook][hyperconverged-webhook] is enabled, accessible topology isn't set, so
all nodes process the request.

When each driver instance receives the request, normally it would ignore
requests for volumes on other nodes. Instead, we check if the node is deleted
first, and return that the CSI Delete operation was successful if it was.

When the [Hyperconverged webhook][hyperconverged-webhook] is not enabled, manual
cleanup steps as described above must be used.

## Related Documentation

- [external-provisioner: Deleting local volumes after a node failure or removal][external-provisioner-cleanup]
- [local-static-provisioner: Local Volume Node Cleanup Controller][static-provisioner-cleanup]

[external-provisioner]: https://github.com/kubernetes-csi/external-provisioner
[external-provisioner-cleanup]:
    https://github.com/kubernetes-csi/external-provisioner?tab=readme-ov-file#deleting-local-volumes-after-a-node-failure-or-removal
[static-provisioner-cleanup]:
    https://github.com/kubernetes-sigs/sig-storage-local-static-provisioner/blob/master/docs/node-cleanup-controller.md
[hyperconverged-webhook]: webhooks.md
