/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License").
*/

// Package allocator turns a VLANPool plus the set of already-used VLANs into a
// concrete network Allocation for a TenantCluster. The node subnet is a pure
// function of the VLAN (base.<vlan>.0.0/prefix), so the only thing that must be
// unique across clusters is the VLAN id itself.
package allocator

import (
	"fmt"
	"net/netip"
	"sort"

	caasv1alpha1 "github.com/hbang/caas/api/v1alpha1"
)

// ErrExhausted is returned when no free VLAN remains in the pool range.
var ErrExhausted = fmt.Errorf("VLAN pool exhausted: no free VLAN in range")

// Allocate picks the lowest free VLAN in the pool's range (skipping used and any
// whose node subnet would collide with the pod/service CIDR) and derives the full
// network allocation from it. used is the set of VLANs already claimed by other
// TenantClusters.
func Allocate(pool *caasv1alpha1.VLANPool, used map[int]bool) (caasv1alpha1.Allocation, error) {
	vlan := firstFreeVLAN(pool, used)
	if vlan == 0 {
		return caasv1alpha1.Allocation{}, ErrExhausted
	}
	return Derive(pool, vlan), nil
}

// Derive computes the network allocation for a known VLAN. Exported so the
// controller can recompute (and validate) an already-stored allocation.
func Derive(pool *caasv1alpha1.VLANPool, vlan int) caasv1alpha1.Allocation {
	nn := pool.Spec.NodeNetwork
	return caasv1alpha1.Allocation{
		VLAN:       vlan,
		NodeSubnet: fmt.Sprintf("%d.%d.0.0/%d", nn.Base, vlan, nn.Prefix),
		Gateway:    fmt.Sprintf("%d.%d.0.%d", nn.Base, vlan, nn.GatewayHostIndex),
		HostRange: fmt.Sprintf("%d.%d.0.%d-%d.%d.0.%d",
			nn.Base, vlan, nn.HostRange.Start, nn.Base, vlan, nn.HostRange.End),
		PodCIDR:     pool.Spec.Pods,
		ServiceCIDR: pool.Spec.Services,
	}
}

func firstFreeVLAN(pool *caasv1alpha1.VLANPool, used map[int]bool) int {
	for v := pool.Spec.VLANRange.Start; v <= pool.Spec.VLANRange.End; v++ {
		if used[v] {
			continue
		}
		if NodeSubnetCollides(pool, v) {
			continue // node subnet would overlap the pod/service CIDR — unusable
		}
		return v
	}
	return 0
}

// NodeSubnetCollides reports whether the node subnet derived for vlan overlaps the
// pool's pod or service CIDR. Reusing the pod/service CIDRs across clusters is safe
// (tenant Cilium runs VXLAN overlay + masquerade, so pod/service IPs never appear on
// the VLAN), but because the node subnet shares the same base octet (10.<vlan>.0.0/24)
// a VLAN equal to the pod/service second octet (e.g. 244 vs 10.244.0.0/16, 96 vs
// 10.96.0.0/16) would overlap its own cluster's pod/service space and break routing
// inside that cluster. Such VLANs are skipped during allocation.
func NodeSubnetCollides(pool *caasv1alpha1.VLANPool, vlan int) bool {
	nn := pool.Spec.NodeNetwork
	node, err := netip.ParsePrefix(fmt.Sprintf("%d.%d.0.0/%d", nn.Base, vlan, nn.Prefix))
	if err != nil {
		return false // malformed node spec; leave it to other validation
	}
	for _, cidr := range []string{pool.Spec.Pods, pool.Spec.Services} {
		if cidr == "" {
			continue
		}
		other, err := netip.ParsePrefix(cidr)
		if err != nil {
			continue
		}
		if node.Overlaps(other) {
			return true
		}
	}
	return false
}

// UsedVLANs builds the used-set from a list of TenantClusters, optionally
// excluding one (the cluster currently being reconciled, so it doesn't conflict
// with its own prior allocation).
func UsedVLANs(clusters []caasv1alpha1.TenantCluster, excludeUID string) map[int]bool {
	used := map[int]bool{}
	for i := range clusters {
		c := &clusters[i]
		if string(c.UID) == excludeUID {
			continue
		}
		if v := c.Status.Allocation.VLAN; v != 0 {
			used[v] = true
		}
	}
	return used
}

// SortedVLANs returns the used VLANs as a sorted slice (for VLANPool.status mirror).
func SortedVLANs(used map[int]bool) []int {
	out := make([]int, 0, len(used))
	for v := range used {
		out = append(out, v)
	}
	sort.Ints(out)
	return out
}
