# cmd

`main.go` — the operator entrypoint and dependency wiring (kubebuilder scaffold + our additions).

Builds the manager, then constructs and registers:
- the `TenantClusterReconciler` with its `ProxmoxConfig` (from `PROXMOX_*` flags/env) and the
  selected network `Provider`;
- the **network provider**: UniFi if `UNIFI_URL` + `UNIFI_API_KEY` are set, else `Noop`;
- the **web UI** (unless `--web-bind-address` is empty), with its node-template and
  Kubernetes-version lists.

All tunables are flags with env fallbacks (`--proxmox-*`, `--web-*`); secrets come from env
(injected from the `proxmox-credentials` / `unifi-credentials` Secrets in the deployment).
Leader election is off (single pinned replica).
