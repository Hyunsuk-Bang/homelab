# config/bootstrap

**Reference snapshot only.** The management cluster (k3s + Cilium/BGP + Kamaji + Cluster API)
is owned by the **homelab repo** (`~/homelab`: `ansible/`, `bgp/`, `helm/apps`), which deploys
it via Ansible + ArgoCD. Don't bootstrap from here.

These files are a captured-from-live copy kept for quick reference / disaster recovery if the
homelab repo isn't handy:
- `cilium-values.yaml`, `cilium-bgp.yaml` — mirror `homelab/helm/apps` (cilium) + `homelab/bgp/`.
- `install.sh` — a standalone fallback that reproduces the homelab bootstrap order.

Source of truth = homelab. See `_docs/REBUILD.md`. The caas operator itself is deployed by the
homelab GitOps (`homelab/helm/apps` → `caas/charts/caas`).
