# Rebuilding the management cluster

The management cluster (k3s + Cilium/BGP + Kamaji + Cluster API) is **owned by the homelab
repo** (`~/homelab`), not this one. Don't reconstruct it here — use:

- `homelab/ansible/k3s/` — k3s install (flannel/kube-proxy/servicelb off, `--cluster-init`).
- `homelab/ansible/bootstrap/` — Helm + ArgoCD; ArgoCD then GitOps-deploys everything from
  `homelab/helm/apps` (Cilium, Kamaji, Cluster API providers, …).
- `homelab/bgp/cilium-bgp.yaml` — the Cilium BGP / LoadBalancer config (cluster ASN 65200 ↔
  UniFi router ASN 65100). **The UniFi router needs the matching BGP peer**, else Kamaji
  control-plane VIPs (172.17.x) are unreachable and tenant `kubeadm join` fails.

This repo only owns the **caas operator** layer, which the homelab deploys via ArgoCD
(`homelab/helm/apps` → `caas/charts/caas`). The operator replaces the
old `homelab/management-helm`.

## caas-specific steps on a fresh cluster (after the homelab bootstrap)
1. Build + import the image (no registry):
   `docker build -f Dockerfile.prebuilt -t caas:0.1.5 .` → `docker save` →
   `sudo k3s ctr images import` on k3s0.
2. Ensure `caas-system` has the `proxmox-credentials` + `unifi-credentials` Secrets (ideally
   via the homelab's external-secrets).
3. ArgoCD syncs the `caas` app (or `helm install caas charts/caas -n caas-system …`).
4. `kubectl apply -f config/samples/caas_v1alpha1_vlanpool.yaml`.

See `config/bootstrap/` for the captured Cilium/BGP values kept as a reference snapshot.
