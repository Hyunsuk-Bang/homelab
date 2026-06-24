# Phase 1 — CRDs + allocator + reconciler

What this delivers: apply a `TenantCluster` (and a `VLANPool`) to the **management**
cluster and the operator renders + applies the full CAPI bundle (Kamaji control plane,
Proxmox infra, workers) with a VLAN allocated automatically. No web UI yet (Phase 3); no
auto-CNI yet (Phase 2 — nodes come up `NotReady` until you install a CNI in the tenant,
as we did by hand).

## Layout
```
api/v1alpha1/          VLANPool + TenantCluster types
internal/allocator/    VLAN allocation (node subnet derived as 10.<vlan>.0.0/16)
internal/render/       renders the CAPI bundle as unstructured (the 4 validated fixes baked in)
internal/controller/   TenantCluster reconciler (allocate → apply → mirror status)
config/crd/bases/      generated CRDs (TenantCluster=Namespaced, VLANPool=Cluster)
config/samples/        applyable VLANPool + TenantCluster examples
```

## Run against the homelab mgmt cluster (out-of-cluster)
```sh
# point at the k3s management cluster (e.g. copied from k3s0:/etc/rancher/k3s/k3s.yaml)
export KUBECONFIG=~/.kube/homelab

make install                       # install the two CRDs

kubectl apply -f config/samples/caas_v1alpha1_vlanpool.yaml

# Proxmox creds the operator renders into each tenant's credentials Secret:
export PROXMOX_URL=https://192.168.10.10:8006
export PROXMOX_TOKEN_ID='root@pam!CAPI'
export PROXMOX_TOKEN_SECRET=<token-secret>

make run                           # runs cmd/main.go locally against KUBECONFIG

# in another shell:
kubectl apply -f config/samples/caas_v1alpha1_tenantcluster.yaml
kubectl get tenantcluster -w       # watch Phase/VLAN/Endpoint/Workers
```

Deleting the `TenantCluster` cascades (ownerRefs) to the CAPI objects → capmox removes the
VMs → the VLAN is freed (it's derived from the live TenantCluster list).

## Deliberately not done in Phase 1
- **CNI auto-install** (Phase 2): `spec.cni.type: cilium` is recorded but not yet acted on.
- **Delete wait**: `reconcileDelete` removes the finalizer immediately and relies on GC; a
  future version should block until children are gone.
- **client.Apply deprecation**: controller-runtime v0.23 deprecates `client.Apply`; switch
  to `Client.Apply()` when convenient (cosmetic).
- **Owns() watches**: status refresh is via periodic requeue (30s) rather than watching the
  unstructured children.
