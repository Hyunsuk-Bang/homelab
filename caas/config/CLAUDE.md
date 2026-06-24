# config

Kustomize manifests (kubebuilder layout) + our deploy.

- `deploy/caas.yaml` — **the one to use**: self-contained operator deployment (namespace
  `caas-system`, ServiceAccount, ClusterRole+Binding, Deployment pinned to k3s0 with
  `imagePullPolicy: Never`, NodePort `caas-web`:30090). Header lists the 3 prereqs (CRDs,
  the `proxmox-credentials` + `unifi-credentials` Secrets, image imported into k3s).
- `crd/bases/` — generated CRDs (`make manifests`). Apply these.
- `rbac/role.yaml` — generated from controller `+kubebuilder:rbac` markers; the deploy
  manifest's ClusterRole mirrors it (keep them in sync when markers change).
- `samples/` — applyable `VLANPool` (the "default" pool: VLANs 30–33, `/24`) and an example
  `TenantCluster`.
- `default/`, `manager/`, `prometheus/`, `network-policy/` — stock kubebuilder kustomize; not
  used by the homelab deploy.

Gotcha: `deploy/caas.yaml` pins the image tag, so `kubectl apply -f` overrides any
`kubectl set image` — bump the tag in the file when rolling a new build.
