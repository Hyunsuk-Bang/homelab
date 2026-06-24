package allocator

import (
	"testing"

	caasv1alpha1 "github.com/hbang/caas/api/v1alpha1"
)

func testPool(start, end int) *caasv1alpha1.VLANPool {
	p := &caasv1alpha1.VLANPool{}
	p.Spec.VLANRange.Start = start
	p.Spec.VLANRange.End = end
	p.Spec.NodeNetwork.Base = 10
	p.Spec.NodeNetwork.Prefix = 24
	p.Spec.NodeNetwork.GatewayHostIndex = 1
	p.Spec.NodeNetwork.HostRange.Start = 10
	p.Spec.NodeNetwork.HostRange.End = 250
	p.Spec.Pods = "10.244.0.0/16"
	p.Spec.Services = "10.96.0.0/16"
	return p
}

func TestNodeSubnetCollides(t *testing.T) {
	pool := testPool(1, 4094)
	cases := map[int]bool{
		30:  false, // clean
		95:  false,
		96:  true, // 10.96.0.0/24 ⊂ services 10.96.0.0/16
		97:  false,
		243: false,
		244: true, // 10.244.0.0/24 ⊂ pods 10.244.0.0/16
		245: false,
	}
	for vlan, want := range cases {
		if got := NodeSubnetCollides(pool, vlan); got != want {
			t.Errorf("NodeSubnetCollides(vlan=%d) = %v, want %v", vlan, got, want)
		}
	}
}

func TestAllocateSkipsCollidingVLAN(t *testing.T) {
	// Range that starts on a colliding VLAN: allocation must skip 96 → 97.
	pool := testPool(96, 100)
	alloc, err := Allocate(pool, map[int]bool{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if alloc.VLAN != 97 {
		t.Errorf("expected VLAN 97 (96 skipped as colliding), got %d", alloc.VLAN)
	}
	if alloc.NodeSubnet != "10.97.0.0/24" {
		t.Errorf("unexpected node subnet %q", alloc.NodeSubnet)
	}
}

func TestAllocateExhaustedWhenAllCollideOrUsed(t *testing.T) {
	// Single-VLAN range pinned on a colliding VLAN → nothing allocatable.
	pool := testPool(244, 244)
	if _, err := Allocate(pool, map[int]bool{}); err != ErrExhausted {
		t.Errorf("expected ErrExhausted, got %v", err)
	}
}
