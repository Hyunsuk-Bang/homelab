# Kamaji-CaaS — Design Document

Self-service, multi-tenant Kubernetes-cluster platform.
A k3s **management cluster** runs Kamaji (hosted control planes) + Cluster API; each
tenant cluster's **worker nodes** are VMs provisioned on Proxmox. A small Go operator +
web UI lets a user "launch a cluster" with one click. Tenant clusters are
network-isolated from each other by a unique VLAN per cluster (isolation enforced on the
external switch/router; the platform guarantees deterministic, collision-free
VLAN/subnet allocation).

Status: **design phase**. Nothing in this doc is built yet. The manual baseline it
generalizes is proven working (see Appendix A).

---

## 1. Goals & non-goals

### Goals
- One CRD describes a tenant cluster; applying it (via UI or `kubectl`) provisions a full
  working cluster: Kamaji control plane + Proxmox workers + CNI.
- Deterministic per-cluster VLAN + IP allocation, tracked as cluster state (CRDs/etcd).
- Tenant clusters cannot talk to each other (VLAN isolation; ACLs on external L3 device).
- Each worker can reach **only its own** control-plane endpoint.
- Clean teardown: deleting the CRD removes all CAPI objects, VMs, and **releases the VLAN**.

### Non-goals (v1)
- Enforcing the inter-tenant ACLs in software — that's the external switch's job. The
  operator only *emits the intended rules* (Phase 4).
- Multi-management-cluster / HA federation.
- Storage/CSI, ingress, or app-layer concerns inside tenant clusters.

---

## 2. Architecture

```
                        Management k3s cluster
  ┌──────────────────────────────────────────────────────────────────┐
  │  caas (single Go binary)                                    │
  │   ├── controller-manager (controller-runtime)                      │
  │   │     • TenantCluster reconciler                                 │
  │   │     • VLAN/IPAM allocator (single writer, CAS)                 │
  │   └── http server  → embedded web UI (launch / list / delete)     │
  │                                                                    │
  │  CAPI providers (already installed):                               │
  │   cluster-api v1.13 · kubeadm-bootstrap · capmox v0.8 ·            │
  │   kamaji controlplane v0.19 · ipam-in-cluster v1.0                 │
  └───────────────┬────────────────────────────────────────────────────┘
                  │ renders + applies CAPI bundle, sets ownerRefs
                  ▼
   Cluster · KamajiControlPlane · ProxmoxCluster(+Secret) ·
   per-pool MachineDeployment / ProxmoxMachineTemplate / KubeadmConfigTemplate
                  │ capmox provisions
                  ▼
        Proxmox: worker VMs, NIC tagged VLAN=<allocated>, IPs from <allocated /24>
```

### Why these choices
- **Operator + CRDs (no external DB):** state lives in etcd via CRDs. The operator is the
  *single writer* for allocation, so transactional weakness is handled with optimistic
  concurrency (resourceVersion CAS + `RetryOnConflict`) and a finalizer for release.
- **Go + controller-runtime:** best CAPI/k8s ecosystem fit; ships as one static binary
  that also serves the UI.
- **External-ACL isolation:** the platform's contract is *deterministic allocation*; the
  switch/router enforces deny-between-VLANs. The operator surfaces the exact rules needed.

---

## 3. CRDs

Group: `caas.hbang.io`  ·  Version: `v1alpha1`.
`VLANPool` is **cluster-scoped** (global allocation view); `TenantCluster` is
**namespaced** — Kubernetes won't garbage-collect namespaced CAPI children owned by a
cluster-scoped resource, so namespacing TenantCluster lets ownerRef cascade-delete work.
The allocator lists TenantClusters across all namespaces.

### 3.1 `VLANPool` (singleton config, admin-managed)

Defines the allocatable resources. The operator reads it; never auto-creates it.

```yaml
apiVersion: caas.hbang.io/v1alpha1
kind: VLANPool
metadata:
  name: default
spec:
  vlanRange: { start: 10, end: 250 }       # allocatable VLAN IDs (VLAN 18 = existing manual cluster)
  nodeNetwork:
    base: 10                               # node subnet is deterministically 10.<vlan>.0.0/16
    prefix: 16                             #   e.g. VLAN 18 -> 10.18.0.0/16
    gatewayHostIndex: 1                    # 10.<vlan>.0.1 = gateway (an SVI on the external switch)
    hostRange: { start: 10, end: 250 }     # VM IPs: 10.<vlan>.0.10 .. 10.<vlan>.0.250
  pods:     "10.244.0.0/16"                # tenant-internal, REUSED across all tenants
  services: "10.96.0.0/16"                 # tenant-internal, REUSED across all tenants
  dns: ["8.8.8.8", "1.1.1.1"]
  controlPlane:
    bgpPool: "172.17.0.0/16"               # Kamaji LB VIP pool on the mgmt network
    bridge: "vmbr0"                         # vlan-aware bridge on Proxmox nodes
status:
  allocatedVLANs: [18]                      # mirror for quick observability
  conditions: [...]

# Addressing model: the node subnet is DERIVED from the VLAN (10.<vlan>.0.0/16), so VLAN is
# the only quantity allocated. pods/services are fixed constants — safe to reuse because
# different VLANs are not routed to each other, so identical pod CIDRs never collide.
```

### 3.2 `TenantCluster` (the intent object the UI writes)

```yaml
apiVersion: caas.hbang.io/v1alpha1
kind: TenantCluster
metadata:
  name: team-blue
spec:
  kubernetesVersion: "v1.34.3"
  poolRef: { name: default }               # which VLANPool to allocate from
  controlPlane:
    replicas: 3                            # Kamaji TenantControlPlane replicas
  cni:
    type: cilium                           # auto-installed in Phase 2 (none|cilium)
  workers:
    - name: lab01
      sourceNode: lab01                    # Proxmox node
      templateID: 106                      # pre-baked ubuntu-2404-kube image
      replicas: 1
      cores: 4
      memoryMiB: 16384
      diskGiB: 100
    - name: lab02
      sourceNode: lab02
      templateID: 107
      replicas: 1
      cores: 4
      memoryMiB: 16384
      diskGiB: 100
  sshAuthorizedKeys:
    - "ssh-ed25519 AAAA... ansible_key"
status:
  phase: Provisioning                      # Pending|Allocating|Provisioning|Ready|Deleting|Failed
  allocation:
    vlan: 18
    nodeSubnet: "10.18.0.0/16"             # derived: 10.<vlan>.0.0/16
    gateway: "10.18.0.1"                   # 10.<vlan>.0.1 (switch SVI)
    hostRange: "10.18.0.10-10.18.0.250"    # capmox ipv4Config addresses
    podCIDR: "10.244.0.0/16"               # tenant-internal, reused across tenants
    serviceCIDR: "10.96.0.0/16"            # tenant-internal, reused across tenants
  controlPlaneEndpoint: "172.17.0.5:6443"  # assigned by Kamaji LB, copied here
  kubeconfigSecretRef: { name: team-blue-kubeconfig }
  observedWorkers: { desired: 2, ready: 0 }
  isolation:                               # rendered ACL intent (Phase 4 enforces)
    allowToControlPlane: "172.17.0.5/32:6443"
    denyToVLANs: [101, 102]
  conditions:
    - type: VLANAllocated      status: "True"
    - type: ControlPlaneReady  status: "False"
    - type: WorkersReady       status: "False"
    - type: Ready              status: "False"
```

**Key allocation invariants**
- `vlan` is unique across all `TenantCluster`s (the allocator guarantees this).
- `nodeSubnet` is a unique `/24` from `VLANPool.spec.subnet.supernet`.
- `podCIDR`/`serviceCIDR` are tenant-internal and **not** routed between clusters, so they
  may repeat across tenants — only `nodeSubnet`+`vlan` must be globally unique.

---

## 4. Reconcile logic (TenantCluster)

Pseudocode — idempotent, level-triggered.

```
reconcile(tc):
  if tc.DeletionTimestamp != nil:
      return handleDelete(tc)            # see §4.2

  ensureFinalizer(tc, "caas.hbang.io/finalizer")
  pool = get(VLANPool, tc.spec.poolRef)

  # 1. Allocate (only if not already allocated) — single-writer + CAS
  if tc.status.allocation.vlan == 0:
      alloc = allocate(pool, tc)         # see §4.1
      tc.status.allocation = alloc
      setCondition(tc, VLANAllocated, True)
      updateStatus(tc)                   # CAS; on conflict, requeue

  # 2. Render + apply the CAPI bundle (server-side apply, ownerRef=tc)
  objs = render(tc, pool)               # Secret, ProxmoxCluster, KamajiControlPlane,
                                        # Cluster, and per-pool MD/PMT/KCT
  for o in objs: serverSideApply(o, owner=tc)

  # 3. Mirror status from children
  kcp = get(KamajiControlPlane, tc.name)
  if kcp.status.ready:
      tc.status.controlPlaneEndpoint = kcp.spec.controlPlaneEndpoint
      setCondition(tc, ControlPlaneReady, True)

  # 4. CNI (Phase 2): once CP ready + ≥1 node registered, install cilium into tenant
  if cpReady and nodesRegistered and tc.spec.cni.type == cilium:
      ensureCNI(tenantKubeconfig(tc))

  # 5. Workers ready?
  tc.status.observedWorkers = sumMachineDeployments(tc)
  setCondition(tc, WorkersReady, observed.ready == observed.desired)

  setCondition(tc, Ready, cpReady && workersReady && cniReady)
  tc.status.phase = derivePhase(conditions)
  updateStatus(tc)                       # CAS
  return requeueAfter(30s)
```

### 4.1 VLAN/IPAM allocation (CRD-safe)

The operator is the only writer, so a simple scan-and-claim with optimistic concurrency is
safe:

```
allocate(pool, tc):
  usedVLANs = { t.status.allocation.vlan
                for t in list(TenantCluster) if t.status.allocation.vlan != 0 }
  vlan = firstFree(pool.spec.vlanRange, usedVLANs)        # VLAN is the ONLY thing allocated
  if vlan == nil: fail("VLAN pool exhausted")
  subnet  = f"{pool.nodeNetwork.base}.{vlan}.0.0/{pool.nodeNetwork.prefix}"  # 10.<vlan>.0.0/16
  gateway = f"{pool.nodeNetwork.base}.{vlan}.0.{pool.nodeNetwork.gatewayHostIndex}"
  hostRange = f"{base}.{vlan}.0.{hostRange.start}-{base}.{vlan}.0.{hostRange.end}"
  return Allocation{ vlan, subnet, gateway, hostRange,
                     podCIDR: pool.pods, serviceCIDR: pool.services }   # pods/services are constants
```
Because the node subnet is a pure function of the VLAN, collision-freedom reduces to
"never hand out the same VLAN twice" — handled by the single-writer + CAS guarantee.
- **Race safety:** the claim is written to `tc.status` via `UpdateStatus` guarded by
  `resourceVersion`. Two concurrent reconciles of *different* TenantClusters that pick the
  same VLAN → the second `UpdateStatus` sees a changed list on requeue and re-picks. Belt
  + suspenders: a `MaxConcurrentReconciles: 1` on the allocation path removes the race
  entirely for v1 (revisit if throughput matters).
- **Release:** on delete, the finalizer removes the allocation; because allocation is
  derived from the live list of TenantClusters, deletion frees it automatically.

### 4.2 Delete

```
handleDelete(tc):
  # ownerRefs cascade-delete the CAPI bundle; wait for capmox to remove VMs
  if childrenStillExist(tc): return requeueAfter(10s)
  removeFinalizer(tc)        # frees the VLAN/subnet (derived from list)
```

---

## 5. Rendered CAPI bundle (per TenantCluster)

Generalization of the proven manual manifests (Appendix A). Substitutions in **bold**.

- **Secret** `<name>-proxmox-credentials` — Proxmox API token (from operator config, not
  per-tenant).
- **ProxmoxCluster** `<name>` — `externalManagedControlPlane: true`,
  `controlPlaneEndpoint` = Kamaji VIP, `ipv4Config.addresses` = **allocated host range**
  (`10.<vlan>.0.10-…0.250`), `prefix: 16`, `gateway` = **`10.<vlan>.0.1`**,
  `allowedNodes` = union of worker `sourceNode`s.
- **KamajiControlPlane** `<name>` (`controlplane.cluster.x-k8s.io/v1alpha1`) —
  `replicas` = **spec.controlPlane.replicas**, `network.serviceType: LoadBalancer`.
- **Cluster** `<name>` — `controlPlaneRef` → KamajiControlPlane **(v1alpha1!)**,
  `infrastructureRef` → ProxmoxCluster, `clusterNetwork` = **pod/service CIDRs**.
- **Per worker pool:**
  - `ProxmoxMachineTemplate` — `network.default.{bridge, model, vlan:` **allocated VLAN** `}`,
    `memoryMiB`, `numCores`, disk, `sourceNode`, `templateID`.
  - `KubeadmConfigTemplate` — `joinConfiguration.nodeRegistration.kubeletExtraArgs`:
    `provider-id` **and the required feature gates**
    `KubeletCrashLoopBackOffMax=true,KubeletEnsureSecretPulledImages=true` (see Appendix B).
  - `MachineDeployment` — `replicas`, refs to the two templates above.

---

## 6. Web UI (Phase 3)
Served by the same binary. Pages:
- **Launch** — form (name, k8s version, CP replicas, worker pools, CNI) → creates a
  `TenantCluster`.
- **List** — all TenantClusters with phase, VLAN, endpoint, node ready count.
- **Detail** — conditions, events, **download kubeconfig**, the rendered isolation ACLs.
- **Delete** — confirmation → deletes the CRD (cascade).

Backend talks to the k8s API (the CRDs are the source of truth); no separate datastore.

---

## 7. Phased delivery
| Phase | Scope | Done when |
|---|---|---|
| 1 | CRDs + allocator + reconciler (render & apply bundle) | `kubectl apply` a TenantCluster → CP up, workers join |
| 2 | Status/conditions, **auto-CNI**, delete + VLAN release, kubeconfig endpoint | nodes reach Ready; delete frees VLAN |
| 3 | Embedded web UI | launch/list/delete from browser |
| 4 | Isolation automation: render switch ACLs + validation | per-cluster ACL artifact produced |

---

## Appendix A — proven manual baseline (on k3s0:/root/capmox)
- `base.yaml`: Secret + KamajiControlPlane(v1alpha1) + ProxmoxCluster(+controlPlaneEndpoint)
  + Cluster(v1beta1). Brought up a 3-replica Kamaji TenantControlPlane, Ready.
- `workers.yaml`: 3 pools (lab01/02/03), `network.default.vlan: 18`, kubelet feature gates.
  lab02/lab03 joined; lab01 needed a VM start + memory fix (capmox reconcile quirk).

## Appendix B — bugs found while validating the baseline (must stay fixed in the renderer)
1. **KamajiControlPlane ref version:** Cluster `controlPlaneRef` must be
   `controlplane.cluster.x-k8s.io/**v1alpha1**` (v1alpha2 is not served).
2. **VLAN tag required:** `vmbr0` is vlan-aware; its untagged segment is `192.168.10.0/24`.
   Worker NICs must carry `vlan: <id>` or they have zero L2 connectivity to their gateway.
3. **Kubelet feature gates:** k8s 1.34 kubelet config carries
   `crashLoopBackOff.maxContainerRestartPeriod` + `imagePullCredentialsVerificationPolicy`,
   which require `KubeletCrashLoopBackOffMax` + `KubeletEnsureSecretPulledImages` enabled,
   else kubelet crash-loops. Set them in `kubeletExtraArgs`.
4. **capmox VMID-reuse race (operational):** on rettries, a reused VMID can be left cloned
   but un-started / un-resized. Renderer can't fix this; mitigate by not reusing VMIDs and
   letting the machine recreate fresh.
