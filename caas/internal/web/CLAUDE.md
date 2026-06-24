# internal/web

The embedded launch/list/delete web UI. Runs in the operator process as a controller-runtime
`Runnable` (`NeedLeaderElection=false`), backed entirely by the CRDs (no separate datastore).
Uses a direct (uncached) client.

`web.go` — routes (Go 1.22 `ServeMux` patterns): `GET /` (list + launch form),
`POST /clusters` (create), `GET /clusters/{ns}/{name}` (detail),
`POST .../scale` (change worker count), `POST .../delete`,
`GET .../kubeconfig` (reads the Secret from the cluster's `status.namespace`).
Unmatched paths and errors render the styled `error.html` (with a "back to clusters"
button); k8s NotFound → 404. `templates/*.html` are server-rendered via `html/template`
(`go:embed`).

**Provenance gating:** the UI stamps `caas.hbang.io/managed-by=ui` (`LabelManagedBy`) on
clusters it creates and only allows **scale/delete on those**. Clusters without it
(GitOps-/kubectl-applied) are read-only here — shown with a `GitOps` owner badge, action
buttons hidden, and `handleScale`/`handleDelete` reject with **403** server-side (not just
hidden buttons). Kubeconfig download stays available for all.

Worker placement is hidden from the UI: the form/scale take a single **count**, and
`spreadWorkers` round-robins it across the configured nodes, always emitting **one pool
per node (replicas may be 0)** so scale-down sets a MachineDeployment to 0 rather than
dropping it from the SSA bundle (which the controller wouldn't prune). `scale` preserves
the existing machine size (`workerSizeOf`).

Form helpers that are **UI-only** (not in the CRD): machine **sizes**
(small/medium/large/xlarge) expand to concrete cores/mem/disk before the TenantCluster is
created; the **Kubernetes version** dropdown is validated server-side against the configured
list. CNI is fixed to cilium (not selectable).

Configured from `cmd/main.go` flags: `--web-bind-address`, `--web-namespace`,
`--web-node-templates` (`node=templateID,...`), `--web-kubernetes-versions`. Served via the
`caas-web` NodePort (30090).
