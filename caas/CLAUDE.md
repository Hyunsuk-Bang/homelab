# caas

Self-service Kubernetes cluster-as-a-service. A k3s **management cluster** runs Kamaji
(hosted control planes) + Cluster API; each tenant cluster's **workers are Proxmox VMs**.
A single Go operator (controller-runtime) + embedded web UI launches/lists/deletes clusters.

One `TenantCluster` CR → the operator allocates a VLAN, creates a per-cluster namespace,
realizes the VLAN network (UniFi), renders the CAPI bundle (Kamaji CP + Proxmox workers),
and installs the CNI. Delete reverses it all and frees the VLAN.

## Layout
- `api/v1alpha1/` — CRDs (`TenantCluster`, `VLANPool`)
- `internal/controller/` — the reconciler (orchestrates everything)
- `internal/allocator/` — VLAN → subnet allocation
- `internal/render/` — renders the CAPI object bundle
- `internal/network/` — vendor-agnostic VLAN/firewall provider (UniFi + noop)
- `internal/cni/` — installs Cilium into tenants
- `internal/web/` — embedded launch/list/delete UI
- `cmd/` — entrypoint + wiring
- `config/deploy/caas.yaml` — self-contained in-cluster deployment

## Build & deploy
`make build` / `make manifests`. Image is built via `Dockerfile.prebuilt` (copies the
cross-compiled `manager` binary), imported into k3s containerd on k3s0 (no registry),
deployed from `config/deploy/caas.yaml`. See that file's header for prereqs.

## Key facts
- Runs against the homelab via `ssh -i ~/.ssh/ansible hbang@192.168.20.3` (k3s0).
- "Kamaji" = the upstream hosted-control-plane provider; not part of this project's name.
- Detailed history & fixes in `_docs/` (esp. `TROUBLESHOOTING-initial-cluster.md`).
