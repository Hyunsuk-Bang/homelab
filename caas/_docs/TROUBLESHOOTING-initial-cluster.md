# Why the initial Proxmox cluster failed to come up (and how we fixed it)

A postmortem of bringing up the first hand-built tenant cluster — Kamaji hosted control
plane + Proxmox VM workers via Cluster API (CAPI) + capmox. Every fix here is now baked
into the operator's renderer (`internal/render`) so launched clusters work first try.

**Environment:** management k3s cluster (`k3s0/1/2`, `192.168.20.x`); CAPI providers
cluster-api v1.13, kubeadm-bootstrap, capmox v0.8.1, kamaji v0.19; Kamaji etcd datastore;
Cilium BGP LoadBalancer pool `172.17.0.0/16`; Proxmox 9.1 at `192.168.10.10`; pre-baked
image templates `ubuntu-2404-kube-v1.34.3` (VMIDs 106/107/108 on lab01/02/03).

The cluster came up only after clearing **five distinct failures**, in the order we hit
them. Each section is: symptom → root cause → fix.

---

## 1. `KamajiControlPlane` reference rejected (wrong API version)

**Symptom.** Applying the `Cluster` object failed / the control plane never reconciled;
the `controlPlaneRef` pointed at `controlplane.cluster.x-k8s.io/v1alpha2`.

**Root cause.** The installed Kamaji control-plane provider (v0.19) **only serves
`v1alpha1`** of `KamajiControlPlane`. The `v1alpha2` group/version simply isn't registered,
so the reference dangled.

```
kubectl get crd kamajicontrolplanes.controlplane.cluster.x-k8s.io \
  -o jsonpath='{range .spec.versions[?(@.served==true)]}{.name}{"\n"}{end}'
# -> v1alpha1   (only)
```

**Fix.** Reference the control plane as `controlplane.cluster.x-k8s.io/v1alpha1` in both the
`KamajiControlPlane` object and the `Cluster.spec.controlPlaneRef`.

> Lesson: always check which API versions a provider's CRD actually *serves* before copying
> manifests from docs/blogs — examples often use a newer version than your provider.

---

## 2. `ProxmoxCluster` rejected: `controlPlaneEndpoint` required

**Symptom.**
```
ProxmoxCluster "..." is invalid: spec.controlPlaneEndpoint: Invalid value: "null":
spec.controlPlaneEndpoint in body must be of type object
```

**Root cause.** Even with `externalManagedControlPlane: true` (the control plane lives in
Kamaji, not on Proxmox VMs), capmox's CRD still **requires a non-null
`controlPlaneEndpoint`**. We'd omitted it expecting the external-CP flag to make it optional.

**Fix.** Set `controlPlaneEndpoint` to the Kamaji control-plane VIP. Kamaji assigns this VIP
from the Cilium BGP LB pool (e.g. `172.17.0.3:6443`) — see #3 for the ordering subtlety.

---

## 3. Chicken-and-egg: Kamaji assigns the endpoint only *after* the Cluster exists

**Symptom.** The `KamajiControlPlane` sat without a `controlPlaneEndpoint`, so we couldn't
fill in the `ProxmoxCluster` (#2), so nothing progressed.

**Root cause.** Kamaji creates the backing `TenantControlPlane` (and thus allocates the LB
VIP) **only once the owning CAPI `Cluster` exists** and references it. Waiting for the
endpoint *before* creating the Cluster deadlocks.

**Fix (manual).** Apply the `KamajiControlPlane` **and** the `Cluster` together; the VIP
(`172.17.0.x:6443`) then appears, and we patch it into the `ProxmoxCluster`.

**Fix (operator).** The reconciler applies `KamajiControlPlane` + `Cluster` + credentials
Secret in one step, *then* waits for the endpoint, *then* applies the `ProxmoxCluster` and
workers. This is the "CP-before-endpoint ordering" rule in the controller.

---

## 4. Workers had zero network connectivity (missing VLAN tag) — the big one

**Symptom.** Worker VMs booted but `kubeadm join` failed:
```
couldn't validate the identity of the API Server: Get "https://172.17.0.3:6443/...":
dial tcp 172.17.0.3:6443: connect: no route to host
```
From inside the VM, it couldn't even reach its **own gateway**:
```
ping 172.18.0.1  -> Destination Host Unreachable   (ARP fails)
ping 8.8.8.8     -> Destination Host Unreachable
```
So this was never a control-plane routing problem — the VM had no working L2 at all.

**Root cause.** The first attempt attached worker NICs to `vmbr0` **untagged**. But `vmbr0`
is a *VLAN-aware* bridge whose **native/untagged segment is `192.168.10.0/24`**, not the
`172.18.0.0/24` we'd addressed the VMs in. The `172.18.0.0/24` network lives on **VLAN 18**.
Untagged, the VM sat on the wrong L2 segment with no gateway.

```
# Proxmox node network: vmbr0 is bridge_vlan_aware=1, address 192.168.10.10/24
# -> the 172.18.x network is only reachable tagged on VLAN 18
```

**Fix.** Tag the worker NIC with the cluster's VLAN:
```yaml
network:
  default:
    bridge: vmbr0
    model: virtio
    vlan: 18          # <-- without this, no L2 path to the gateway
```
After tagging, the VM reached its gateway (`ping 172.18.0.1` → 0.8 ms) and the control plane.

> This is *the* reason the platform allocates a VLAN per cluster and derives the node subnet
> from it (`10.<vlan>.0.0/16`). The L3 gateway + isolation live on the switch (UniFi); the
> VM is useless without the matching tag.

---

## 5. Kubelet crash-looped (k8s 1.34 config needs feature gates)

**Symptom.** With networking fixed, the join got further — it reached `kubelet-start` — then
timed out after 4 minutes: *"The kubelet is not healthy"*, `127.0.0.1:10248` refused. The
kubelet was crash-looping (restart counter in the 30s):
```
failed to validate kubelet configuration, error:
  [invalid configuration: FeatureGate KubeletCrashLoopBackOffMax not enabled,
   CrashLoopBackOff.MaxContainerRestartPeriod must not be set,
   invalid configuration: `imagePullCredentialsVerificationPolicy` must not be set
   if KubeletEnsureSecretPulledImages feature gate is not enabled]
```

**Root cause.** The Kubernetes **1.34** control plane writes a kubelet `config.yaml`
containing `crashLoopBackOff.maxContainerRestartPeriod` and
`imagePullCredentialsVerificationPolicy`. Those fields are only valid when their feature
gates are enabled — but the gates default **off**, so the kubelet rejects its own config and
never starts.

**Fix.** Enable both gates via `kubeletExtraArgs` in the `KubeadmConfigTemplate`:
```yaml
joinConfiguration:
  nodeRegistration:
    kubeletExtraArgs:
      provider-id: "proxmox://{{ ds.meta_data.instance_id }}"
      feature-gates: "KubeletCrashLoopBackOffMax=true,KubeletEnsureSecretPulledImages=true"
```

---

## 6. (Operational, not a manifest bug) lab01 wouldn't join

While lab02/lab03 joined cleanly, lab01's VM was repeatedly left **stopped at the template's
default 2 GB** instead of the requested 16 GB. capmox had cloned the VM but a reconcile race
(tied to a reused VMID) skipped the resize+start step.

Worse: manually start/stop cycling the half-built VM, then running `cloud-init clean`, left
cloud-init **disabled** on reboot — so the join script never ran again on that VM.

**Fix.** Don't rehabilitate a wedged first-boot VM. **Delete the CAPI `Machine`** and let the
MachineSet recreate it fresh (new VMID, new bootstrap seed). It joined first try.

> Lesson: never interrupt a worker's first-boot cloud-init. If a VM is wedged, recreate the
> Machine rather than hand-fixing the VM.

---

## 7. Nodes `NotReady` after joining — no CNI

**Symptom.** All workers registered but stayed `NotReady` with the
`node.kubernetes.io/not-ready` taint.

**Root cause.** Expected — a fresh cluster has **no pod network** until a CNI is installed.

**Fix (manual).** Install Cilium 1.19.1 into the tenant via Helm (ipam cluster-pool, pod CIDR
`172.19.0.0/16`, agents pointed at the Kamaji endpoint). Nodes flipped to `Ready` in ~40s.

**Fix (operator).** Phase 2 auto-installs an embedded Cilium manifest (pod CIDR
`10.244.0.0/16`) into each tenant once its control plane is reachable, so launched clusters
reach `Ready` with no manual step.

---

## Summary — the renderer invariants

The operator encodes all of the above so it can't regress:

| # | Failure | Fix encoded in renderer |
|---|---------|-------------------------|
| 1 | KamajiControlPlane ref version | `controlplane.cluster.x-k8s.io/v1alpha1` |
| 2 | ProxmoxCluster needs endpoint | always set `controlPlaneEndpoint` to the Kamaji VIP |
| 3 | endpoint-before-Cluster deadlock | apply KamajiControlPlane + Cluster together, then wait |
| 4 | no L2 connectivity | tag worker NIC `vlan: <allocated>` |
| 5 | kubelet crash-loop on 1.34 | `feature-gates: KubeletCrashLoopBackOffMax=true,KubeletEnsureSecretPulledImages=true` |
| 6 | wedged first-boot VM | recreate the Machine; never interrupt first boot (operational) |
| 7 | NotReady, no CNI | auto-install Cilium once CP reachable (Phase 2) |
