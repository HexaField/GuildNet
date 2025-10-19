package proxy

import (
	"context"
	"fmt"

	v1 "github.com/your/module/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// ProxyReconciler is a minimal controller that writes per-site ConfigMaps
// containing endpoint lists and ensures a minimal DaemonSet exists per site.
type ProxyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *ProxyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	// Attempt to fetch the FederatedService; if missing, cleanup any per-site artifacts
	var svc v1.FederatedService
	if err := r.Get(ctx, req.NamespacedName, &svc); err != nil {
		// NotFound or other error - nothing to do here in this scaffold
		logger.Info("FederatedService not found, nothing to reconcile", "name", req.NamespacedName)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// For each member site in the hypothetical cluster registry we would compute endpoints.
	// For the scaffold, create a single ConfigMap with a placeholder entry.
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("proxy-endpoints-%s-%s", svc.Namespace, svc.Name),
			Namespace: svc.Namespace,
			Labels: map[string]string{
				"guildnet.federation": "proxy",
			},
		},
	}

	// CreateOrUpdate the ConfigMap and set controller reference so K8s GC handles cleanup
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data["endpoints"] = "[]"
		// set owner reference to the FederatedService so GC will remove this CM when svc deleted
		if err := controllerutil.SetControllerReference(&svc, cm, r.Scheme); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		logger.Error(err, "failed to create/update proxy configmap")
		return ctrl.Result{}, err
	}

	// Ensure a placeholder DaemonSet exists in the service namespace. Use CreateOrUpdate and set ownerref.
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("guildnet-proxy-%s", svc.Name),
			Namespace: svc.Namespace,
			Labels:    map[string]string{"guildnet.proxy": "true"},
		},
	}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, ds, func() error {
		ds.Labels = map[string]string{"guildnet.proxy": "true"}
		ds.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"guildnet.proxy": "true"}}
		ds.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"guildnet.proxy": "true"}},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:  "proxy",
					Image: "ghcr.io/hexa-field/guildnet-proxy:latest",
					Args:  []string{"--endpoints-file", "/etc/guildnet/endpoints.json"},
					VolumeMounts: []corev1.VolumeMount{{
						Name:      "endpoints",
						MountPath: "/etc/guildnet",
					}},
				}},
				Volumes: []corev1.Volume{{
					Name:         "endpoints",
					VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: cm.Name}}},
				}},
			},
		}
		if err := controllerutil.SetControllerReference(&svc, ds, r.Scheme); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		logger.Error(err, "failed to create/update proxy daemonset")
		return ctrl.Result{}, err
	}

	logger.Info("reconciled proxy artifacts", "service", svc.Name)
	return ctrl.Result{}, nil
}

func (r *ProxyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.FederatedService{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&appsv1.DaemonSet{}).
		Complete(r)
}
