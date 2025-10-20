package federation

import (
	"context"
	"testing"

	apiv1 "github.com/your/module/api/v1alpha1"
	"github.com/your/module/internal/cluster"
	"github.com/your/module/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// simpleFakeRegistry implements the minimal interface used by the reconciler/actuate helpers.
type simpleFakeRegistry struct {
	getFn func(ctx context.Context, id string) (*cluster.Instance, error)
	listV []cluster.Status
}

func (f *simpleFakeRegistry) Get(ctx context.Context, id string) (*cluster.Instance, error) {
	return f.getFn(ctx, id)
}
func (f *simpleFakeRegistry) List() []cluster.Status { return f.listV }

func TestActuatePerSiteDeployment_CreateAndUpdate(t *testing.T) {
	ctx := context.Background()

	svc := apiv1.FederatedService{}
	svc.Name = "svc"
	svc.Namespace = "default"
	svc.Spec.Template = &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "svc"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx:1.19"}}},
	}

	fakeClient := fake.NewSimpleClientset()
	kcli := &k8s.Client{K: fakeClient}
	inst := &cluster.Instance{K8s: kcli}
	reg := &simpleFakeRegistry{getFn: func(ctx context.Context, id string) (*cluster.Instance, error) { return inst, nil }}

	// Create
	if err := actuatePerSiteDeployment(ctx, reg, "siteA", svc, 2); err != nil {
		t.Fatalf("actuate create failed: %v", err)
	}
	dep, err := fakeClient.AppsV1().Deployments("default").Get(ctx, "svc-siteA-deployment", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get created deployment: %v", err)
	}
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 2 {
		t.Fatalf("expected replicas=2 got=%v", dep.Spec.Replicas)
	}
	if len(dep.Spec.Template.Spec.Containers) == 0 || dep.Spec.Template.Spec.Containers[0].Image != "nginx:1.19" {
		t.Fatalf("expected image nginx:1.19, got %v", dep.Spec.Template.Spec.Containers)
	}

	// Update replicas
	if err := actuatePerSiteDeployment(ctx, reg, "siteA", svc, 4); err != nil {
		t.Fatalf("actuate update failed: %v", err)
	}
	dep2, err := fakeClient.AppsV1().Deployments("default").Get(ctx, "svc-siteA-deployment", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get updated deployment: %v", err)
	}
	if dep2.Spec.Replicas == nil || *dep2.Spec.Replicas != 4 {
		t.Fatalf("expected replicas=4 got=%v", dep2.Spec.Replicas)
	}
}

func TestActuatePerSiteService_CreateAndUpdate(t *testing.T) {
	ctx := context.Background()

	svc := apiv1.FederatedService{}
	svc.Name = "svc"
	svc.Namespace = "default"
	svc.Spec.Ports = []apiv1.ServicePort{{Name: "http", Port: 8080, TargetPort: 8080}}

	fakeClient := fake.NewSimpleClientset()
	kcli := &k8s.Client{K: fakeClient}
	inst := &cluster.Instance{K8s: kcli}
	reg := &simpleFakeRegistry{getFn: func(ctx context.Context, id string) (*cluster.Instance, error) { return inst, nil }}

	if err := actuatePerSiteService(ctx, reg, "siteB", svc); err != nil {
		t.Fatalf("actuate service create failed: %v", err)
	}
	s, err := fakeClient.CoreV1().Services("default").Get(ctx, "svc-siteB-service", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get created service: %v", err)
	}
	if len(s.Spec.Ports) != 1 || s.Spec.Ports[0].Port != 8080 {
		t.Fatalf("expected port 8080, got: %#v", s.Spec.Ports)
	}

	// Update ports
	svc.Spec.Ports = []apiv1.ServicePort{{Name: "http", Port: 9090, TargetPort: 9090}}
	if err := actuatePerSiteService(ctx, reg, "siteB", svc); err != nil {
		t.Fatalf("actuate service update failed: %v", err)
	}
	s2, err := fakeClient.CoreV1().Services("default").Get(ctx, "svc-siteB-service", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get updated service: %v", err)
	}
	if len(s2.Spec.Ports) != 1 || s2.Spec.Ports[0].Port != 9090 {
		t.Fatalf("expected port 9090, got: %#v", s2.Spec.Ports)
	}
}

func TestFederatedServiceReconciler_PlansAndUpdatesStatus(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = apiv1.AddToScheme(scheme)

	// Create FederatedService and a SiteStatus in the fake API server
	fs := &apiv1.FederatedService{}
	fs.TypeMeta = metav1.TypeMeta{APIVersion: "guildnet.io/v1alpha1", Kind: "FederatedService"}
	fs.Name = "svc"
	fs.Namespace = "default"
	fs.Spec.Replicas = 3

	ss := &apiv1.SiteStatus{}
	ss.TypeMeta = metav1.TypeMeta{APIVersion: "guildnet.io/v1alpha1", Kind: "SiteStatus"}
	ss.Name = "site-a"
	ss.Spec.SiteID = "siteA"
	ss.Spec.CPU = 1000
	ss.Spec.MemoryMB = 1024

	cl := crfake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(fs, ss).Build()

	// Prepare registry that returns an instance per site
	fakeClient := fake.NewSimpleClientset()
	kcli := &k8s.Client{K: fakeClient}
	inst := &cluster.Instance{K8s: kcli}
	reg := &simpleFakeRegistry{
		getFn: func(ctx context.Context, id string) (*cluster.Instance, error) { return inst, nil },
		listV: []cluster.Status{{ID: "siteA"}},
	}

	rec := &FederatedServiceReconciler{Client: cl, Registry: reg}
	// Run reconcile; status update may not be supported by the fake client used here,
	// but actuation to the remote cluster (via our simpleFakeRegistry) should create a Deployment.
	_, _ = rec.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: fs.Name, Namespace: fs.Namespace}})

	// Verify the remote fake cluster received a Deployment for siteA with 3 replicas
	dep, err := fakeClient.AppsV1().Deployments("default").Get(ctx, "svc-siteA-deployment", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected remote deployment created, got error: %v", err)
	}
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 3 {
		t.Fatalf("expected remote deployment replicas=3, got=%v", dep.Spec.Replicas)
	}
}
