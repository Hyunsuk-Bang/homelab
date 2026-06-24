# DEPRECATED — superseded by the caas operator

This chart hand-templated the Cluster API bundle (Cluster / ProxmoxCluster / KamajiControlPlane
/ MachineDeployments) for a tenant cluster from static values. That job is now done by the
**caas operator** — the `ARCHITECTURE.md` TODO ("CIDR allocation operator … from a single
`TenantCluster` CRD") — deployed via ArgoCD (`helm/apps` → `caas/charts/caas`).

To create a cluster now, apply a `TenantCluster` CR (or use the caas web UI) instead of
`helm install`-ing this chart. The operator allocates the VLAN, creates the per-cluster
namespace + UniFi network, renders the same CAPI bundle (with the validated fixes baked in),
and installs the CNI.

Kept for reference; safe to delete once you're comfortable with the operator.
