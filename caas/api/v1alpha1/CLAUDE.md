# api/v1alpha1

The two CRDs (group `caas.hbang.io`).

- **`VLANPool`** (cluster-scoped, singleton "default") — admin config: allocatable VLAN
  range + how the node subnet is derived (`10.<vlan>.0.0/24`), pod/service CIDRs (reused
  across tenants), the control-plane BGP pool. `vlanpool_types.go`.
- **`TenantCluster`** (namespaced) — the user's intent: k8s version, CP replicas, worker
  pools (node/template/replicas/cores/mem/disk), CNI. Its `status` carries the allocation,
  the per-cluster `namespace`, control-plane endpoint, and conditions. `tenantcluster_types.go`.

Condition types and phases are consts in `tenantcluster_types.go`. `LabelCluster` /
`LabelControlNamespace` mark operator-owned namespaces.

After editing types, run `make generate manifests` (regenerates `zz_generated.deepcopy.go`
and the CRD YAML under `config/crd/bases/`). Both CRDs use a `status` subresource.

Gotcha: label *values* can't contain `/` — that's why namespace ownership uses two labels.
