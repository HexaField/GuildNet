package federation

import (
	"context"
	"fmt"

	apiv1 "github.com/your/module/api/v1alpha1"
	"github.com/your/module/internal/cluster"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// RegistryGetter is the minimal interface required from a cluster registry for actuation.
type RegistryGetter interface {
	Get(context.Context, string) (*cluster.Instance, error)
}

// actuatePerSiteDeployment ensures a Deployment exists in the remote cluster for the given site.
func actuatePerSiteDeployment(ctx context.Context, reg RegistryGetter, siteID string, svc apiv1.FederatedService, replicas int32) error {
	if reg == nil {
		return fmt.Errorf("registry is nil")
	}
	inst, err := reg.Get(ctx, siteID)
	if err != nil {
		return fmt.Errorf("get instance for site %s: %w", siteID, err)
	}
	if inst == nil || inst.K8s == nil {
		return fmt.Errorf("no k8s client for site %s", siteID)
	}

	// Build a Deployment manifest. Prefer a user-supplied PodTemplateSpec in the CRD if present.
	name := fmt.Sprintf("%s-%s-deployment", svc.Name, siteID)
	var podTemplate corev1.PodTemplateSpec
	if svc.Spec.Template != nil {
		podTemplate = *svc.Spec.Template.DeepCopy()
		// ensure label selector label exists so the Deployment selector matches
		if podTemplate.ObjectMeta.Labels == nil {
			podTemplate.ObjectMeta.Labels = map[string]string{}
		}
		podTemplate.ObjectMeta.Labels["app"] = svc.Name
	} else {
		// PoC default template: busybox serving nothing useful, user should supply template in CRD
		podTemplate = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": svc.Name}},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:  svc.Name,
					Image: "busybox",
					Ports: []corev1.ContainerPort{{ContainerPort: int32(80), Protocol: corev1.ProtocolTCP}},
				}},
			},
		}
	}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: svc.Namespace,
			Labels: map[string]string{
				"app":  svc.Name,
				"site": siteID,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": svc.Name}},
			Template: podTemplate,
		},
	}

	// Use the per-cluster k8s client to Apply the Deployment. For now use a simple Create or Update by name.
	// Prefer using the clientset in inst.K8s for typed client operations.
	clientset := inst.K8s.K
	if clientset == nil {
		return fmt.Errorf("cluster clientset unavailable for site %s", siteID)
	}
	// Try to get existing deployment
	existing, err := clientset.AppsV1().Deployments(svc.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil && existing != nil {
		// Update replicas and image (derive image from podTemplate if available)
		existing.Spec.Replicas = &replicas
		var updateImage string
		if len(dep.Spec.Template.Spec.Containers) > 0 {
			updateImage = dep.Spec.Template.Spec.Containers[0].Image
		} else {
			updateImage = "busybox"
		}
		if len(existing.Spec.Template.Spec.Containers) > 0 {
			existing.Spec.Template.Spec.Containers[0].Image = updateImage
		}
		_, uerr := clientset.AppsV1().Deployments(svc.Namespace).Update(ctx, existing, metav1.UpdateOptions{})
		if uerr != nil {
			return fmt.Errorf("update deployment site=%s: %w", siteID, uerr)
		}
		return nil
	}
	// Create namespace if not exists (best-effort)
	if _, nerr := clientset.CoreV1().Namespaces().Get(ctx, svc.Namespace, metav1.GetOptions{}); nerr != nil {
		// attempt create
		_, _ = clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: svc.Namespace}}, metav1.CreateOptions{})
	}
	if _, cerr := clientset.AppsV1().Deployments(svc.Namespace).Create(ctx, dep, metav1.CreateOptions{}); cerr != nil {
		return fmt.Errorf("create deployment site=%s: %w", siteID, cerr)
	}
	return nil
}

// deletePerSiteDeployment deletes the per-site deployment for a service in the given cluster.
func deletePerSiteDeployment(ctx context.Context, reg RegistryGetter, siteID string, svc apiv1.FederatedService) error {
	if reg == nil {
		return fmt.Errorf("registry is nil")
	}
	inst, err := reg.Get(ctx, siteID)
	if err != nil {
		return fmt.Errorf("get instance for site %s: %w", siteID, err)
	}
	if inst == nil || inst.K8s == nil {
		return fmt.Errorf("no k8s client for site %s", siteID)
	}
	clientset := inst.K8s.K
	if clientset == nil {
		return fmt.Errorf("cluster clientset unavailable for site %s", siteID)
	}
	name := fmt.Sprintf("%s-%s-deployment", svc.Name, siteID)
	derr := clientset.AppsV1().Deployments(svc.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if derr != nil {
		fmt.Printf("deletePerSiteDeployment site=%s name=%s err=%v\n", siteID, name, derr)
		return derr
	}
	fmt.Printf("deletePerSiteDeployment site=%s name=%s deleted\n", siteID, name)
	return nil
}

// actuatePerSiteService ensures a Service exists in the remote cluster for the given site.
func actuatePerSiteService(ctx context.Context, reg RegistryGetter, siteID string, svc apiv1.FederatedService) error {
	if reg == nil {
		return fmt.Errorf("registry is nil")
	}
	inst, err := reg.Get(ctx, siteID)
	if err != nil {
		return fmt.Errorf("get instance for site %s: %w", siteID, err)
	}
	if inst == nil || inst.K8s == nil {
		return fmt.Errorf("no k8s client for site %s", siteID)
	}
	clientset := inst.K8s.K
	if clientset == nil {
		return fmt.Errorf("cluster clientset unavailable for site %s", siteID)
	}

	name := fmt.Sprintf("%s-%s-service", svc.Name, siteID)
	svcPorts := []corev1.ServicePort{}
	for _, p := range svc.Spec.Ports {
		svcPorts = append(svcPorts, corev1.ServicePort{Name: p.Name, Port: p.Port, TargetPort: intstrFromInt32(p.TargetPort)})
	}

	s := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: svc.Namespace, Labels: map[string]string{"app": svc.Name, "site": siteID}},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": svc.Name}, Ports: svcPorts},
	}

	// Try to get existing service
	if _, err := clientset.CoreV1().Services(svc.Namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		// Update is optional for now (ports may change); perform Update
		if _, uerr := clientset.CoreV1().Services(svc.Namespace).Update(ctx, s, metav1.UpdateOptions{}); uerr != nil {
			return fmt.Errorf("update service site=%s: %w", siteID, uerr)
		}
		return nil
	}
	// Create namespace if not exists (best-effort)
	if _, nerr := clientset.CoreV1().Namespaces().Get(ctx, svc.Namespace, metav1.GetOptions{}); nerr != nil {
		_, _ = clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: svc.Namespace}}, metav1.CreateOptions{})
	}
	if _, cerr := clientset.CoreV1().Services(svc.Namespace).Create(ctx, s, metav1.CreateOptions{}); cerr != nil {
		return fmt.Errorf("create service site=%s: %w", siteID, cerr)
	}
	return nil
}

// deletePerSiteService deletes the per-site Service for a service in the given cluster.
func deletePerSiteService(ctx context.Context, reg RegistryGetter, siteID string, svc apiv1.FederatedService) error {
	if reg == nil {
		return fmt.Errorf("registry is nil")
	}
	inst, err := reg.Get(ctx, siteID)
	if err != nil {
		return fmt.Errorf("get instance for site %s: %w", siteID, err)
	}
	if inst == nil || inst.K8s == nil {
		return fmt.Errorf("no k8s client for site %s", siteID)
	}
	clientset := inst.K8s.K
	if clientset == nil {
		return fmt.Errorf("cluster clientset unavailable for site %s", siteID)
	}
	name := fmt.Sprintf("%s-%s-service", svc.Name, siteID)
	derr := clientset.CoreV1().Services(svc.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if derr != nil {
		fmt.Printf("deletePerSiteService site=%s name=%s err=%v\n", siteID, name, derr)
		return derr
	}
	fmt.Printf("deletePerSiteService site=%s name=%s deleted\n", siteID, name)
	return nil
}

// intstrFromInt32 is a small helper to build an IntOrString from an int32.
func intstrFromInt32(v int32) intstr.IntOrString {
	return intstr.IntOrString{Type: intstr.Int, IntVal: v}
}
