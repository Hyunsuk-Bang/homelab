# Network Provider — vendor-agnostic VLAN/ACL automation (Phase 4 design)

Today the operator **allocates** a VLAN and **derives** the node subnet
(`10.<vlan>.0.0/16`), but the actual network — the VLAN, its gateway/SVI, DHCP-off,
and the isolation firewall rules — is provisioned **by hand** on UniFi. Phase 4
automates that, behind an interface so the controller never speaks UniFi (or any
vendor) directly.

Status: **interface designed** (`internal/network`), no vendor implementation yet.

---

## 1. The interface

`internal/network/provider.go`:

```go
type Provider interface {
    EnsureNetwork(ctx, Spec) (Status, error)   // idempotent converge-to-spec
    DeleteNetwork(ctx, Ref) error              // idempotent teardown
    Name() string
}
```

The operator builds a `Spec` purely from the VLAN allocation + VLANPool — no vendor
knowledge:

```go
Spec{
  Ref:          {ClusterID: "default/team-blue", VLAN: 30},
  Name:         "team-blue",
  Subnet:       "10.30.0.0/16",
  Gateway:      "10.30.0.1",
  ManagedRange: "10.30.0.10-10.30.0.250",   // capmox static range; no DHCP here
  Isolation: {
    AllowInternal: [{CIDR:"172.17.0.0/16", Protocol:"tcp", Ports:[6443]}], // CP VIP pool
    AllowInternet: true,                                                    // image pulls, DNS
  },
}
```

### Three design principles
1. **Declarative + idempotent.** `EnsureNetwork` runs every reconcile and converges;
   `DeleteNetwork` is safe to repeat. Matches the controller model.
2. **Default-deny lateral, explicit-allow.** The operator never enumerates peer
   tenants. It states only what a tenant *may* reach (its control plane + internet);
   the provider denies all *other* private (RFC1918) traffic. **Adding a new tenant
   never forces re-reconciliation of existing ones** — a property peer-enumeration
   would not have.
3. **Ownership-tagged.** A provider tags every resource it creates with `ClusterID`,
   so teardown is precise and it never deletes a network/rule it didn't create
   (same safety model as our per-cluster namespace labels).

---

## 2. How it plugs into the reconciler

`EnsureNetwork` slots in right after VLAN allocation / namespace creation, gating
the cluster as a new condition `NetworkReady`:

```
allocate VLAN ─► ensure namespace ─► provider.EnsureNetwork(spec)  [NetworkReady]
              ─► apply CAPI bundle ─► CNI ─► Ready (= CP && Workers && CNI && Network)
```

- The allow-target uses the **whole CP VIP pool** (`VLANPool.controlPlane.bgpPool`,
  e.g. `172.17.0.0/16`) on tcp/6443, so network setup does **not** need to wait for
  the specific Kamaji endpoint — it can run before the control plane is up.
- `DeleteNetwork` is called in `reconcileDelete` before releasing the finalizer, so
  the VLAN's rules are gone before the VLAN id is freed for reuse.

A `NetworkProvider` field on the reconciler selects the implementation; default is
`Noop` (logs intent, assumes out-of-band provisioning) so unconfigured/dev setups
keep working exactly as today.

---

## 3. UniFi implementation (next step, once interface is approved)

Maps the domain spec onto the UniFi controller API (site-manager / network app):

| Domain | UniFi resource |
|---|---|
| VLAN + Subnet + Gateway | a **network** (virtual network) with `vlan`, `ip_subnet`, DHCP disabled |
| `AllowInternal` target | **firewall rule**: accept `src=net` → `dst=CP-pool` proto/port |
| `AllowInternet` | **firewall rule**: accept `src=net` → internet (after the deny) |
| default-deny lateral | **firewall rule**: drop `src=net` → RFC1918 group |
| ownership | name/note prefix `caas:<ClusterID>` on every created object |

Rule ordering (accept CP-pool → drop RFC1918 → accept internet) is the provider's
responsibility; the operator only supplies intent.

Config (URL, API key/credentials, site, the RFC1918 group) is constructor input to
the UniFi provider, not part of the `Provider` interface.

### Open questions for the UniFi build
- UniFi API surface/version (integrations API key vs controller login + CSRF); which
  firewall model (legacy rules vs the newer zone-based policy engine).
- Whether the gateway SVI per VLAN is auto-created with the network or needs a
  separate step on the target hardware.

---

## 4. Decisions (confirmed 2026-06-23)
1. **Default-deny + explicit-allow** isolation model. ✅ accepted.
2. **NetworkReady as a hard gate** (cluster not Ready until the network + rules are
   realized), `Noop` default when unconfigured. ✅ accepted.
3. **Inline in the TenantCluster reconciler** (one reconcile owns the lifecycle). ✅ accepted.
4. **Auth = UniFi API key** (`X-API-KEY` header). ✅
5. **VLAN-aware switch + router**: creating the UniFi network auto-provisions the
   per-VLAN gateway SVI — the provider only creates the network object, no separate
   gateway step. ✅
