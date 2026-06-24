/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License").
*/

// Package render turns a TenantCluster + its network Allocation into the Cluster
// API object bundle (Kamaji control plane, Proxmox infra, workers). Objects are
// emitted as unstructured so we don't need to vendor the CAPI/capmox/Kamaji Go
// types. Every object carries the fixes validated against the live homelab:
//   - KamajiControlPlane referenced as controlplane.cluster.x-k8s.io/v1alpha1
//   - worker NICs tagged with the cluster VLAN
//   - kubelet feature gates required by k8s 1.34
package render

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	caasv1alpha1 "github.com/hbang/caas/api/v1alpha1"
)

// ProxmoxConfig holds the operator-level Proxmox API credentials shared by all
// tenants (rendered into a per-namespace Secret capmox reads).
type ProxmoxConfig struct {
	URL     string
	TokenID string // e.g. root@pam!CAPI
	Secret  string // the token secret value
}

const (
	gvCore      = "cluster.x-k8s.io/v1beta1"
	gvBootstrap = "bootstrap.cluster.x-k8s.io/v1beta1"
	gvInfra     = "infrastructure.cluster.x-k8s.io/v1alpha1"
	gvKamaji    = "controlplane.cluster.x-k8s.io/v1alpha1"

	credsSuffix          = "-proxmox-credentials"
	kubeletFeatureGates  = "KubeletCrashLoopBackOffMax=true,KubeletEnsureSecretPulledImages=true"
	providerIDFromMeta   = "proxmox://{{ ds.meta_data.instance_id }}"
	kubeadmConfigSuffix  = "-worker"
)

// ClusterNamespace is the dedicated namespace the operator creates for a cluster's
// CAPI bundle, credentials and kubeconfig. It is derived from the cluster name so
// the controller, renderer and web UI all agree on one value. The TenantCluster
// object itself lives in the control namespace, not here.
func ClusterNamespace(tc *caasv1alpha1.TenantCluster) string {
	return tc.Name
}

// CredentialsSecretName is the capmox credentials secret name for a cluster.
func CredentialsSecretName(tc *caasv1alpha1.TenantCluster) string {
	return tc.Name + credsSuffix
}

// ControlPlane renders the KamajiControlPlane. It does not depend on the
// allocation, so it can be applied first to obtain the control-plane VIP.
func ControlPlane(tc *caasv1alpha1.TenantCluster) *unstructured.Unstructured {
	return obj(gvKamaji, "KamajiControlPlane", tc.Name, ClusterNamespace(tc), map[string]any{
		"dataStoreName": "default",
		"addons": map[string]any{
			"coreDNS":   map[string]any{},
			"kubeProxy": map[string]any{},
		},
		"kubelet": map[string]any{
			"cgroupfs":              "systemd",
			"preferredAddressTypes": []any{"InternalIP"},
		},
		"network":  map[string]any{"serviceType": "LoadBalancer"},
		"replicas": int64(tc.Spec.ControlPlane.Replicas),
		"version":  tc.Spec.KubernetesVersion,
	})
}

// CredentialsSecret renders the capmox credentials Secret for the cluster's namespace.
func CredentialsSecret(tc *caasv1alpha1.TenantCluster, cfg ProxmoxConfig) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("v1")
	u.SetKind("Secret")
	u.SetName(CredentialsSecretName(tc))
	u.SetNamespace(ClusterNamespace(tc))
	_ = unstructured.SetNestedStringMap(u.Object, map[string]string{
		"url":    cfg.URL,
		"token":  cfg.TokenID,
		"secret": cfg.Secret,
	}, "stringData")
	return u
}

// InfraCluster renders the ProxmoxCluster. endpoint must be the Kamaji VIP
// (host:port handled by caller via host/port fields).
func InfraCluster(tc *caasv1alpha1.TenantCluster, pool *caasv1alpha1.VLANPool, alloc caasv1alpha1.Allocation, host string, port int64) *unstructured.Unstructured {
	dns := pool.Spec.DNS
	if len(dns) == 0 {
		dns = []string{"8.8.8.8", "1.1.1.1"}
	}
	// NOTE: allowedNodes is deliberately NOT set. Our image templates are on
	// node-local storage (local-lvm), so a VM can only be cloned onto the node that
	// holds its template. Per the capmox CRD: "If neither a Target nor AllowedNodes
	// was set, the VM will be cloned onto the same node as SourceNode." That pins
	// each worker pool to its template's node and avoids the illegal cross-node
	// clone ("500 can't clone VM to node X (VM uses local storage)") that otherwise
	// left VMs stranded. (target= is not an option — it requires shared storage.)
	return obj(gvInfra, "ProxmoxCluster", tc.Name, ClusterNamespace(tc), map[string]any{
		"dnsServers":                  toAnySlice(dns),
		"externalManagedControlPlane": true,
		"controlPlaneEndpoint": map[string]any{
			"host": host,
			"port": port,
		},
		"ipv4Config": map[string]any{
			"addresses": []any{alloc.HostRange},
			"gateway":   alloc.Gateway,
			"prefix":    int64(pool.Spec.NodeNetwork.Prefix),
		},
		"credentialsRef": map[string]any{
			"name":      CredentialsSecretName(tc),
			"namespace": ClusterNamespace(tc),
		},
	})
}

// Cluster renders the top-level CAPI Cluster wiring Kamaji + Proxmox together.
func Cluster(tc *caasv1alpha1.TenantCluster, alloc caasv1alpha1.Allocation) *unstructured.Unstructured {
	return obj(gvCore, "Cluster", tc.Name, ClusterNamespace(tc), map[string]any{
		"clusterNetwork": map[string]any{
			"pods":     map[string]any{"cidrBlocks": []any{alloc.PodCIDR}},
			"services": map[string]any{"cidrBlocks": []any{alloc.ServiceCIDR}},
		},
		"controlPlaneRef": map[string]any{
			"apiVersion": gvKamaji, // v1alpha1 — v1alpha2 is NOT served
			"kind":       "KamajiControlPlane",
			"name":       tc.Name,
		},
		"infrastructureRef": map[string]any{
			"apiVersion": gvInfra,
			"kind":       "ProxmoxCluster",
			"name":       tc.Name,
		},
	})
}

// Workers renders the shared KubeadmConfigTemplate plus, per pool, a
// ProxmoxMachineTemplate and a MachineDeployment.
func Workers(tc *caasv1alpha1.TenantCluster, alloc caasv1alpha1.Allocation) []*unstructured.Unstructured {
	out := []*unstructured.Unstructured{kubeadmConfigTemplate(tc)}
	for _, p := range tc.Spec.Workers {
		out = append(out, proxmoxMachineTemplate(tc, alloc, p), machineDeployment(tc, p))
	}
	return out
}

func kubeadmConfigTemplate(tc *caasv1alpha1.TenantCluster) *unstructured.Unstructured {
	keys := toAnySlice(tc.Spec.SSHAuthorizedKeys)
	users := []any{}
	if len(keys) > 0 {
		users = append(users, map[string]any{"name": "root", "sshAuthorizedKeys": keys})
	}
	return obj(gvBootstrap, "KubeadmConfigTemplate", tc.Name+kubeadmConfigSuffix, ClusterNamespace(tc), map[string]any{
		"template": map[string]any{
			"spec": map[string]any{
				"joinConfiguration": map[string]any{
					"nodeRegistration": map[string]any{
						"kubeletExtraArgs": map[string]any{
							"provider-id":   providerIDFromMeta,
							"feature-gates": kubeletFeatureGates, // required on k8s 1.34
						},
					},
				},
				"users": users,
			},
		},
	})
}

func proxmoxMachineTemplate(tc *caasv1alpha1.TenantCluster, alloc caasv1alpha1.Allocation, p caasv1alpha1.WorkerPool) *unstructured.Unstructured {
	return obj(gvInfra, "ProxmoxMachineTemplate", poolName(tc, p), ClusterNamespace(tc), map[string]any{
		"template": map[string]any{
			"spec": map[string]any{
				"disks": map[string]any{
					"bootVolume": map[string]any{"disk": "scsi0", "sizeGb": int64(p.DiskGiB)},
				},
				"format":    "raw",
				"full":      true,
				"memoryMiB": int64(p.MemoryMiB),
				"network": map[string]any{
					"default": map[string]any{
						"bridge": "vmbr0",
						"model":  "virtio",
						"vlan":   int64(alloc.VLAN), // VLAN tag — without it the VM has no L2 path
					},
				},
				"numCores":   int64(p.Cores),
				"numSockets": int64(1),
				"sourceNode": p.SourceNode,
				"templateID": int64(p.TemplateID),
			},
		},
	})
}

func machineDeployment(tc *caasv1alpha1.TenantCluster, p caasv1alpha1.WorkerPool) *unstructured.Unstructured {
	return obj(gvCore, "MachineDeployment", poolName(tc, p), ClusterNamespace(tc), map[string]any{
		"clusterName": tc.Name,
		"replicas":    int64(p.Replicas),
		"selector":    map[string]any{"matchLabels": nil},
		"template": map[string]any{
			"metadata": map[string]any{
				"labels": map[string]any{"node-role.kubernetes.io/node": ""},
			},
			"spec": map[string]any{
				"clusterName": tc.Name,
				"version":     tc.Spec.KubernetesVersion,
				"bootstrap": map[string]any{
					"configRef": map[string]any{
						"apiVersion": gvBootstrap,
						"kind":       "KubeadmConfigTemplate",
						"name":       tc.Name + kubeadmConfigSuffix,
					},
				},
				"infrastructureRef": map[string]any{
					"apiVersion": gvInfra,
					"kind":       "ProxmoxMachineTemplate",
					"name":       poolName(tc, p),
				},
			},
		},
	})
}

// --- helpers ---

func poolName(tc *caasv1alpha1.TenantCluster, p caasv1alpha1.WorkerPool) string {
	return fmt.Sprintf("%s-worker-%s", tc.Name, p.Name)
}

func toAnySlice(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

func obj(apiVersion, kind, name, namespace string, spec map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(apiVersion)
	u.SetKind(kind)
	u.SetName(name)
	u.SetNamespace(namespace)
	u.Object["spec"] = spec
	return u
}
