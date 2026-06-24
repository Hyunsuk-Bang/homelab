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

// Condition types set on a TenantCluster.
const (
	ConditionVLANAllocated     = "VLANAllocated"
	ConditionNetworkReady      = "NetworkReady"
	ConditionControlPlaneReady = "ControlPlaneReady"
	ConditionWorkersReady      = "WorkersReady"
	ConditionCNIReady          = "CNIReady"
	ConditionReady             = "Ready"
)

// Phases for TenantCluster.status.phase.
const (
	PhasePending      = "Pending"
	PhaseAllocating   = "Allocating"
	PhaseProvisioning = "Provisioning"
	PhaseReady        = "Ready"
	PhaseDeleting     = "Deleting"
	PhaseFailed       = "Failed"
)

// PoolRef references the VLANPool to allocate from.
type PoolRef struct {
	// +kubebuilder:default=default
	Name string `json:"name"`
}

// ControlPlaneSpec configures the Kamaji hosted control plane.
type ControlPlaneSpec struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=3
	Replicas int32 `json:"replicas"`
}

// CNISpec selects the CNI installed into the tenant cluster (Phase 2).
type CNISpec struct {
	// +kubebuilder:validation:Enum=none;cilium
	// +kubebuilder:default=cilium
	Type string `json:"type,omitempty"`
}

// WorkerPool is one MachineDeployment's worth of Proxmox worker VMs, pinned to a
// Proxmox source node and image template.
type WorkerPool struct {
	// Name is the pool name; used to name the MachineDeployment and templates.
	Name string `json:"name"`
	// SourceNode is the Proxmox node the template lives on / VMs are cloned to.
	SourceNode string `json:"sourceNode"`
	// TemplateID is the Proxmox VMID of the pre-baked image template.
	TemplateID int `json:"templateID"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=1
	Replicas int32 `json:"replicas"`
	// +kubebuilder:default=4
	Cores int `json:"cores"`
	// +kubebuilder:default=16384
	MemoryMiB int `json:"memoryMiB"`
	// +kubebuilder:default=100
	DiskGiB int `json:"diskGiB"`
}

// TenantClusterSpec is the user's intent for a cluster.
type TenantClusterSpec struct {
	// +kubebuilder:default="v1.34.3"
	KubernetesVersion string `json:"kubernetesVersion"`
	// +optional
	PoolRef PoolRef `json:"poolRef,omitempty"`
	// +optional
	ControlPlane ControlPlaneSpec `json:"controlPlane,omitempty"`
	// +optional
	CNI CNISpec `json:"cni,omitempty"`
	// +kubebuilder:validation:MinItems=1
	Workers []WorkerPool `json:"workers"`
	// +optional
	SSHAuthorizedKeys []string `json:"sshAuthorizedKeys,omitempty"`
}

// Allocation is the network claim made for this cluster. VLAN is the only quantity
// allocated; the rest is derived from it (and from the VLANPool constants).
type Allocation struct {
	// +optional
	VLAN int `json:"vlan,omitempty"`
	// +optional
	NodeSubnet string `json:"nodeSubnet,omitempty"`
	// +optional
	Gateway string `json:"gateway,omitempty"`
	// +optional
	HostRange string `json:"hostRange,omitempty"`
	// +optional
	PodCIDR string `json:"podCIDR,omitempty"`
	// +optional
	ServiceCIDR string `json:"serviceCIDR,omitempty"`
}

// ObservedWorkers summarises worker MachineDeployment rollout.
type ObservedWorkers struct {
	Desired int32 `json:"desired"`
	Ready   int32 `json:"ready"`
}

// LabelCluster / LabelControlNamespace mark an operator-created per-cluster
// namespace with the name and control namespace of the owning TenantCluster (kept
// as two labels because a label value may not contain '/'). The controller only
// adopts/deletes namespaces whose labels match, so it never touches pre-existing
// or unrelated namespaces.
const (
	LabelCluster          = "caas.hbang.io/cluster"
	LabelControlNamespace = "caas.hbang.io/control-namespace"
)

// LabelManagedBy records what created a TenantCluster so the web UI only mutates
// (scale/delete) clusters it owns. The UI stamps ManagedByUI on create; anything
// without it (GitOps-applied, kubectl-applied, …) is read-only in the UI and must
// be changed through its own source of truth.
const LabelManagedBy = "caas.hbang.io/managed-by"

// ManagedByUI is the LabelManagedBy value stamped on UI-created clusters.
const ManagedByUI = "ui"

// TenantClusterStatus is the observed state of a TenantCluster.
type TenantClusterStatus struct {
	// +optional
	Phase string `json:"phase,omitempty"`
	// Namespace is the dedicated namespace the operator created for this cluster's
	// CAPI bundle, credentials and kubeconfig.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// +optional
	Allocation Allocation `json:"allocation,omitzero"`
	// +optional
	ControlPlaneEndpoint string `json:"controlPlaneEndpoint,omitempty"`
	// +optional
	KubeconfigSecretRef string `json:"kubeconfigSecretRef,omitempty"`
	// +optional
	ObservedWorkers ObservedWorkers `json:"observedWorkers,omitzero"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=tc
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="VLAN",type=integer,JSONPath=`.status.allocation.vlan`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.controlPlaneEndpoint`
// +kubebuilder:printcolumn:name="Workers",type=string,JSONPath=`.status.observedWorkers.ready`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TenantCluster is the Schema for the tenantclusters API.
type TenantCluster struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec TenantClusterSpec `json:"spec"`
	// +optional
	Status TenantClusterStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// TenantClusterList contains a list of TenantCluster.
type TenantClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []TenantCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TenantCluster{}, &TenantClusterList{})
}
