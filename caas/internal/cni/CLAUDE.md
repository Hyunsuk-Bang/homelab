# internal/cni

Installs a CNI into a freshly-provisioned tenant so its nodes reach Ready (they stay NotReady
without a pod network).

`cilium.yaml` is the Cilium 1.19.1 manifest rendered once from the chart
(`helm template`, ipam cluster-pool, pod CIDR `10.244.0.0/16`) and embedded via `go:embed`.
`InstallCilium` builds a client from the tenant's `<name>-kubeconfig` Secret and server-side
applies it (idempotent). The controller calls this once the tenant control plane is reachable,
gated on the `CNIReady` condition.

To change CNI version/config: re-render `cilium.yaml` with `helm template` and rebuild.
