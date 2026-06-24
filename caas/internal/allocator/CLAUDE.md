# internal/allocator

VLAN allocation. The **VLAN is the only thing allocated** — everything else (node subnet,
gateway, host range) is a pure function of it: `<base>.<vlan>.0.0/<prefix>` (e.g. VLAN 30 →
`10.30.0.0/24`, gateway `10.30.0.1`). Pod/service CIDRs are constants reused across tenants
(safe because tenant VLANs aren't routed together).

`Allocate(pool, used)` picks the lowest free VLAN in the pool range and calls `Derive`.
`used` comes from the live `TenantCluster` list (`UsedVLANs`). Collision-freedom therefore
reduces to "never hand out the same VLAN twice" — guaranteed by the controller's single
writer + `MaxConcurrentReconciles=1`. Pure functions, no client — trivially unit-testable.

`Allocate` also **skips VLANs whose node subnet would overlap the pod/service CIDR**
(`NodeSubnetCollides`): since the node subnet is `10.<vlan>.0.0/24` and pods/services live
in the same `10.0.0.0/8`, VLAN 244 (vs `10.244.0.0/16`) and VLAN 96 (vs `10.96.0.0/16`)
would collide with their own cluster's pod/service space. Overlap is computed generically
(`netip.Prefix.Overlaps`), so it tracks whatever CIDRs the pool actually sets.
