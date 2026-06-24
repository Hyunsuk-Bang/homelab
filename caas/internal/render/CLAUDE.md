# internal/render

Renders the Cluster API bundle for a `TenantCluster` as **unstructured** objects (so we don't
vendor CAPI/capmox/Kamaji Go types). The controller server-side-applies what these return.

Objects: credentials Secret, KamajiControlPlane, Cluster, ProxmoxCluster, and per worker pool
a KubeadmConfigTemplate / ProxmoxMachineTemplate / MachineDeployment. All placed in the
per-cluster namespace via `ClusterNamespace(tc)` (= cluster name) — the single source of
truth the controller and web UI also use.

**Three invariants validated against the live homelab — do not regress** (see
`_docs/TROUBLESHOOTING-initial-cluster.md`):
1. Cluster `controlPlaneRef` API version = `controlplane.cluster.x-k8s.io/v1alpha1` (v1alpha2
   is not served).
2. Worker NIC carries `vlan: <allocated>` — node-local templates + untagged NIC = no L2 path.
3. kubelet `feature-gates: KubeletCrashLoopBackOffMax=true,KubeletEnsureSecretPulledImages=true`
   (k8s 1.34 kubelet rejects its own config otherwise).

Also: `ProxmoxCluster` deliberately sets **no `allowedNodes`** — with node-local (local-lvm)
templates that makes capmox clone each VM onto its `sourceNode`, avoiding the cross-node-clone
failure that stranded VMs.
