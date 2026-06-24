#!/usr/bin/env bash
# Bootstrap the management cluster the caas operator runs on.
# Run AFTER k3s is up (see _docs/REBUILD.md for k3s flags). Idempotent-ish; re-running
# helm installs will error — use `helm upgrade --install` if needed.
set -euo pipefail
export KUBECONFIG=${KUBECONFIG:-/etc/rancher/k3s/k3s.yaml}
HERE="$(cd "$(dirname "$0")" && pwd)"

echo "== 1/5 Cilium (CNI + kube-proxy + BGP LB) =="
helm repo add cilium https://helm.cilium.io >/dev/null
helm repo update cilium >/dev/null
helm install cilium cilium/cilium --version 1.19.1 -n kube-system \
  -f "$HERE/cilium-values.yaml"
cilium status --wait || true
kubectl apply -f "$HERE/cilium-bgp.yaml"
# NOTE: configure the matching BGP peer on the UniFi router (ASN 65100 <-> 65200).

echo "== 2/5 Kamaji (hosted control planes + bundled etcd datastore 'default') =="
helm repo add clastix https://clastix.github.io/charts >/dev/null
helm repo update clastix >/dev/null
helm install kamaji clastix/kamaji -n kamaji-system --create-namespace \
  --set defaultDatastoreName=default
kubectl -n kamaji-system rollout status deploy/kamaji --timeout=180s

echo "== 3/5 Cluster API + providers =="
# clusterctl reads provider creds from env / clusterctl config; capmox also takes a
# per-cluster credentials Secret (which the operator renders), so global creds are optional.
clusterctl init \
  --core cluster-api:v1.13.1 \
  --bootstrap kubeadm:v1.13.1 \
  --control-plane kamaji:v0.19.0 \
  --infrastructure proxmox:v0.8.1 \
  --ipam in-cluster:v1.0.3

echo "== 4/5 caas operator image =="
# No registry: import the image into k3s containerd on this node (k3s0).
#   (build elsewhere:  docker build -f Dockerfile.prebuilt -t caas:0.1.5 . && docker save -o caas.tar caas:0.1.5)
[ -f /tmp/caas-image.tar ] && sudo k3s ctr images import /tmp/caas-image.tar || \
  echo "  -> import caas:0.1.5 manually: sudo k3s ctr images import caas-image.tar"

echo "== 5/5 caas operator (Helm) =="
kubectl create namespace caas-system --dry-run=client -o yaml | kubectl apply -f -
# Create credential secrets first (edit values):
#   kubectl -n caas-system create secret generic proxmox-credentials \
#     --from-literal=url=https://192.168.10.10:8006 --from-literal=tokenID='root@pam!CAPI' --from-literal=tokenSecret=<...>
#   kubectl -n caas-system create secret generic unifi-credentials \
#     --from-literal=url=https://192.168.20.1 --from-literal=apiKey=<...> --from-literal=site=default
helm install caas "$HERE/../../charts/caas" -n caas-system \
  --set proxmox.existingSecret=proxmox-credentials \
  --set unifi.enabled=true --set unifi.existingSecret=unifi-credentials
kubectl apply -f "$HERE/../samples/caas_v1alpha1_vlanpool.yaml"

echo "Done. UI: http://<node-ip>:30090/"
