package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	caasv1alpha1 "github.com/hbang/caas/api/v1alpha1"
)

func cluster(name string, managedByUI bool) *caasv1alpha1.TenantCluster {
	tc := &caasv1alpha1.TenantCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "caas-system"},
	}
	tc.Spec.Workers = []caasv1alpha1.WorkerPool{
		{Name: "lab01", SourceNode: "lab01", TemplateID: 106, Replicas: 1, Cores: 4, MemoryMiB: 8192, DiskGiB: 60},
	}
	if managedByUI {
		tc.Labels = map[string]string{caasv1alpha1.LabelManagedBy: caasv1alpha1.ManagedByUI}
	}
	return tc
}

func TestManagedByUI(t *testing.T) {
	if !managedByUI(*cluster("a", true)) {
		t.Error("ui-stamped cluster should be managedByUI")
	}
	if managedByUI(*cluster("b", false)) {
		t.Error("unstamped cluster should not be managedByUI")
	}
	other := cluster("c", false)
	other.Labels = map[string]string{caasv1alpha1.LabelManagedBy: "gitops"}
	if managedByUI(*other) {
		t.Error("gitops-stamped cluster should not be managedByUI")
	}
}

func testServer(t *testing.T, objs ...client.Object) *Server {
	t.Helper()
	sch := runtime.NewScheme()
	if err := caasv1alpha1.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(sch).WithObjects(objs...).Build()
	nodes := []NodeTemplate{
		{Node: "lab01", TemplateID: 106}, {Node: "lab02", TemplateID: 107}, {Node: "lab03", TemplateID: 108},
	}
	s, err := NewServer(c, ":0", "caas-system", nodes, []string{"v1.34.3"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// post drives a handler through the mux so path values ({ns}/{name}) are populated.
func post(s *Server, path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	return rr
}

func TestScaleRejectsGitOpsCluster(t *testing.T) {
	s := testServer(t, cluster("gitcluster", false))
	rr := post(s, "/clusters/caas-system/gitcluster/scale", url.Values{"workers": {"3"}})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("scale on gitops cluster = %d, want 403", rr.Code)
	}
}

func TestDeleteRejectsGitOpsCluster(t *testing.T) {
	s := testServer(t, cluster("gitcluster", false))
	rr := post(s, "/clusters/caas-system/gitcluster/delete", url.Values{})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("delete on gitops cluster = %d, want 403", rr.Code)
	}
	// must still exist
	got := &caasv1alpha1.TenantCluster{}
	if err := s.Client.Get(context.Background(), client.ObjectKey{Namespace: "caas-system", Name: "gitcluster"}, got); err != nil {
		t.Fatalf("gitops cluster should not have been deleted: %v", err)
	}
}

func TestScaleAllowsUICluster(t *testing.T) {
	s := testServer(t, cluster("uicluster", true))
	rr := post(s, "/clusters/caas-system/uicluster/scale", url.Values{"workers": {"6"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("scale on ui cluster = %d, want 303", rr.Code)
	}
	got := &caasv1alpha1.TenantCluster{}
	if err := s.Client.Get(context.Background(), client.ObjectKey{Namespace: "caas-system", Name: "uicluster"}, got); err != nil {
		t.Fatal(err)
	}
	if n := totalReplicas(got); n != 6 {
		t.Errorf("after scale to 6, total replicas = %d", n)
	}
}
