package federation

import (
	"context"
	"testing"

	apiv1 "github.com/your/module/api/v1alpha1"
	"github.com/your/module/internal/cluster"
	"github.com/your/module/internal/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetes "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

// fakeRegistry implements minimal subset of cluster.Registry used by actuatePerSiteDeployment
type fakeRegistry struct {
	instances map[string]*cluster.Instance
}

func (f *fakeRegistry) Get(ctx context.Context, clusterID string) (*cluster.Instance, error) {
	if f.instances == nil {
		f.instances = map[string]*cluster.Instance{}
	}
	if inst, ok := f.instances[clusterID]; ok {
		return inst, nil
	}
	cs := fake.NewSimpleClientset()
	// assign to the kubernetes.Interface so fake Clientset can be used
	var kcs kubernetes.Interface = cs
	k := &k8s.Client{K: kcs, Rest: nil}
	inst := &cluster.Instance{K8s: k}
	f.instances[clusterID] = inst
	return inst, nil
}

func TestActuatePerSiteDeploymentCreatesDeployment(t *testing.T) {
	ctx := context.Background()
	svc := apiv1.FederatedService{}
	svc.Name = "myapp"
	svc.Namespace = "default"
	svc.Spec.Selector = map[string]string{"app": "myapp"}
	svc.Spec.Replicas = 2

	// Ensure actuatePerSiteDeployment returns error when registry is nil
	if err := actuatePerSiteDeployment(ctx, nil, "site-a", svc, 2); err == nil {
		t.Fatalf("expected error when registry is nil, got nil")
	}

	fr := &fakeRegistry{}
	inst, err := fr.Get(ctx, "site-a")
	if err != nil {
		t.Fatalf("fake registry get: %v", err)
	}
	if inst == nil || inst.K8s == nil || inst.K8s.K == nil {
		t.Fatalf("fake k8s client missing")
	}

	// Actuate using the fake registry; should create the deployment in the fake clientset
	if err := actuatePerSiteDeployment(ctx, fr, "site-a", svc, 2); err != nil {
		t.Fatalf("actuatePerSiteDeployment failed: %v", err)
	}

	// Verify deployment exists
	if _, err := inst.K8s.K.AppsV1().Deployments(svc.Namespace).Get(ctx, "myapp-site-a-deployment", metav1.GetOptions{}); err != nil {
		t.Fatalf("expected deployment to be present in fake clientset: %v", err)
	}
}
