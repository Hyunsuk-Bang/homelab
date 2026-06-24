# internal/controller

The `TenantCluster` reconciler — orchestrates the whole lifecycle. `MaxConcurrentReconciles=1`
(serializes VLAN allocation). Status is written once per reconcile via a deferred
non-optimistic merge patch (avoids resourceVersion conflicts).

Reconcile order (create):
1. allocate VLAN (only if unset) — `internal/allocator`
2. `ensureNamespace` — per-cluster namespace, dual ownership labels (never adopts a ns it
   doesn't own)
3. `reconcileNetwork` — `internal/network` provider; sets `NetworkReady`
4. apply KamajiControlPlane **+ Cluster + creds together**, then wait for the Kamaji
   endpoint, then apply ProxmoxCluster + workers — `internal/render`
5. mirror CP/worker status; `reconcileCNI` installs Cilium — `internal/cni`
6. `Ready = Network && ControlPlane && Workers && CNI`

Delete (`reconcileDelete`, finalizer-gated, ordered): delete CAPI Cluster (VMs torn down
while creds Secret still exists) → delete namespace → `DeleteNetwork` → release finalizer
(frees the VLAN).

Why no ownerRefs on the bundle: it lives in the per-cluster namespace while the
TenantCluster lives in the control namespace — ownerRefs can't cross namespaces, so cleanup
is by deleting the namespace. RBAC markers here drive `make manifests`.

Critical invariants the renderer must keep (see `internal/render`): KamajiControlPlane ref =
`v1alpha1`; worker NIC VLAN-tagged; kubelet feature gates for k8s 1.34.
