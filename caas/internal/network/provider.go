/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License").
*/

// Package network defines the vendor-agnostic interface the operator uses to
// realize a tenant cluster's L2/L3 network and isolation policy on whatever
// network controller a site runs (UniFi, and later others). The operator speaks
// only in domain terms here — VLAN, subnet, gateway, allowed destinations — and
// each Provider translates those into vendor primitives (UniFi networks/firewall
// rules, switch ACLs, etc.).
//
// Design principles:
//   - Declarative + idempotent: EnsureNetwork is called every reconcile and must
//     converge to Spec, creating or updating as needed. DeleteNetwork is safe to
//     call repeatedly.
//   - Default-deny lateral, explicit-allow: the operator does NOT enumerate peer
//     tenants. It states what a tenant MAY reach (its control plane, the internet);
//     the provider denies all other private traffic by default. Adding a new tenant
//     therefore never requires re-reconciling existing ones.
//   - Ownership-tagged: a provider must tag every resource it creates with the
//     cluster identity (see Ref) so it can find them on teardown and never touch a
//     network/rule it did not create.
package network

import "context"

// Ref identifies a tenant network for lookup and teardown. ClusterID is stable for
// the lifetime of the cluster (the operator uses "<control-namespace>/<name>") and
// is what providers tag their resources with.
type Ref struct {
	ClusterID string
	VLAN      int
}

// Protocol for an isolation target.
type Protocol string

const (
	ProtocolTCP Protocol = "tcp"
	ProtocolUDP Protocol = "udp"
	ProtocolAny Protocol = "any"
)

// Target is an internal destination a tenant is explicitly allowed to reach.
// Empty Ports means all ports.
type Target struct {
	CIDR     string
	Protocol Protocol
	Ports    []int
}

// Isolation expresses what a tenant network may reach. Everything in private
// (RFC1918) space that is not in AllowInternal is denied by the provider — this is
// what keeps tenants isolated from each other and from the home/management LANs.
type Isolation struct {
	// AllowInternal are private destinations the tenant may reach, e.g. its Kamaji
	// control-plane VIP pool on tcp/6443.
	AllowInternal []Target
	// AllowInternet permits egress to public (non-RFC1918) space for image pulls,
	// DNS, etc.
	AllowInternet bool
}

// Spec is the desired state of a tenant network. It is derived entirely from the
// VLAN allocation and the VLANPool, so the operator can build it without any
// vendor knowledge.
type Spec struct {
	Ref
	// Name is a human-friendly label (the cluster name) for the created resources.
	Name string
	// Subnet is the node network CIDR, e.g. 10.30.0.0/16.
	Subnet string
	// Gateway is the L3 gateway / SVI address the provider configures, e.g.
	// 10.30.0.1.
	Gateway string
	// ManagedRange is the static IP range capmox assigns to worker VMs
	// (e.g. 10.30.0.10-10.30.0.250). Providers must NOT run DHCP over this range;
	// the simplest compliant behaviour is to disable DHCP on the network entirely.
	ManagedRange string
	// Isolation is the firewall intent for this network.
	Isolation Isolation
}

// Status is the observed state of a tenant network after EnsureNetwork.
type Status struct {
	// Ready is true once the network and all isolation rules are in place.
	Ready bool
	// Message is a human-readable detail for surfacing in a condition.
	Message string
	// ResourceRefs are provider-assigned IDs (network id, rule ids, ...) for
	// observability and debugging. Opaque to the operator.
	ResourceRefs map[string]string
}

// Provider realizes tenant networks on a specific network controller.
//
// Implementations are constructed with their own vendor config (controller URL,
// credentials, site) outside this interface; the operator only sees these methods.
type Provider interface {
	// EnsureNetwork idempotently converges the tenant network + isolation to spec.
	EnsureNetwork(ctx context.Context, spec Spec) (Status, error)
	// DeleteNetwork removes the tenant network and its rules. Idempotent: a missing
	// network is success.
	DeleteNetwork(ctx context.Context, ref Ref) error
	// Name identifies the implementation for logs, conditions and status.
	Name() string
}
