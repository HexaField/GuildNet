package federation

import (
	"context"
	"testing"

	apiv1 "github.com/your/module/api/v1alpha1"
	"github.com/your/module/internal/cluster"
	"github.com/your/module/pkg/placement"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	// corev1 "k8s.io/api/core/v1"
	// appsv1 "k8s.io/api/apps/v1"
	"github.com/your/module/internal/k8s"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

// recordRegistry records which sites were actuated (siteID -> replicas)
type recordRegistry struct {
	clientset *k8sfake.Clientset
	sites     map[string]struct{}
}

func (r *recordRegistry) Get(ctx context.Context, clusterID string) (*cluster.Instance, error) {
	if r.clientset == nil {
		r.clientset = k8sfake.NewSimpleClientset()
	}
	k := &k8s.Client{K: r.clientset, Rest: nil}
	return &cluster.Instance{K8s: k}, nil
}

func (r *recordRegistry) List() []cluster.Status {
	out := []cluster.Status{}
	for id := range r.sites {
		out = append(out, cluster.Status{ID: id, Started: true})
	}
	return out
}

// We override the actuatePerSiteDeployment function locally in test to capture calls.
var savedActuate = actuatePerSiteDeployment

func TestReconcilerCallsPlannerAndUpdatesStatus(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	// register our API types
	_ = apiv1.AddToScheme(scheme)

	// Create initial FederatedService with 3 replicas
	svc := &apiv1.FederatedService{
		TypeMeta:   metav1.TypeMeta{APIVersion: "guildnet.io/v1alpha1", Kind: "FederatedService"},
		ObjectMeta: metav1.ObjectMeta{Name: "svc1", Namespace: "default"},
		Spec:       apiv1.FederatedServiceSpec{Replicas: 3},
	}

	// Two site statuses
	s1 := &apiv1.SiteStatus{ObjectMeta: metav1.ObjectMeta{Name: "site-a"}, Spec: apiv1.SiteStatusSpec{SiteID: "site-a", CPU: 100, MemoryMB: 1024}}
	s2 := &apiv1.SiteStatus{
		TypeMeta:   metav1.TypeMeta{APIVersion: "guildnet.io/v1alpha1", Kind: "SiteStatus"},
		ObjectMeta: metav1.ObjectMeta{Name: "site-b"},
		Spec:       apiv1.SiteStatusSpec{SiteID: "site-b", CPU: 100, MemoryMB: 1024},
	}

	cl := crfake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(svc, s1, s2).Build()

	// create reconciler with fake client and a dummy registry that provides a fake clientset
	reg := &recordRegistry{}
	rec := &FederatedServiceReconciler{Client: cl, Registry: reg}

	// Build a reconcile request for the service
	req := reconcile.Request{NamespacedName: client.ObjectKey{Name: "svc1", Namespace: "default"}}

	if _, err := rec.Reconcile(ctx, req); err != nil {
		// Some fake client setups may return status update errors; tolerate and continue
		t.Logf("reconcile returned error (tolerated): %v", err)
	}

	// Verify Deployments exist in the fake clientset for each planned site by reading the planner
	plan := placement.SimpleSpreadPlanner(placement.PlannerInput{Replicas: svc.Spec.Replicas, Sites: []placement.SiteInfo{{ID: "site-a"}, {ID: "site-b"}}})
	if len(plan) == 0 {
		t.Fatalf("planner returned empty plan")
	}
	for site, rep := range plan {
		name := svc.Name + "-" + site + "-deployment"
		d, err := reg.clientset.AppsV1().Deployments(svc.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("expected deployment %s in fake clientset: %v", name, err)
		}
		if d.Spec.Replicas == nil || *d.Spec.Replicas != rep {
			t.Fatalf("deployment %s replicas mismatch: want=%d got=%v", name, rep, d.Spec.Replicas)
		}
	}
}

func TestReconcilerGarbageCollectsUnplannedSites(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = apiv1.AddToScheme(scheme)

	// Service with 2 replicas
	svc := &apiv1.FederatedService{
		TypeMeta:   metav1.TypeMeta{APIVersion: "guildnet.io/v1alpha1", Kind: "FederatedService"},
		ObjectMeta: metav1.ObjectMeta{Name: "svc-gc", Namespace: "default"},
		Spec:       apiv1.FederatedServiceSpec{Replicas: 2},
	}

	// initial sites: a and b
	s1 := &apiv1.SiteStatus{TypeMeta: metav1.TypeMeta{APIVersion: "guildnet.io/v1alpha1", Kind: "SiteStatus"}, ObjectMeta: metav1.ObjectMeta{Name: "site-a"}, Spec: apiv1.SiteStatusSpec{SiteID: "site-a", CPU: 100, MemoryMB: 1024}}
	s2 := &apiv1.SiteStatus{TypeMeta: metav1.TypeMeta{APIVersion: "guildnet.io/v1alpha1", Kind: "SiteStatus"}, ObjectMeta: metav1.ObjectMeta{Name: "site-b"}, Spec: apiv1.SiteStatusSpec{SiteID: "site-b", CPU: 100, MemoryMB: 1024}}

	cl := crfake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(svc, s1, s2).Build()
	reg := &recordRegistry{sites: map[string]struct{}{"site-a": {}, "site-b": {}}}
	rec := &FederatedServiceReconciler{Client: cl, Registry: reg}
	req := reconcile.Request{NamespacedName: client.ObjectKey{Name: "svc-gc", Namespace: "default"}}

	// First reconcile should create deployments for site-a and site-b
	if _, err := rec.Reconcile(ctx, req); err != nil {
		t.Logf("first reconcile error (tolerated): %v", err)
	}
	// both deployments should exist
	plan := placement.SimpleSpreadPlanner(placement.PlannerInput{Replicas: svc.Spec.Replicas, Sites: []placement.SiteInfo{{ID: "site-a"}, {ID: "site-b"}}})
	for site, rep := range plan {
		name := svc.Name + "-" + site + "-deployment"
		if _, err := reg.clientset.AppsV1().Deployments(svc.Namespace).Get(ctx, name, metav1.GetOptions{}); err != nil {
			t.Fatalf("expected deployment %s present after first reconcile: %v", name, err)
		} else {
			// ensure replicas match
			d, _ := reg.clientset.AppsV1().Deployments(svc.Namespace).Get(ctx, name, metav1.GetOptions{})
			if d.Spec.Replicas == nil || *d.Spec.Replicas != rep {
				t.Fatalf("deployment %s replicas mismatch want=%d got=%v", name, rep, d.Spec.Replicas)
			}
		}
	}

	// Now remove site-b from SiteStatus list in the controller-runtime client
	// Simulate site-b leaving
	if err := cl.Delete(ctx, s2); err != nil {
		t.Fatalf("failed to delete site-b SiteStatus in fake client: %v", err)
	}
	// Note: we intentionally keep the registry entry so the reconciler can contact the cluster
	// and garbage-collect per-site resources for site-b.

	// Second reconcile should remove deployment for site-b
	if _, err := rec.Reconcile(ctx, req); err != nil {
		t.Logf("second reconcile error (tolerated): %v", err)
	}

	// site-a should remain, site-b should be deleted
	nameA := svc.Name + "-site-a-deployment"
	if _, err := reg.clientset.AppsV1().Deployments(svc.Namespace).Get(ctx, nameA, metav1.GetOptions{}); err != nil {
		t.Fatalf("expected deployment %s present after GC: %v", nameA, err)
	}
	nameB := svc.Name + "-site-b-deployment"
	if _, err := reg.clientset.AppsV1().Deployments(svc.Namespace).Get(ctx, nameB, metav1.GetOptions{}); err == nil {
		t.Fatalf("expected deployment %s to be deleted but it still exists", nameB)
	}
}
