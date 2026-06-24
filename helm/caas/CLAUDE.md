# charts/caas

Helm chart — the easy way to deploy the operator. Packages the CRDs, RBAC, Deployment,
Service, and (optionally) the Proxmox/UniFi credential Secrets.

- `crds/` — the two CRDs (Helm installs these before templates; **not** upgraded/deleted by
  Helm — update by hand or `helm upgrade` won't touch them). Keep in sync with
  `config/crd/bases/` after `make manifests`.
- `templates/` — SA, ClusterRole+Binding, Secrets (created from values unless
  `*.existingSecret` is set), Deployment, Service, NOTES.txt.
- `values.yaml` — image, nodeSelector (defaults to `k3s0` + `pullPolicy: Never` for the
  registry-less homelab), web config, and `proxmox` / `unifi` creds.

Install (homelab, reusing existing secrets):
```
helm install caas charts/caas -n caas-system --create-namespace \
  --set proxmox.existingSecret=proxmox-credentials \
  --set unifi.enabled=true --set unifi.existingSecret=unifi-credentials
```
Or provide creds inline via `--set proxmox.url=…,proxmox.tokenID=…,proxmox.tokenSecret=…`
(a Secret is created). Resource names match the chart fullname, so release name `caas`
reproduces the manual `config/deploy/caas.yaml` names exactly.

Note: image has no registry — it must already be imported into k3s containerd on the target
node (`sudo k3s ctr images import caas-image.tar`), hence `pullPolicy: Never` + the nodeSelector.
