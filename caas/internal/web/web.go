/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License").
*/

// Package web serves a small embedded UI for launching, listing, inspecting and
// deleting TenantClusters. It runs in the operator process and uses the same
// Kubernetes client; the CRDs are the source of truth (no separate datastore).
package web

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	caasv1alpha1 "github.com/hbang/caas/api/v1alpha1"
)

//go:embed templates/*.html
var templatesFS embed.FS

// MachineSize is a UI-only preset that expands to concrete worker resources. It is
// not part of the CRD — the form resolves a chosen size to cores/memoryMiB/diskGiB
// before the TenantCluster is created, so the API stays purely concrete.
type MachineSize struct {
	Name      string
	Cores     int
	MemoryMiB int
	DiskGiB   int
}

// machineSizes is the catalog offered in the launch form, in display order.
var machineSizes = []MachineSize{
	{Name: "small", Cores: 2, MemoryMiB: 4 * 1024, DiskGiB: 40},
	{Name: "medium", Cores: 4, MemoryMiB: 8 * 1024, DiskGiB: 60},
	{Name: "large", Cores: 4, MemoryMiB: 16 * 1024, DiskGiB: 100},
	{Name: "xlarge", Cores: 8, MemoryMiB: 32 * 1024, DiskGiB: 200},
}

const defaultSize = "large"

func (m MachineSize) Label() string {
	return fmt.Sprintf("%s — %d vCPU / %d GiB / %d GiB", m.Name, m.Cores, m.MemoryMiB/1024, m.DiskGiB)
}

// resolveVersion returns name if it's an allowed Kubernetes version, else the first
// configured version. Guards against form tampering with an unsupported version.
func (s *Server) resolveVersion(name string) string {
	for _, v := range s.KubernetesVersions {
		if v == name {
			return v
		}
	}
	if len(s.KubernetesVersions) > 0 {
		return s.KubernetesVersions[0]
	}
	return "v1.34.3"
}

// spreadWorkers distributes count workers across the configured Proxmox nodes
// round-robin (e.g. 4 workers over 3 nodes → 2/1/1); node placement is hidden from
// the UI, so this is where it's decided. It returns one WorkerPool per node *always* —
// including replicas:0 — so that scaling down (a node's share dropping to 0) just sets
// that MachineDeployment to 0 replicas (CAPI removes its VMs) instead of dropping the
// pool from the applied bundle, which the controller's server-side apply would not
// prune. Each pool pins to its node's local template (render clones onto the source node).
func (s *Server) spreadWorkers(count int, size MachineSize) []caasv1alpha1.WorkerPool {
	n := len(s.NodeTemplates)
	if n == 0 {
		return nil
	}
	if count < 0 {
		count = 0
	}
	base, rem := count/n, count%n
	workers := make([]caasv1alpha1.WorkerPool, 0, n)
	for i, nt := range s.NodeTemplates {
		reps := base
		if i < rem {
			reps++ // hand the remainder to the first nodes
		}
		workers = append(workers, caasv1alpha1.WorkerPool{
			Name: nt.Node, SourceNode: nt.Node, TemplateID: nt.TemplateID,
			Replicas:  int32(reps),
			Cores:     size.Cores,
			MemoryMiB: size.MemoryMiB,
			DiskGiB:   size.DiskGiB,
		})
	}
	return workers
}

// workerSizeOf recovers the machine size from a cluster's existing workers so a
// scale keeps the same resources (the size preset isn't stored, only concrete values).
func workerSizeOf(tc *caasv1alpha1.TenantCluster) MachineSize {
	for _, w := range tc.Spec.Workers {
		if w.Cores > 0 {
			return MachineSize{Cores: w.Cores, MemoryMiB: w.MemoryMiB, DiskGiB: w.DiskGiB}
		}
	}
	return resolveSize(defaultSize)
}

// totalReplicas sums the desired worker replicas across a cluster's pools.
func totalReplicas(tc *caasv1alpha1.TenantCluster) int {
	var n int
	for _, w := range tc.Spec.Workers {
		n += int(w.Replicas)
	}
	return n
}

// resolveSize returns the preset for name, defaulting if unknown.
func resolveSize(name string) MachineSize {
	for _, s := range machineSizes {
		if s.Name == name {
			return s
		}
	}
	for _, s := range machineSizes {
		if s.Name == defaultSize {
			return s
		}
	}
	return machineSizes[0]
}

// NodeTemplate maps a Proxmox source node to its pre-baked image template, used to
// populate the launch form.
type NodeTemplate struct {
	Node       string
	TemplateID int
}

// ParseNodeTemplates parses "lab01=106,lab02=107,lab03=108" into NodeTemplates.
func ParseNodeTemplates(s string) ([]NodeTemplate, error) {
	var out []NodeTemplate
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("invalid node-template %q (want node=templateID)", part)
		}
		id, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return nil, fmt.Errorf("invalid templateID in %q: %w", part, err)
		}
		out = append(out, NodeTemplate{Node: strings.TrimSpace(k), TemplateID: id})
	}
	return out, nil
}

// Server is the web UI. It is a controller-runtime Runnable.
type Server struct {
	Client        client.Client
	Addr          string
	Namespace     string
	NodeTemplates []NodeTemplate
	// KubernetesVersions are the selectable versions in the launch form. They must
	// each have a matching pre-baked worker image template on Proxmox.
	KubernetesVersions []string
	tmpl               *template.Template
}

// NewServer parses templates and returns a ready Server.
func NewServer(c client.Client, addr, namespace string, nodes []NodeTemplate, k8sVersions []string) (*Server, error) {
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"age":       humanAge,
		"cond":      conditionStatus,
		"uiManaged": managedByUI,
		"managedBy": managedByLabel,
	}).ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	if len(k8sVersions) == 0 {
		k8sVersions = []string{"v1.34.3"}
	}
	return &Server{Client: c, Addr: addr, Namespace: namespace, NodeTemplates: nodes, KubernetesVersions: k8sVersions, tmpl: tmpl}, nil
}

// NeedLeaderElection lets the UI serve on every replica, not just the leader.
func (s *Server) NeedLeaderElection() bool { return false }

// Start runs the HTTP server until ctx is cancelled (controller-runtime Runnable).
func (s *Server) Start(ctx context.Context) error {
	srv := &http.Server{Addr: s.Addr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Handler builds the route mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("POST /clusters", s.handleCreate)
	mux.HandleFunc("GET /clusters/{ns}/{name}", s.handleDetail)
	mux.HandleFunc("POST /clusters/{ns}/{name}/scale", s.handleScale)
	mux.HandleFunc("POST /clusters/{ns}/{name}/delete", s.handleDelete)
	mux.HandleFunc("GET /clusters/{ns}/{name}/kubeconfig", s.handleKubeconfig)
	return mux
}

// handleRoot serves the list at "/" and a styled 404 for any other unmatched path
// (the "/" pattern is the catch-all in Go 1.22 ServeMux).
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		s.renderError(w, http.StatusNotFound, "Page not found",
			fmt.Sprintf("There's nothing at %s.", r.URL.Path))
		return
	}
	s.handleList(w, r)
}

type listView struct {
	Clusters           []caasv1alpha1.TenantCluster
	Sizes              []MachineSize
	DefaultSize        string
	KubernetesVersions []string
	Namespace          string
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	var list caasv1alpha1.TenantClusterList
	if err := s.Client.List(r.Context(), &list); err != nil {
		s.httpError(w, err)
		return
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	s.render(w, "list.html", listView{
		Clusters: list.Items,
		Sizes:    machineSizes, DefaultSize: defaultSize,
		KubernetesVersions: s.KubernetesVersions, Namespace: s.Namespace,
	})
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.httpError(w, err)
		return
	}
	name := r.FormValue("name")
	if name == "" {
		s.renderError(w, http.StatusBadRequest, "Name is required", "Give the cluster a name and try again.")
		return
	}
	cpReplicas := atoiDefault(r.FormValue("cpReplicas"), 1)
	version := s.resolveVersion(r.FormValue("kubernetesVersion"))
	const cniType = "cilium" // CNI is fixed; not user-selectable

	// The form asks only for a total worker count + size; placement across Proxmox
	// nodes is an implementation detail the operator owns (each node has its own
	// local template, so we render one pool per node it lands on).
	workerCount := atoiDefault(r.FormValue("workers"), 0)
	if workerCount <= 0 {
		s.renderError(w, http.StatusBadRequest, "Workers must be at least 1", "Set the worker count to 1 or more, then try again.")
		return
	}
	size := resolveSize(r.FormValue("size")) // expand preset to concrete values
	workers := s.spreadWorkers(workerCount, size)
	if len(workers) == 0 {
		s.renderError(w, http.StatusInternalServerError, "No Proxmox nodes configured", "The operator has no node templates to place workers on.")
		return
	}

	tc := &caasv1alpha1.TenantCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: s.Namespace,
			// Stamp provenance so the UI knows it owns this cluster (and may
			// scale/delete it). Clusters created elsewhere (GitOps) lack this label
			// and are read-only here.
			Labels: map[string]string{caasv1alpha1.LabelManagedBy: caasv1alpha1.ManagedByUI},
		},
		Spec: caasv1alpha1.TenantClusterSpec{
			KubernetesVersion: version,
			PoolRef:           caasv1alpha1.PoolRef{Name: "default"},
			ControlPlane:      caasv1alpha1.ControlPlaneSpec{Replicas: int32(cpReplicas)},
			CNI:               caasv1alpha1.CNISpec{Type: cniType},
			Workers:           workers,
		},
	}
	if err := s.Client.Create(r.Context(), tc); err != nil {
		s.httpError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

type detailView struct {
	Cluster        caasv1alpha1.TenantCluster
	DesiredWorkers int // sum of spec replicas — prefills the scale form
}

func (s *Server) handleDetail(w http.ResponseWriter, r *http.Request) {
	tc, err := s.get(r)
	if err != nil {
		s.httpError(w, err)
		return
	}
	s.render(w, "detail.html", detailView{Cluster: *tc, DesiredWorkers: totalReplicas(tc)})
}

// handleScale re-spreads a new worker count across the nodes (preserving size) and
// updates the cluster; the reconciler then scales the MachineDeployments.
func (s *Server) handleScale(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.httpError(w, err)
		return
	}
	count := atoiDefault(r.FormValue("workers"), 0)
	if count <= 0 {
		s.renderError(w, http.StatusBadRequest, "Workers must be at least 1", "Set the worker count to 1 or more, then try again.")
		return
	}
	tc, err := s.get(r)
	if err != nil {
		s.httpError(w, err)
		return
	}
	if !managedByUI(*tc) {
		s.renderError(w, http.StatusForbidden, "Cluster is managed by GitOps",
			"Scaling is disabled in the UI for clusters it didn't create. Change the worker count in its source (git).")
		return
	}
	tc.Spec.Workers = s.spreadWorkers(count, workerSizeOf(tc))
	if len(tc.Spec.Workers) == 0 {
		s.renderError(w, http.StatusInternalServerError, "No Proxmox nodes configured", "The operator has no node templates to place workers on.")
		return
	}
	if err := s.Client.Update(r.Context(), tc); err != nil {
		s.httpError(w, err)
		return
	}
	http.Redirect(w, r, "/clusters/"+tc.Namespace+"/"+tc.Name, http.StatusSeeOther)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	// Fetch first so we can refuse to delete a cluster the UI doesn't own — deleting
	// a GitOps-managed cluster here would just have ArgoCD recreate it (and races the
	// finalizer teardown). Such clusters must be removed from their source instead.
	tc, err := s.get(r)
	if err != nil {
		if apierrors.IsNotFound(err) { // already gone — treat delete as success
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		s.httpError(w, err)
		return
	}
	if !managedByUI(*tc) {
		s.renderError(w, http.StatusForbidden, "Cluster is managed by GitOps",
			"Deletion is disabled in the UI for clusters it didn't create. Remove it from its source (git) instead.")
		return
	}
	if err := s.Client.Delete(r.Context(), tc); err != nil && !apierrors.IsNotFound(err) {
		s.httpError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleKubeconfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	// The kubeconfig Secret lives in the cluster's own namespace (status.Namespace).
	tc, err := s.get(r)
	if err != nil {
		s.httpError(w, err)
		return
	}
	clusterNS := tc.Status.Namespace
	if clusterNS == "" {
		s.renderError(w, http.StatusNotFound, "Kubeconfig not ready yet", "The cluster's namespace hasn't been assigned. Check back once it's provisioning.")
		return
	}
	sec := &corev1.Secret{}
	if err := s.Client.Get(r.Context(), types.NamespacedName{Namespace: clusterNS, Name: name + "-kubeconfig"}, sec); err != nil {
		if apierrors.IsNotFound(err) {
			s.renderError(w, http.StatusNotFound, "Kubeconfig not ready yet", "The control plane hasn't issued a kubeconfig. Check back once it's Ready.")
			return
		}
		s.httpError(w, err)
		return
	}
	data := sec.Data["value"]
	if len(data) == 0 {
		s.renderError(w, http.StatusNotFound, "Kubeconfig not ready yet", "The control plane hasn't issued a kubeconfig. Check back once it's Ready.")
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s.kubeconfig", name))
	_, _ = w.Write(data)
}

func (s *Server) get(r *http.Request) (*caasv1alpha1.TenantCluster, error) {
	tc := &caasv1alpha1.TenantCluster{}
	err := s.Client.Get(r.Context(), types.NamespacedName{Namespace: r.PathValue("ns"), Name: r.PathValue("name")}, tc)
	return tc, err
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		// Rendering already started writing; fall back to plain text.
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// --- helpers ---

type errorView struct {
	Code    int
	Title   string
	Message string
}

// renderError serves the styled error page with a "back to clusters" button.
func (s *Server) renderError(w http.ResponseWriter, code int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if err := s.tmpl.ExecuteTemplate(w, "error.html", errorView{Code: code, Title: title, Message: message}); err != nil {
		http.Error(w, message, code)
	}
}

// httpError maps an error to a styled page: Kubernetes NotFound → 404, else 500.
func (s *Server) httpError(w http.ResponseWriter, err error) {
	if apierrors.IsNotFound(err) {
		s.renderError(w, http.StatusNotFound, "Cluster not found", "It may have been deleted.")
		return
	}
	s.renderError(w, http.StatusInternalServerError, "Something went wrong", err.Error())
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func humanAge(t metav1.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t.Time).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// managedByUI reports whether the UI created (and may therefore mutate) a cluster.
func managedByUI(tc caasv1alpha1.TenantCluster) bool {
	return tc.Labels[caasv1alpha1.LabelManagedBy] == caasv1alpha1.ManagedByUI
}

// managedByLabel is a display string for who owns the cluster ("UI" / "GitOps").
// Anything not stamped by the UI is shown as GitOps (its source of truth is external).
func managedByLabel(tc caasv1alpha1.TenantCluster) string {
	if managedByUI(tc) {
		return "UI"
	}
	return "GitOps"
}

func conditionStatus(tc caasv1alpha1.TenantCluster, condType string) string {
	c := meta.FindStatusCondition(tc.Status.Conditions, condType)
	if c == nil {
		return "Unknown"
	}
	return string(c.Status)
}
