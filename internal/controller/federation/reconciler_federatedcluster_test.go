package federation

import (
	"context"
	"testing"

	apiv1 "github.com/your/module/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestFederatedClusterReconcileAddsSpecClusterNameToStatus(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = apiv1.AddToScheme(scheme)

	fc := &apiv1.FederatedCluster{
		TypeMeta:   metav1.TypeMeta{APIVersion: "guildnet.io/v1alpha1", Kind: "FederatedCluster"},
		ObjectMeta: metav1.ObjectMeta{Name: "fc1", Namespace: "default"},
		Spec:       apiv1.FederatedClusterSpec{ClusterName: "cluster-a"},
	}

	cl := crfake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(fc).Build()

	rec := &FederatedClusterReconciler{Client: cl}
	if _, err := rec.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: fc.Name, Namespace: fc.Namespace}}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Read back the object and assert status contains the cluster name
	var got apiv1.FederatedCluster
	if err := cl.Get(ctx, client.ObjectKey{Name: fc.Name, Namespace: fc.Namespace}, &got); err != nil {
		t.Fatalf("get after reconcile: %v", err)
	}
	if len(got.Status.Members) == 0 || got.Status.Members[0] != "cluster-a" {
		t.Fatalf("expected status.members to include 'cluster-a', got: %#v", got.Status.Members)
	}
}

func TestFederatedClusterReconcileNoSpecClusterNameLeavesStatusEmpty(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = apiv1.AddToScheme(scheme)

	fc := &apiv1.FederatedCluster{
		TypeMeta:   metav1.TypeMeta{APIVersion: "guildnet.io/v1alpha1", Kind: "FederatedCluster"},
		ObjectMeta: metav1.ObjectMeta{Name: "fc2", Namespace: "default"},
		Spec:       apiv1.FederatedClusterSpec{ClusterName: ""},
	}

	cl := crfake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(fc).Build()

	rec := &FederatedClusterReconciler{Client: cl}
	if _, err := rec.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: fc.Name, Namespace: fc.Namespace}}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	var got apiv1.FederatedCluster
	if err := cl.Get(ctx, client.ObjectKey{Name: fc.Name, Namespace: fc.Namespace}, &got); err != nil {
		t.Fatalf("get after reconcile: %v", err)
	}
	if len(got.Status.Members) != 0 {
		t.Fatalf("expected empty status.members, got: %#v", got.Status.Members)
	}
}

// helper to build a reconcile.Request-like object key
// (no helper required; use ctrl.Request directly)
