# internal/network

Vendor-agnostic provider that realizes a tenant's VLAN network (and, later, isolation) on the
site's network controller. The controller speaks only domain terms; each provider translates.

- `provider.go` — the `Provider` interface (`EnsureNetwork`/`DeleteNetwork`/`Name`) + `Spec`,
  `Ref`, `Isolation`. Declarative + idempotent; **default-deny lateral, explicit-allow** model
  (the operator never enumerates peer tenants); resources are ownership-tagged.
- `noop.go` — default when no controller is configured: logs intent, assumes out-of-band
  provisioning.
- `unifi.go` — UniFi impl via the legacy REST API (`/proxy/network/api/s/<site>/rest/networkconf`)
  with `X-API-KEY`. Creates/updates/deletes the VLAN network named `caas-vlan-<vlan>`; adopts
  any `caas`-prefixed network on a managed VLAN, never touches others; DHCP off.

Provider is selected in `cmd/main.go` from env (`UNIFI_URL`/`UNIFI_API_KEY`/`UNIFI_SITE` →
unifi, else noop), creds from the `unifi-credentials` Secret.

**Not yet implemented: firewall/isolation.** The `Isolation` field is plumbed through but the
UniFi provider only logs it — so VLANs give L2 separation while inter-VLAN routing still allows
tenant↔tenant. This is the designed next increment. See `_docs/network-provider.md`.
