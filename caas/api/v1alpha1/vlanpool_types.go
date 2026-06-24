/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VLANRange is an inclusive range of allocatable VLAN IDs.
type VLANRange struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4094
	Start int `json:"start"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4094
	End int `json:"end"`
}

// HostRange is an inclusive range of host octets for worker VM IPs within a
// cluster's node subnet (e.g. 10..250 => 10.<vlan>.0.10 .. 10.<vlan>.0.250).
type HostRange struct {
	// +kubebuilder:validation:Minimum=2
	// +kubebuilder:validation:Maximum=254
	Start int `json:"start"`
	// +kubebuilder:validation:Minimum=2
	// +kubebuilder:validation:Maximum=254
	End int `json:"end"`
}

// NodeNetwork describes how a cluster's node subnet is derived from its VLAN.
// The node subnet is a pure function of the VLAN: <base>.<vlan>.0.0/<prefix>.
type NodeNetwork struct {
	// Base is the first octet of every node subnet (e.g. 10 => 10.<vlan>.0.0/16).
	// +kubebuilder:default=10
	Base int `json:"base"`
	// Prefix is the CIDR prefix length of each cluster's node subnet.
	// +kubebuilder:default=16
	Prefix int `json:"prefix"`
	// GatewayHostIndex is the host octet of the gateway (an SVI on the external
	// switch), e.g. 1 => 10.<vlan>.0.1.
	// +kubebuilder:default=1
	GatewayHostIndex int `json:"gatewayHostIndex"`
	// HostRange bounds the worker VM IPs handed to capmox within <base>.<vlan>.0.0/24.
	HostRange HostRange `json:"hostRange"`
}

// ControlPlaneNetwork describes management-side networking for tenant control
// planes (Kamaji) and Proxmox.
type ControlPlaneNetwork struct {
	// BGPPool is the Cilium LoadBalancer pool on the management network from which
	// each Kamaji TenantControlPlane VIP is drawn. Informational.
	BGPPool string `json:"bgpPool,omitempty"`
	// Bridge is the vlan-aware Proxmox bridge worker NICs attach to.
	// +kubebuilder:default=vmbr0
	Bridge string `json:"bridge"`
}

// VLANPoolSpec is admin-managed config defining the allocatable VLAN range and how
// per-cluster node networks are derived. Normally a single VLANPool named "default".
type VLANPoolSpec struct {
	VLANRange   VLANRange   `json:"vlanRange"`
	NodeNetwork NodeNetwork `json:"nodeNetwork"`
	// Pods is the pod CIDR every tenant uses. Reused across tenants: different VLANs
	// are not routed together, so identical pod CIDRs never collide.
	// +kubebuilder:default="10.244.0.0/16"
	Pods string `json:"pods"`
	// Services is the service CIDR every tenant uses (reused, same reasoning as Pods).
	// +kubebuilder:default="10.96.0.0/16"
	Services     string              `json:"services"`
	DNS          []string            `json:"dns,omitempty"`
	ControlPlane ControlPlaneNetwork `json:"controlPlane"`
}

// VLANPoolStatus mirrors allocation state for observability.
type VLANPoolStatus struct {
	// AllocatedVLANs is the set of VLAN IDs currently claimed by TenantClusters.
	// +optional
	AllocatedVLANs []int `json:"allocatedVLANs,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=vlp
// +kubebuilder:printcolumn:name="VLAN-Start",type=integer,JSONPath=`.spec.vlanRange.start`
// +kubebuilder:printcolumn:name="VLAN-End",type=integer,JSONPath=`.spec.vlanRange.end`
// +kubebuilder:printcolumn:name="Pods",type=string,JSONPath=`.spec.pods`

// VLANPool is the Schema for the vlanpools API.
type VLANPool struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec VLANPoolSpec `json:"spec"`
	// +optional
	Status VLANPoolStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// VLANPoolList contains a list of VLANPool.
type VLANPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []VLANPool `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VLANPool{}, &VLANPoolList{})
}
