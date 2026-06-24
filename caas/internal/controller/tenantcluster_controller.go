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

package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	caasv1alpha1 "github.com/hbang/caas/api/v1alpha1"
	"github.com/hbang/caas/internal/allocator"
	"github.com/hbang/caas/internal/cni"
	"github.com/hbang/caas/internal/network"
	"github.com/hbang/caas/internal/render"
)

const (
	finalizer  = "caas.hbang.io/finalizer"
	fieldOwner = client.FieldOwner("caas")
)

// TenantClusterReconciler reconciles a TenantCluster object.
type TenantClusterReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Proxmox render.ProxmoxConfig
	Network network.Provider
}

// +kubebuilder:rbac:groups=caas.hbang.io,resources=tenantclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=caas.hbang.io,resources=tenantclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=caas.hbang.io,resources=tenantclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=caas.hbang.io,resources=vlanpools,verbs=get;list;watch
// +kubebuilder:rbac:groups=caas.hbang.io,resources=vlanpools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;machinedeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=kamajicontrolplanes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=proxmoxclusters;proxmoxmachinetemplates,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=bootstrap.cluster.x-k8s.io,resources=kubeadmconfigtemplates,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;delete

// Reconcile drives a TenantCluster towards a running cluster: allocate a VLAN,
// render and apply the CAPI bundle, and mirror child status back.
func (r *TenantClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, reterr error) {
	log := logf.FromContext(ctx)

	tc := &caasv1alpha1.TenantCluster{}
	if err := r.Get(ctx, req.NamespacedName, tc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !tc.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, tc)
	}

	// Persist status exactly once per reconcile, via a non-optimistic merge patch
	// (no resourceVersion precondition) so back-to-back reconciles don't conflict.
	base := tc.DeepCopy()
	defer func() {
		if err := r.Status().Patch(ctx, tc, client.MergeFrom(base)); err != nil && reterr == nil {
			reterr = err
		}
	}()

	if controllerutil.AddFinalizer(tc, finalizer) {
		if err := r.Update(ctx, tc); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Resolve the VLANPool.
	poolName := tc.Spec.PoolRef.Name
	if poolName == "" {
		poolName = "default"
	}
	pool := &caasv1alpha1.VLANPool{}
	if err := r.Get(ctx, types.NamespacedName{Name: poolName}, pool); err != nil {
		if apierrors.IsNotFound(err) {
			r.setCond(tc, caasv1alpha1.ConditionReady, metav1.ConditionFalse, "VLANPoolNotFound",
				fmt.Sprintf("VLANPool %q not found", poolName))
			tc.Status.Phase = caasv1alpha1.PhaseFailed
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	// 1. Allocate a VLAN (only once). MaxConcurrentReconciles=1 serializes this.
	if tc.Status.Allocation.VLAN == 0 {
		var list caasv1alpha1.TenantClusterList
		if err := r.List(ctx, &list); err != nil {
			return ctrl.Result{}, err
		}
		alloc, err := allocator.Allocate(pool, allocator.UsedVLANs(list.Items, string(tc.UID)))
		if err != nil {
			r.setCond(tc, caasv1alpha1.ConditionVLANAllocated, metav1.ConditionFalse, "Exhausted", err.Error())
			tc.Status.Phase = caasv1alpha1.PhaseFailed
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
		tc.Status.Allocation = alloc
		tc.Status.Phase = caasv1alpha1.PhaseAllocating
		r.setCond(tc, caasv1alpha1.ConditionVLANAllocated, metav1.ConditionTrue, "Allocated",
			fmt.Sprintf("VLAN %d, node subnet %s", alloc.VLAN, alloc.NodeSubnet))
		log.Info("allocated", "vlan", alloc.VLAN, "subnet", alloc.NodeSubnet)
	}
	alloc := tc.Status.Allocation

	// 1b. Ensure the per-cluster namespace exists (the bundle, credentials and
	//     kubeconfig all live there). Refuse to adopt a pre-existing namespace we
	//     don't own, so we never manage/delete an unrelated namespace.
	nsName := render.ClusterNamespace(tc)
	if err := r.ensureNamespace(ctx, tc, nsName); err != nil {
		r.setCond(tc, caasv1alpha1.ConditionReady, metav1.ConditionFalse, "NamespaceError", err.Error())
		tc.Status.Phase = caasv1alpha1.PhaseFailed
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	tc.Status.Namespace = nsName

	// 1c. Realize the tenant network (VLAN + subnet + gateway) on the network
	//     controller. Allowed internal target = the whole control-plane VIP pool, so
	//     this needs no Kamaji endpoint. NetworkReady gates overall readiness.
	netReady := r.reconcileNetwork(ctx, tc, pool, alloc)

	// 2. Apply control plane + Cluster + credentials together. Kamaji only assigns
	//    the control-plane endpoint once the owning Cluster exists, so the Cluster
	//    must NOT be gated on the endpoint.
	kcp := render.ControlPlane(tc)
	for _, o := range []*unstructured.Unstructured{kcp, render.Cluster(tc, alloc), render.CredentialsSecret(tc, r.Proxmox)} {
		if err := r.apply(ctx, tc, o); err != nil {
			return ctrl.Result{}, fmt.Errorf("apply %s/%s: %w", o.GetKind(), o.GetName(), err)
		}
	}

	// 3. Wait for Kamaji to assign the endpoint (re-applying kcp returns it).
	host, found, _ := unstructured.NestedString(kcp.Object, "spec", "controlPlaneEndpoint", "host")
	port, _, _ := unstructured.NestedInt64(kcp.Object, "spec", "controlPlaneEndpoint", "port")
	if !found || host == "" || port == 0 {
		r.setCond(tc, caasv1alpha1.ConditionControlPlaneReady, metav1.ConditionFalse, "AwaitingEndpoint",
			"waiting for Kamaji to assign a control-plane endpoint")
		tc.Status.Phase = caasv1alpha1.PhaseProvisioning
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	tc.Status.ControlPlaneEndpoint = fmt.Sprintf("%s:%d", host, port)

	// 4. Apply infra (needs the endpoint) and workers.
	infra := []*unstructured.Unstructured{render.InfraCluster(tc, pool, alloc, host, port)}
	infra = append(infra, render.Workers(tc, alloc)...)
	for _, o := range infra {
		if err := r.apply(ctx, tc, o); err != nil {
			return ctrl.Result{}, fmt.Errorf("apply %s/%s: %w", o.GetKind(), o.GetName(), err)
		}
	}

	// 5. Mirror child status.
	cpReady, _, _ := unstructured.NestedBool(kcp.Object, "status", "ready")
	r.setCond(tc, caasv1alpha1.ConditionControlPlaneReady, boolToStatus(cpReady), "Kamaji",
		fmt.Sprintf("control plane endpoint %s", tc.Status.ControlPlaneEndpoint))

	desired, ready := r.workerCounts(ctx, tc)
	tc.Status.ObservedWorkers = caasv1alpha1.ObservedWorkers{Desired: desired, Ready: ready}
	workersReady := desired > 0 && ready == desired
	r.setCond(tc, caasv1alpha1.ConditionWorkersReady, boolToStatus(workersReady), "Workers",
		fmt.Sprintf("%d/%d ready", ready, desired))

	// Install the CNI into the tenant once its control plane is reachable.
	cniReady := r.reconcileCNI(ctx, tc, cpReady)

	allReady := netReady && cpReady && workersReady && cniReady
	r.setCond(tc, caasv1alpha1.ConditionReady, boolToStatus(allReady), "Reconciled", "")
	tc.Status.KubeconfigSecretRef = tc.Name + "-kubeconfig"
	if allReady {
		tc.Status.Phase = caasv1alpha1.PhaseReady
	} else {
		tc.Status.Phase = caasv1alpha1.PhaseProvisioning
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// reconcileNetwork realizes the tenant's VLAN network + isolation on the network
// controller and reports whether it is ready.
func (r *TenantClusterReconciler) reconcileNetwork(ctx context.Context, tc *caasv1alpha1.TenantCluster, pool *caasv1alpha1.VLANPool, alloc caasv1alpha1.Allocation) bool {
	allow := []network.Target{}
	if cp := pool.Spec.ControlPlane.BGPPool; cp != "" {
		allow = append(allow, network.Target{CIDR: cp, Protocol: network.ProtocolTCP, Ports: []int{6443}})
	}
	spec := network.Spec{
		Ref:          network.Ref{ClusterID: tc.Namespace + "/" + tc.Name, VLAN: alloc.VLAN},
		Name:         tc.Name,
		Subnet:       alloc.NodeSubnet,
		Gateway:      alloc.Gateway,
		ManagedRange: alloc.HostRange,
		Isolation:    network.Isolation{AllowInternal: allow, AllowInternet: true},
	}
	st, err := r.Network.EnsureNetwork(ctx, spec)
	if err != nil {
		r.setCond(tc, caasv1alpha1.ConditionNetworkReady, metav1.ConditionFalse, "EnsureFailed", err.Error())
		return false
	}
	r.setCond(tc, caasv1alpha1.ConditionNetworkReady, boolToStatus(st.Ready), r.Network.Name(), st.Message)
	return st.Ready
}

// reconcileCNI installs the tenant CNI once the control plane is reachable, and
// reports whether it is ready. It's gated on the CNIReady condition so the
// (large) manifest apply happens once, not every reconcile.
func (r *TenantClusterReconciler) reconcileCNI(ctx context.Context, tc *caasv1alpha1.TenantCluster, cpReady bool) bool {
	if tc.Spec.CNI.Type == "none" {
		r.setCond(tc, caasv1alpha1.ConditionCNIReady, metav1.ConditionTrue, "None", "no CNI requested")
		return true
	}
	if meta.IsStatusConditionTrue(tc.Status.Conditions, caasv1alpha1.ConditionCNIReady) {
		return true
	}
	if !cpReady {
		r.setCond(tc, caasv1alpha1.ConditionCNIReady, metav1.ConditionFalse, "AwaitingControlPlane",
			"waiting for the tenant control plane to be reachable")
		return false
	}
	sec := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: render.ClusterNamespace(tc), Name: tc.Name + "-kubeconfig"}, sec); err != nil {
		r.setCond(tc, caasv1alpha1.ConditionCNIReady, metav1.ConditionFalse, "NoKubeconfig", err.Error())
		return false
	}
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(sec.Data["value"])
	if err != nil {
		r.setCond(tc, caasv1alpha1.ConditionCNIReady, metav1.ConditionFalse, "BadKubeconfig", err.Error())
		return false
	}
	if err := cni.InstallCilium(ctx, restCfg); err != nil {
		r.setCond(tc, caasv1alpha1.ConditionCNIReady, metav1.ConditionFalse, "InstallFailed", err.Error())
		return false
	}
	r.setCond(tc, caasv1alpha1.ConditionCNIReady, metav1.ConditionTrue, "Installed", "Cilium applied to tenant")
	return true
}

// reconcileDelete tears the cluster down in order and holds the finalizer (and so
// the VLAN) until everything is gone:
//  1. delete the CAPI Cluster and wait — this destroys the worker VMs while the
//     credentials Secret is still present (capmox needs it to auth to Proxmox);
//  2. delete the per-cluster namespace (cleans up creds, kubeconfig, leftovers);
//  3. once the namespace is gone, release the finalizer (frees the VLAN).
func (r *TenantClusterReconciler) reconcileDelete(ctx context.Context, tc *caasv1alpha1.TenantCluster) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(tc, finalizer) {
		return ctrl.Result{}, nil
	}
	tc.Status.Phase = caasv1alpha1.PhaseDeleting
	nsName := render.ClusterNamespace(tc)

	// 1. Delete the Cluster first (VMs torn down while creds Secret still exists).
	cl := &unstructured.Unstructured{}
	cl.SetAPIVersion("cluster.x-k8s.io/v1beta1")
	cl.SetKind("Cluster")
	switch err := r.Get(ctx, types.NamespacedName{Namespace: nsName, Name: tc.Name}, cl); {
	case err == nil:
		if cl.GetDeletionTimestamp() == nil {
			if e := r.Delete(ctx, cl, client.PropagationPolicy(metav1.DeletePropagationForeground)); e != nil && !apierrors.IsNotFound(e) {
				return ctrl.Result{}, e
			}
		}
		_ = r.updateStatus(ctx, tc)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil // wait for VMs to go
	case !apierrors.IsNotFound(err):
		return ctrl.Result{}, err
	}

	// 2. Cluster gone — delete the per-cluster namespace (only if it's ours).
	ns := &corev1.Namespace{}
	switch err := r.Get(ctx, types.NamespacedName{Name: nsName}, ns); {
	case err == nil:
		if r.ownsNamespace(ns, tc) && ns.DeletionTimestamp == nil {
			if e := r.Delete(ctx, ns); e != nil && !apierrors.IsNotFound(e) {
				return ctrl.Result{}, e
			}
		}
		_ = r.updateStatus(ctx, tc)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil // wait for ns to terminate
	case !apierrors.IsNotFound(err):
		return ctrl.Result{}, err
	}

	// 3. Namespace gone — remove the tenant network, then release the finalizer
	//    (and the VLAN). DeleteNetwork is idempotent, so a retry after a transient
	//    error is safe.
	if r.Network != nil {
		if err := r.Network.DeleteNetwork(ctx, network.Ref{ClusterID: tc.Namespace + "/" + tc.Name, VLAN: tc.Status.Allocation.VLAN}); err != nil {
			r.setCond(tc, caasv1alpha1.ConditionNetworkReady, metav1.ConditionFalse, "DeleteFailed", err.Error())
			_ = r.updateStatus(ctx, tc)
			return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
		}
	}
	controllerutil.RemoveFinalizer(tc, finalizer)
	return ctrl.Result{}, r.Update(ctx, tc)
}

// apply server-side-applies a bundle object. No ownerRef is set: the bundle lives
// in the per-cluster namespace while the TenantCluster lives in the control
// namespace, and ownerRefs cannot cross namespaces. Cleanup is by deleting the
// per-cluster namespace (see reconcileDelete).
func (r *TenantClusterReconciler) apply(ctx context.Context, _ *caasv1alpha1.TenantCluster, o *unstructured.Unstructured) error {
	return r.Patch(ctx, o, client.Apply, fieldOwner, client.ForceOwnership)
}

// ensureNamespace creates the per-cluster namespace if absent, labelled as owned by
// this TenantCluster. It refuses to adopt a pre-existing namespace that lacks our
// ownership label, so the operator never manages (or later deletes) an unrelated
// namespace such as default/kube-system.
func (r *TenantClusterReconciler) ensureNamespace(ctx context.Context, tc *caasv1alpha1.TenantCluster, name string) error {
	ns := &corev1.Namespace{}
	err := r.Get(ctx, types.NamespacedName{Name: name}, ns)
	switch {
	case apierrors.IsNotFound(err):
		ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				caasv1alpha1.LabelCluster:          tc.Name,
				caasv1alpha1.LabelControlNamespace: tc.Namespace,
				"app.kubernetes.io/managed-by":     "caas",
			},
		}}
		return r.Create(ctx, ns)
	case err != nil:
		return err
	}
	if !r.ownsNamespace(ns, tc) {
		return fmt.Errorf("namespace %q already exists and is not owned by this cluster", name)
	}
	return nil
}

// ownsNamespace reports whether ns was created by the operator for this tc.
func (r *TenantClusterReconciler) ownsNamespace(ns *corev1.Namespace, tc *caasv1alpha1.TenantCluster) bool {
	return ns.Labels[caasv1alpha1.LabelCluster] == tc.Name &&
		ns.Labels[caasv1alpha1.LabelControlNamespace] == tc.Namespace
}

// workerCounts sums desired/ready replicas across the cluster's MachineDeployments.
func (r *TenantClusterReconciler) workerCounts(ctx context.Context, tc *caasv1alpha1.TenantCluster) (desired, ready int32) {
	for _, p := range tc.Spec.Workers {
		md := &unstructured.Unstructured{}
		md.SetAPIVersion("cluster.x-k8s.io/v1beta1")
		md.SetKind("MachineDeployment")
		name := fmt.Sprintf("%s-worker-%s", tc.Name, p.Name)
		if err := r.Get(ctx, types.NamespacedName{Namespace: render.ClusterNamespace(tc), Name: name}, md); err != nil {
			desired += p.Replicas
			continue
		}
		d, _, _ := unstructured.NestedInt64(md.Object, "spec", "replicas")
		rd, _, _ := unstructured.NestedInt64(md.Object, "status", "readyReplicas")
		desired += int32(d)
		ready += int32(rd)
	}
	return desired, ready
}

func (r *TenantClusterReconciler) setCond(tc *caasv1alpha1.TenantCluster, t string, s metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(&tc.Status.Conditions, metav1.Condition{
		Type:               t,
		Status:             s,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: tc.Generation,
	})
}

func (r *TenantClusterReconciler) updateStatus(ctx context.Context, tc *caasv1alpha1.TenantCluster) error {
	return r.Status().Update(ctx, tc)
}

func boolToStatus(b bool) metav1.ConditionStatus {
	if b {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

// SetupWithManager sets up the controller with the Manager.
func (r *TenantClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&caasv1alpha1.TenantCluster{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}). // serialize VLAN allocation
		Named("tenantcluster").
		Complete(r)
}
