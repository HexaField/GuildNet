package operator

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/your/module/api/v1alpha1"
)

// WorkspaceReconciler reconciles a Workspace object into a Deployment + Service.
type WorkspaceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// cached operator-level default for whether workspaces without an explicit
	// exposure should be LoadBalancer. This value is kept up-to-date by watching
	// the in-cluster ConfigMap `guildnet-cluster-settings` in namespace
	// `guildnet-system` so we avoid a GET on every reconcile.
	DefaultLB bool
	mu        sync.RWMutex
}

// Reconcile implements the reconciliation loop.
func (r *WorkspaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("reconcile start", "name", req.Name, "namespace", req.Namespace)
	ws := &apiv1alpha1.Workspace{}
	if err := r.Get(ctx, req.NamespacedName, ws); err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "failed to get workspace")
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Desired Deployment + Service names.
	depName := ws.Name
	svcName := ws.Name

	// Build desired container ports from spec (with defaults)
	var ports []corev1.ContainerPort
	for _, p := range ws.Spec.Ports {
		ports = append(ports, corev1.ContainerPort{ContainerPort: p.ContainerPort, Name: p.Name, Protocol: p.Protocol})
	}
	if len(ports) == 0 {
		ports = []corev1.ContainerPort{{ContainerPort: 8080, Name: "http"}}
	}

	// Build env (filter blanks, add defaults)
	var env []corev1.EnvVar
	for _, e := range ws.Spec.Env {
		if strings.TrimSpace(e.Name) == "" {
			continue
		}
		env = append(env, e)
	}
	envIndex := map[string]int{}
	for i, e := range env {
		envIndex[e.Name] = i
	}
	if _, ok := envIndex["PORT"]; !ok {
		env = append(env, corev1.EnvVar{Name: "PORT", Value: "8080"})
	}
	imgLower := strings.ToLower(ws.Spec.Image)
	if strings.Contains(imgLower, "codercom/code-server") || strings.Contains(imgLower, "code-server") {
		if _, exists := envIndex["PASSWORD"]; !exists {
			env = append(env, corev1.EnvVar{Name: "PASSWORD", Value: "changeme"})
		}
	}

	// Probes and args
	var command []string
	var readiness *corev1.Probe
	var liveness *corev1.Probe
	var args []string
	probePort := intstr.FromInt(int(ports[0].ContainerPort))
	if strings.Contains(imgLower, "alpine") {
		command = []string{"/bin/sh", "-c", "while true; do echo -e 'HTTP/1.1 200 OK\\r\\nContent-Length:2\\r\\n\\r\\nok' | nc -l -p 8080 -w 1; done"}
		readiness = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: probePort}}, InitialDelaySeconds: 10, PeriodSeconds: 5, TimeoutSeconds: 3, FailureThreshold: 12, SuccessThreshold: 1}
		liveness = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: probePort}}, InitialDelaySeconds: 60, PeriodSeconds: 15, TimeoutSeconds: 5, FailureThreshold: 3, SuccessThreshold: 1}
	} else {
		// Try /healthz first while keeping "/" as a fallback
		readiness = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: probePort}}, InitialDelaySeconds: 10, PeriodSeconds: 5, TimeoutSeconds: 3, FailureThreshold: 12, SuccessThreshold: 1}
		liveness = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: probePort}}, InitialDelaySeconds: 60, PeriodSeconds: 15, TimeoutSeconds: 5, FailureThreshold: 4, SuccessThreshold: 1}
	}
	if strings.Contains(imgLower, "codercom/code-server") || strings.Contains(imgLower, "code-server") {
		// code-server must bind on 0.0.0.0:8080 w/ password auth; base path handled by proxy
		args = []string{"--bind-addr", "0.0.0.0:8080", "--auth", "password"}
	}

	// Security context
	var secCtx *corev1.SecurityContext
	defFalse := func() *bool { b := false; return &b }()
	defTrue := func() *bool { b := true; return &b }()
	// Default: strict, no privilege escalation, non-root, drop all caps
	secCtx = &corev1.SecurityContext{
		AllowPrivilegeEscalation: defFalse,
		RunAsNonRoot:             defTrue,
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
	// Special-case: nginx images historically required running as root to bind
	// to port 80. Prefer using the unprivileged nginx image variant which runs
	// as uid 101 (nginx) and avoids granting root capabilities. For nginx
	// images, set conservative non-root defaults here; later we will switch
	// the image to the unprivileged variant and set pod-level non-root
	// PodSecurityContext to ensure the container runs as uid/gid 101.
	if strings.Contains(imgLower, "nginx") {
		// conservative container-level security context: require non-root
		secCtx = &corev1.SecurityContext{
			AllowPrivilegeEscalation: defFalse,
			RunAsNonRoot:             defTrue,
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		}
	}
	// Relax for code-server to allow SUID fixuid to run (no_new_privs must be disabled)
	if strings.Contains(imgLower, "codercom/code-server") || strings.Contains(imgLower, "code-server") {
		secCtx = &corev1.SecurityContext{
			AllowPrivilegeEscalation: defTrue,
			RunAsNonRoot:             defTrue,
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		}
	}

	// Reconcile Deployment via server-side apply so the operator can take
	// field ownership of podTemplate fields (initContainers, securityContext,
	// volumes). This avoids strategic-merge surprises from other actors.
	// For server-side apply the object must have its TypeMeta (APIVersion/Kind)
	// populated so the API server can interpret the apply request. Set it
	// explicitly here to avoid runtime errors when using client.Apply.
	desired := &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Namespace: ws.Namespace, Name: depName},
	}

	// Build PodTemplateSpec (same as before)
	init := corev1.Container{
		Name:            "workspace-init",
		Image:           "busybox:1.35.0",
		Command:         []string{"sh", "-c", "chown -R 101:101 /var/cache/nginx || true; ls -ld /var/cache/nginx || true"},
		SecurityContext: &corev1.SecurityContext{RunAsUser: func() *int64 { v := int64(0); return &v }()},
		VolumeMounts:    []corev1.VolumeMount{{Name: "nginx-cache", MountPath: "/var/cache/nginx"}},
	}

	workspaceContainer := corev1.Container{
		Name:            "workspace",
		Image:           ws.Spec.Image,
		Env:             env,
		Command:         command,
		Args:            args,
		Ports:           ports,
		SecurityContext: secCtx,
		ReadinessProbe:  readiness,
		LivenessProbe:   liveness,
		VolumeMounts:    []corev1.VolumeMount{{Name: "nginx-cache", MountPath: "/var/cache/nginx"}},
	}

	podSpec := corev1.PodSpec{
		Containers:     []corev1.Container{workspaceContainer},
		InitContainers: []corev1.Container{init},
		Volumes:        []corev1.Volume{{Name: "nginx-cache", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
		Tolerations:    []corev1.Toleration{{Key: "node-role.kubernetes.io/control-plane", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}},
	}
	if strings.Contains(imgLower, "nginx") {
		// Use non-root pod-level securityContext for the unprivileged nginx
		// image so the container runs as uid/gid 101 and can use the
		// initContainer-chown pattern safely.
		podSpec.SecurityContext = &corev1.PodSecurityContext{
			RunAsUser:      func() *int64 { v := int64(101); return &v }(),
			RunAsGroup:     func() *int64 { v := int64(101); return &v }(),
			FSGroup:        func() *int64 { v := int64(101); return &v }(),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		}
		// If the workspace image looked like nginx, prefer the unprivileged
		// variant unless the user already explicitly set an unprivileged
		// image. We will override the container image below when constructing
		// the pod template.
	} else {
		podSpec.SecurityContext = &corev1.PodSecurityContext{
			RunAsUser:      func() *int64 { v := int64(1000); return &v }(),
			RunAsGroup:     func() *int64 { v := int64(1000); return &v }(),
			FSGroup:        func() *int64 { v := int64(1000); return &v }(),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		}
	}

	// If the original image looked like nginx, prefer the unprivileged
	// nginx image so the pod can run without granting additional
	// capabilities. If the user already provided a custom image that
	// contains "unprivileged" we leave it alone. Make the preferred
	// unprivileged image configurable via WORKSPACE_NGINX_UNPRIVILEGED_IMAGE.
	if strings.Contains(imgLower, "nginx") {
		preferred := os.Getenv("WORKSPACE_NGINX_UNPRIVILEGED_IMAGE")
		if strings.TrimSpace(preferred) == "" {
			preferred = "nginxinc/nginx-unprivileged:1.25"
		}
		if !strings.Contains(strings.ToLower(ws.Spec.Image), "unprivileged") {
			workspaceContainer.Image = preferred
		}
		// Ensure the container-level securityContext does not force running
		// as root; the pod-level PodSecurityContext above will enforce uid/gid.
		workspaceContainer.SecurityContext = nil
	}

	podTemplate := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"guildnet.io/workspace": ws.Name}}, Spec: podSpec}

	replicas := int32(1)
	desired.Labels = map[string]string{"guildnet.io/workspace": ws.Name}
	desired.Spec = appsv1.DeploymentSpec{
		Replicas: &replicas,
		Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"guildnet.io/workspace": ws.Name}},
		Template: podTemplate,
		Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType, RollingUpdate: &appsv1.RollingUpdateDeployment{MaxSurge: func() *intstr.IntOrString { v := intstr.FromString("25%"); return &v }(), MaxUnavailable: func() *intstr.IntOrString { v := intstr.FromString("25%"); return &v }()}},
	}

	// NOTE: Do NOT set the owner reference on the desired object before
	// performing a server-side apply. If the owner (the Workspace) does not
	// have fully-populated TypeMeta fields when we embed it, the API server
	// can reject the apply. Instead we will set the controller reference on
	// the live Deployment after the apply succeeds (below).

	// NOTE: Some clusters exhibit issues with server-side apply (for example
	// rejecting apply payloads with "invalid object type" errors). To keep
	// the operator functional across a range of clusters, use a conservative
	// Get/Create/Update flow here. This loses server-side apply field ownership
	// semantics but reliably ensures the Deployment matches the desired spec.
	// We still perform post-update verification below and a delete+create
	// fallback if necessary.
	if cerr := controllerutil.SetControllerReference(ws, desired, r.Scheme); cerr != nil {
		logger.Error(cerr, "failed to set controller reference on desired deployment before ensure")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	existing := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: ws.Namespace, Name: depName}, existing); err != nil {
		if apierrors.IsNotFound(err) {
			if cerr := r.Create(ctx, desired); cerr != nil {
				logger.Error(cerr, "failed to create deployment", "deployment", depName)
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
		} else {
			logger.Error(err, "failed to get deployment")
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	} else {
		// Update only the spec portion we manage. This avoids clobbering other
		// fields that other controllers may legitimately manage.
		existing.Spec = desired.Spec
		if uerr := r.Update(ctx, existing); uerr != nil {
			logger.Error(uerr, "failed to update deployment", "deployment", depName)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}

	// Refresh live Deployment into dep for status reporting below
	dep := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: ws.Namespace, Name: depName}, dep); err != nil {
		logger.Error(err, "failed to get deployment after apply")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Ensure the live Deployment has an owner reference pointing to the
	// Workspace so Kubernetes garbage-collects it when the Workspace is
	// removed. Do this after the server-side apply to avoid embedding the
	// Workspace object into the apply payload (which can fail if TypeMeta is
	// missing on the owner). Update with retry on conflict.
	if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		// Reload current deployment before mutating
		cur := &appsv1.Deployment{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: ws.Namespace, Name: depName}, cur); err != nil {
			return err
		}
		if err := controllerutil.SetControllerReference(ws, cur, r.Scheme); err != nil {
			return err
		}
		return r.Update(ctx, cur)
	}); err != nil {
		logger.Error(err, "failed to set controller reference on deployment after apply")
		// Not a hard failure for reconciliation; continue to verification below.
	}

	// After apply, the API server may still drop or mutate certain fields
	// (initContainers, pod-level securityContext, container image/securityContext)
	// due to merge semantics or prior field managers. Verify concretely that the
	// live Deployment matches the desired pod template and re-apply a few
	// times; if that fails, fall back to deleting and recreating the Deployment
	// so the operator becomes the authoritative owner of the podTemplate.
	var finalErr error
	// capture desired pieces for comparison
	desiredPodSC := podSpec.SecurityContext
	desiredContainerImage := workspaceContainer.Image

	cmpInt64 := func(a, b *int64) bool {
		if a == nil && b == nil {
			return true
		}
		if a == nil || b == nil {
			return false
		}
		return *a == *b
	}

	for attempt := 0; attempt < 5; attempt++ {
		postDep := &appsv1.Deployment{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: ws.Namespace, Name: depName}, postDep); err != nil {
			finalErr = err
			logger.Error(err, "failed to get deployment while verifying post-apply state", "attempt", attempt)
			time.Sleep(500 * time.Millisecond)
			continue
		}

		needFix := false

		// If init container got dropped, request re-apply
		if len(postDep.Spec.Template.Spec.InitContainers) == 0 {
			logger.Info("initContainers missing after apply; will retry apply", "workspace", ws.Name, "attempt", attempt)
			needFix = true
		}

		// Verify pod-level securityContext matches the desired values (presence
		// and numeric uid/gid/fsGroup). For nginx we expect non-root 101; for
		// others we expect the conservative non-root 1000 configured above.
		liveSC := postDep.Spec.Template.Spec.SecurityContext
		if desiredPodSC == nil {
			if liveSC != nil {
				logger.Info("unexpected pod-level securityContext present; will retry apply/clear", "workspace", ws.Name, "attempt", attempt)
				needFix = true
			}
		} else {
			if liveSC == nil {
				logger.Info("pod-level securityContext missing after apply; will retry apply", "workspace", ws.Name, "attempt", attempt)
				needFix = true
			} else {
				if !cmpInt64(desiredPodSC.RunAsUser, liveSC.RunAsUser) || !cmpInt64(desiredPodSC.RunAsGroup, liveSC.RunAsGroup) || !cmpInt64(desiredPodSC.FSGroup, liveSC.FSGroup) {
					logger.Info("pod-level securityContext mismatch; will retry apply", "workspace", ws.Name, "attempt", attempt, "desired", desiredPodSC, "live", liveSC)
					needFix = true
				}
			}
		}

		// Verify the workspace container image and container-level securityContext
		// match the desired values.
		foundWorkspace := false
		for _, c := range postDep.Spec.Template.Spec.Containers {
			if c.Name == "workspace" {
				foundWorkspace = true
				if c.Image != desiredContainerImage {
					logger.Info("workspace container image mismatch; will retry apply", "workspace", ws.Name, "attempt", attempt, "desired", desiredContainerImage, "live", c.Image)
					needFix = true
				}
				// For nginx images we intentionally clear the container-level
				// securityContext and rely on the pod-level context; for other
				// images we expect a container-level securityContext to be set.
				if strings.Contains(imgLower, "nginx") {
					if c.SecurityContext != nil {
						logger.Info("workspace container securityContext present for nginx image; will retry apply/clear", "workspace", ws.Name, "attempt", attempt)
						needFix = true
					}
				} else {
					if c.SecurityContext == nil {
						logger.Info("workspace container securityContext missing for non-nginx image; will retry apply", "workspace", ws.Name, "attempt", attempt)
						needFix = true
					}
				}
				break
			}
		}
		if !foundWorkspace {
			logger.Info("workspace container not found in live deployment; will retry apply", "workspace", ws.Name, "attempt", attempt)
			needFix = true
		}

		if !needFix {
			finalErr = nil
			break
		}

		// Re-apply by performing a targeted Get/Update: copy the desired
		// spec into the live Deployment and update. This avoids server-side
		// apply which some API servers in this environment reject.
		if perr := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			cur := &appsv1.Deployment{}
			if err := r.Get(ctx, client.ObjectKey{Namespace: ws.Namespace, Name: depName}, cur); err != nil {
				return err
			}
			cur.Spec = desired.Spec
			return r.Update(ctx, cur)
		}); perr != nil {
			logger.Error(perr, "re-apply update failed", "attempt", attempt)
			finalErr = perr
		}

		time.Sleep(500 * time.Millisecond)
	}

	// If after retries we still don't control the podTemplate fields, do a
	// delete+create to ensure the operator is authoritative for the
	// Deployment's pod template. This is more aggressive but guarantees we
	// can enforce initContainers and securityContext as required.
	if finalErr != nil {
		logger.Info("post-apply verification failed; recreating Deployment to ensure operator ownership", "deployment", depName)

		// Attempt to delete the existing Deployment (foreground) and then
		// create the desired Deployment from scratch.
		delObj := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ws.Namespace, Name: depName}}
		if derr := r.Delete(ctx, delObj, client.PropagationPolicy(metav1.DeletePropagationForeground)); derr != nil {
			if !apierrors.IsNotFound(derr) {
				logger.Error(derr, "failed to delete deployment during recreate fallback", "deployment", depName)
			}
		} else {
			// Wait for deletion to complete (best-effort small backoff)
			for i := 0; i < 20; i++ {
				time.Sleep(200 * time.Millisecond)
				chk := &appsv1.Deployment{}
				if err := r.Get(ctx, client.ObjectKey{Namespace: ws.Namespace, Name: depName}, chk); apierrors.IsNotFound(err) {
					break
				}
			}
		}

		// Create desired Deployment anew
		// Ensure controller reference is set again
		if err := controllerutil.SetControllerReference(ws, desired, r.Scheme); err != nil {
			logger.Error(err, "failed to set controller reference on desired deployment before recreate")
		} else {
			if cerr := r.Create(ctx, desired); cerr != nil {
				if !apierrors.IsAlreadyExists(cerr) {
					logger.Error(cerr, "failed to create deployment during recreate fallback", "deployment", depName)
				} else {
					logger.Info("deployment already exists after delete; skipping create", "deployment", depName)
				}
			} else {
				logger.Info("recreated deployment to enforce operator ownership", "deployment", depName)
			}
		}
	}

	// Use cached operator-level default which is kept in memory by a watch.
	// Fallback to env var if the in-memory value hasn't been initialized yet.
	r.mu.RLock()
	defaultLB := r.DefaultLB
	r.mu.RUnlock()
	if !defaultLB {
		// If cached is false, still allow env var fallback (for clusters where no
		// ConfigMap exists and the operator hasn't initialized the cache yet).
		if v := strings.ToLower(strings.TrimSpace(os.Getenv("WORKSPACE_LB_DEFAULT"))); v == "1" || v == "true" || v == "yes" {
			defaultLB = true
		}
	}

	// Reconcile Service via CreateOrUpdate with retry
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: svcName, Namespace: ws.Namespace}}
	if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		// fresh object each try
		svc.ObjectMeta = metav1.ObjectMeta{Name: svcName, Namespace: ws.Namespace}
		_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
			svc.Labels = map[string]string{"guildnet.io/workspace": ws.Name}
			svc.Spec.Selector = map[string]string{"guildnet.io/workspace": ws.Name}
			var svcPorts []corev1.ServicePort
			for _, cp := range ports {
				svcPorts = append(svcPorts, corev1.ServicePort{Name: cp.Name, Port: cp.ContainerPort, TargetPort: intstrFromPort(cp)})
			}
			svc.Spec.Ports = svcPorts
			svc.Spec.Type = corev1.ServiceTypeClusterIP
			// Include not-ready addresses so proxy can route during warmup
			svc.Spec.PublishNotReadyAddresses = true
			if ws.Spec.Exposure != nil && ws.Spec.Exposure.Type == apiv1alpha1.ExposureLoadBalancer {
				svc.Spec.Type = corev1.ServiceTypeLoadBalancer
			} else if ws.Spec.Exposure == nil && defaultLB {
				// Operator-level default: expose workspace as LoadBalancer when no explicit exposure set
				svc.Spec.Type = corev1.ServiceTypeLoadBalancer
			}
			return controllerutil.SetControllerReference(ws, svc, r.Scheme)
		})
		return err
	}); err != nil {
		logger.Error(err, "reconcile service failed")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Update Status with retry on conflict
	if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		fresh := &apiv1alpha1.Workspace{}
		if gerr := r.Get(ctx, req.NamespacedName, fresh); gerr != nil {
			return gerr
		}
		fresh.Status.ReadyReplicas = dep.Status.ReadyReplicas
		fresh.Status.ServiceDNS = fmt.Sprintf("%s.%s.svc", svc.Name, svc.Namespace)
		if svc.Spec.ClusterIP != "" {
			fresh.Status.ServiceIP = svc.Spec.ClusterIP
		}
		switch {
		case fresh.DeletionTimestamp != nil:
			fresh.Status.Phase = apiv1alpha1.PhaseTerminating
		case dep.Status.ReadyReplicas > 0:
			fresh.Status.Phase = apiv1alpha1.PhaseRunning
		default:
			fresh.Status.Phase = apiv1alpha1.PhasePending
		}
		fresh.Status.ProxyTarget = fmt.Sprintf("http://%s:%d", fresh.Status.ServiceDNS, ports[0].ContainerPort)
		return r.Status().Update(ctx, fresh)
	}); err != nil {
		logger.Error(err, "status update failed")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	logger.Info("reconcile complete", "phase", ws.Status.Phase, "ready", ws.Status.ReadyReplicas, "svc", fmt.Sprintf("%s.%s.svc", svc.Name, svc.Namespace))
	return ctrl.Result{}, nil
}

func intstrFromPort(p corev1.ContainerPort) intstr.IntOrString {
	return intstr.FromInt(int(p.ContainerPort))
}

// SetupWithManager wires controller to manager.
func (r *WorkspaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Initialize cached default from existing ConfigMap (if present) so we have
	// a sensible value on startup.
	ctx := context.Background()
	var cm corev1.ConfigMap
	if err := mgr.GetClient().Get(ctx, client.ObjectKey{Namespace: "guildnet-system", Name: "guildnet-cluster-settings"}, &cm); err == nil {
		if v, ok := cm.Data["workspace_lb_enabled"]; ok {
			vv := strings.ToLower(strings.TrimSpace(v))
			if vv == "1" || vv == "true" || vv == "yes" {
				r.mu.Lock()
				r.DefaultLB = true
				r.mu.Unlock()
			}
		}
	}

	// Build core controller for Workspace resources.
	builder := ctrl.NewControllerManagedBy(mgr).
		For(&apiv1alpha1.Workspace{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{})

	// Start a background goroutine that polls the guildnet-cluster-settings
	// ConfigMap in `guildnet-system` for changes. When the boolean value
	// changes we update the cached flag and patch all Workspace objects with a
	// short annotation (`guildnet.io/config-hash`) to force reconcile.
	go func() {
		// use the manager's client for reads/patches
		cli := mgr.GetClient()
		var last string
		for {
			var cm corev1.ConfigMap
			err := cli.Get(context.Background(), client.ObjectKey{Namespace: "guildnet-system", Name: "guildnet-cluster-settings"}, &cm)
			v := ""
			if err == nil {
				v = strings.ToLower(strings.TrimSpace(cm.Data["workspace_lb_enabled"]))
			}
			if v == "1" || v == "true" || v == "yes" {
				v = "true"
			} else if v == "0" || v == "false" || v == "no" {
				v = "false"
			}
			if v == "" {
				// No explicit configmap value; fall back to env var
				if ev := strings.ToLower(strings.TrimSpace(os.Getenv("WORKSPACE_LB_DEFAULT"))); ev == "1" || ev == "true" || ev == "yes" {
					v = "true"
				} else {
					v = "false"
				}
			}

			if v != last {
				last = v
				val := v == "true"
				r.mu.Lock()
				r.DefaultLB = val
				r.mu.Unlock()

				// Patch workspaces to trigger reconciles by setting/updating an annotation
				var wsList apiv1alpha1.WorkspaceList
				if err := cli.List(context.Background(), &wsList); err == nil {
					for _, w := range wsList.Items {
						patch := client.MergeFrom(w.DeepCopy())
						if w.Annotations == nil {
							w.Annotations = map[string]string{}
						}
						w.Annotations["guildnet.io/config-hash"] = fmt.Sprintf("%d", time.Now().Unix())
						_ = cli.Patch(context.Background(), &w, patch)
					}
				}
			}

			time.Sleep(5 * time.Second)
		}
	}()

	return builder.Complete(r)
}
