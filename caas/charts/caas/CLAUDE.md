# charts/caas

Helm chart — the easy way to deploy the operator. Packages the CRDs, RBAC, Deployment,
Service, and (optionally) the Proxmox/UniFi credential Secrets.

- `crds/` — the two CRDs (Helm installs these before templates; **not** upgraded/deleted by
  Helm — update by hand or `helm upgrade` won't touch them). Keep in sync with
  `config/crd/bases/` after `make manifests`.
- `templates/` — SA, ClusterRole+Binding, Secrets (created from values unless
  `*.existingSecret` is set), Deployment, Service, NOTES.txt.
- `values.yaml` — image (`banghsk99/caas` on public Docker Hub, `pullPolicy: IfNotPresent`),
  web config, and `proxmox` / `unifi` creds. Schedules anywhere (`nodeSelector: {}`).

Install (homelab, reusing existing secrets):
```
helm install caas charts/caas -n caas-system --create-namespace \
  --set proxmox.existingSecret=proxmox-credentials \
  --set unifi.enabled=true --set unifi.existingSecret=unifi-credentials
```
Or provide creds inline via `--set proxmox.url=…,proxmox.tokenID=…,proxmox.tokenSecret=…`
(a Secret is created). Resource names match the chart fullname, so release name `caas`
reproduces the manual `config/deploy/caas.yaml` names exactly.

Image: `banghsk99/caas` is public on Docker Hub, so kubelet pulls it — no containerd import
or node pinning needed. Build/push a new tag with `docker buildx build --platform linux/amd64
-f Dockerfile.prebuilt -t banghsk99/caas:<tag> --load . && docker push banghsk99/caas:<tag>`.
