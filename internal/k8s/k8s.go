package k8s

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/your/module/internal/model"
)

type Client struct {
	K    kubernetes.Interface
	Rest *rest.Config
}

func kubeconfigDefault() string {
	// Primary: explicit KUBECONFIG if set
	if v := os.Getenv("KUBECONFIG"); v != "" {
		return v
	}
	// Fallback: use the user's standard kubeconfig (~/.kube/config)
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".kube", "config")
	}
	return ""
}

func New(ctx context.Context) (*Client, error) {
	// Single-cluster creation is intentionally removed. This codebase supports
	// multi-device (federated) operation only. Callers should obtain per-cluster
	// kubeconfigs from the Registry and use NewFromKubeconfig, or provide an
	// explicit control-plane kubeconfig via GN_CONTROL_PLANE_KUBECONFIG if an
	// embedded operator/controller-runtime manager is required.
	return nil, fmt.Errorf("k8s.New() is unsupported: single-cluster mode removed; use NewFromKubeconfig or per-cluster Registry")
}

// NewFromKubeconfig builds a client from a kubeconfig string, applying optional per-cluster overrides.
func NewFromKubeconfig(ctx context.Context, kubeconfigYAML string, opts struct {
	APIProxyURL string
	ForceHTTP   bool
	Dial        func(ctx context.Context, network, addr string) (net.Conn, error)
}) (*Client, error) {
	if strings.TrimSpace(kubeconfigYAML) == "" {
		return nil, fmt.Errorf("empty kubeconfig")
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfigYAML))
	if err != nil {
		return nil, err
	}
	if v := strings.TrimSpace(opts.APIProxyURL); v != "" {
		cfg.Host = v
		if strings.HasPrefix(strings.ToLower(v), "http://") {
			cfg.TLSClientConfig = rest.TLSClientConfig{}
		}
	}
	if opts.ForceHTTP {
		if u, err := url.Parse(cfg.Host); err == nil {
			u.Scheme = "http"
			cfg.Host = u.String()
		}
	}
	// Optional custom dialer (e.g., tsnet per-cluster)
	if opts.Dial != nil {
		cfg.Dial = opts.Dial
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{K: cs, Rest: cfg}, nil
}

// Config returns the REST config used to reach the API server.
func (c *Client) Config() *rest.Config { return c.Rest }

func dns1123Name(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			prevDash = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case r == '-' || r == '_' || r == ' ':
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	res := strings.Trim(b.String(), "-")
	for strings.Contains(res, "--") {
		res = strings.ReplaceAll(res, "--", "-")
	}
	if res == "" {
		res = "workload"
	}
	return res
}

type EnsureOpts struct {
	Namespace string
	ID        string // stable id label
	// New: runtime options from per-cluster settings
	ImagePullSecret   string
	LBEnabled         bool
	LBPool            string
	IngressDomain     string
	IngressTLSSecret  string
	IngressClassName  string
	CertManagerIssuer string
	IngressAuthURL    string
	IngressAuthSignin string
}

// EnsureDeploymentAndService creates or updates a Deployment and Service for the job spec.
func (c *Client) EnsureDeploymentAndService(ctx context.Context, spec model.JobSpec, opt EnsureOpts) (name string, id string, err error) {
	ns := opt.Namespace
	if ns == "" {
		ns = "default"
	}
	name = dns1123Name(spec.Name)
	if name == "" {
		name = dns1123Name(spec.Image)
	}
	if opt.ID == "" {
		id = name
	} else {
		id = opt.ID
	}

	labels := map[string]string{
		"app":                    name,
		"app.kubernetes.io/name": name,
		"guildnet.io/managed":    "true",
		"guildnet.io/id":         id,
	}

	// container ports
	cports := []corev1.ContainerPort{}
	for _, p := range spec.Expose {
		if p.Port <= 0 {
			continue
		}
		cp := corev1.ContainerPort{ContainerPort: int32(p.Port)}
		if p.Name != "" {
			cp.Name = p.Name
		}
		cports = append(cports, cp)
	}
	if len(cports) == 0 {
		// If image looks like code-server, expose only HTTP 8080 by default.
		img := strings.ToLower(strings.TrimSpace(spec.Image))
		if strings.Contains(img, "codercom/code-server") || strings.Contains(img, "ghcr.io/coder/code-server") || strings.Contains(img, "code-server") {
			cports = append(cports, corev1.ContainerPort{Name: "http", ContainerPort: 8080})
		} else {
			// Default to both 8080 (http) and 8443 (https). Readiness will target 8080.
			cports = append(cports,
				corev1.ContainerPort{Name: "http", ContainerPort: 8080},
				corev1.ContainerPort{Name: "https", ContainerPort: 8443},
			)
		}
	}

	// env
	env := []corev1.EnvVar{}
	if spec.Env == nil {
		spec.Env = map[string]string{}
	}
	// Ensure PORT=8080 by default
	if strings.TrimSpace(spec.Env["PORT"]) == "" {
		spec.Env["PORT"] = "8080"
	}
	// Ensure PASSWORD for code-server; default fallback if not provided in spec
	if strings.TrimSpace(spec.Env["PASSWORD"]) == "" {
		spec.Env["PASSWORD"] = "changeme"
	}
	for k, v := range spec.Env {
		env = append(env, corev1.EnvVar{Name: k, Value: v})
	}
	sort.Slice(env, func(i, j int) bool { return env[i].Name < env[j].Name })

	// Deployment
	replicas := int32(1)
	// Optional imagePullSecret name
	imgPullSecret := strings.TrimSpace(opt.ImagePullSecret)
	// Do not inject a default image; the API layer validates image is provided.

	// Add explicit args for code-server so it binds correctly under our reverse proxy base path.
	var containerArgs []string
	imgLower := strings.ToLower(strings.TrimSpace(spec.Image))
	if strings.Contains(imgLower, "codercom/code-server") || strings.Contains(imgLower, "code-server") {
		// Older images may not support --base-path; omit it and rely on proxy rewrites.
		containerArgs = []string{"--bind-addr", "0.0.0.0:8080", "--auth", "password"}
	}

	// Security context: default strict; relax for code-server (fixuid needs no_new_privs disabled)
	secCtx := &corev1.SecurityContext{
		AllowPrivilegeEscalation: func() *bool { b := false; return &b }(),
		RunAsNonRoot:             func() *bool { b := true; return &b }(),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
	if strings.Contains(imgLower, "codercom/code-server") || strings.Contains(imgLower, "code-server") {
		secCtx = &corev1.SecurityContext{
			AllowPrivilegeEscalation: func() *bool { b := true; return &b }(),
			RunAsNonRoot:             func() *bool { b := true; return &b }(),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		}
	}

	// pick probe port: first declared container port or 8080
	probePort := int32(8080)
	if len(cports) > 0 {
		probePort = cports[0].ContainerPort
	}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Tolerations: []corev1.Toleration{
						{Key: "node-role.kubernetes.io/control-plane", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
						{Key: "node-role.kubernetes.io/master", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
					},
					ImagePullSecrets: func() []corev1.LocalObjectReference {
						if imgPullSecret == "" {
							return nil
						}
						return []corev1.LocalObjectReference{{Name: imgPullSecret}}
					}(),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser:  func() *int64 { v := int64(1000); return &v }(),
						RunAsGroup: func() *int64 { v := int64(1000); return &v }(),
						FSGroup:    func() *int64 { v := int64(1000); return &v }(),
					},
					Containers: []corev1.Container{{
						Name:            "app",
						Image:           spec.Image,
						Args:            append(containerArgs, spec.Args...),
						Env:             env,
						Ports:           cports,
						ReadinessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstr.FromInt(int(probePort))}}},
						LivenessProbe:   &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstr.FromInt(int(probePort))}}},
						SecurityContext: secCtx,
					}},
				},
			},
		},
	}

	// Create or update
	if _, err := c.K.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
		if _, err := c.K.AppsV1().Deployments(ns).Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
			return "", "", err
		}
	} else {
		if _, err := c.K.AppsV1().Deployments(ns).Create(ctx, dep, metav1.CreateOptions{}); err != nil {
			return "", "", err
		}
	}

	// Service ports
	sports := []corev1.ServicePort{}
	if len(spec.Expose) == 0 && len(cports) == 0 {
		// Should not happen due to defaults above, but keep a sane default.
		sports = append(sports, corev1.ServicePort{Name: "http", Port: 8080, TargetPort: intstr.FromInt(8080)})
	} else {
		for _, p := range spec.Expose {
			if p.Port <= 0 {
				continue
			}
			nm := p.Name
			if nm == "" {
				nm = fmt.Sprintf("p-%d", p.Port)
			}
			sports = append(sports, corev1.ServicePort{Name: nm, Port: int32(p.Port), TargetPort: intstr.FromInt(p.Port)})
		}
		if len(sports) == 0 {
			// Mirror container ports (both 8080 and 8443 by default)
			for _, cp := range cports {
				sports = append(sports, corev1.ServicePort{Name: cp.Name, Port: cp.ContainerPort, TargetPort: intstr.FromInt(int(cp.ContainerPort))})
			}
		}
	}

	// Service type: default ClusterIP; when WORKSPACE_LB is set, expose as LoadBalancer (MetalLB expected)
	svcType := corev1.ServiceTypeClusterIP
	if opt.LBEnabled {
		svcType = corev1.ServiceTypeLoadBalancer
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    labels,
			Annotations: func() map[string]string {
				m := map[string]string{}
				if opt.LBPool != "" {
					// MetalLB pool selection
					m["metallb.universe.tf/address-pool"] = opt.LBPool
				}
				return m
			}(),
		},
		Spec: corev1.ServiceSpec{
			Selector:                 map[string]string{"app": name},
			Ports:                    sports,
			PublishNotReadyAddresses: true,
			Type:                     svcType,
		},
	}
	if _, err := c.K.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
		if _, err := c.K.CoreV1().Services(ns).Update(ctx, svc, metav1.UpdateOptions{}); err != nil {
			return "", "", err
		}
	} else {
		if _, err := c.K.CoreV1().Services(ns).Create(ctx, svc, metav1.CreateOptions{}); err != nil {
			return "", "", err
		}
	}
	// Optionally ensure an Ingress per workspace if domain is configured and LoadBalancer exposure is not requested
	if strings.TrimSpace(opt.IngressDomain) != "" && !opt.LBEnabled {
		dom := strings.TrimSpace(opt.IngressDomain)
		host := fmt.Sprintf("%s.%s", id, dom)
		tlsSec := strings.TrimSpace(opt.IngressTLSSecret)
		iclass := strings.TrimSpace(opt.IngressClassName)
		anns := map[string]string{
			"nginx.ingress.kubernetes.io/enable-websocket":   "true",
			"nginx.ingress.kubernetes.io/proxy-read-timeout": "3600",
			"nginx.ingress.kubernetes.io/proxy-send-timeout": "3600",
			"nginx.ingress.kubernetes.io/backend-protocol":   "HTTP",
		}
		// If a cert-manager issuer is provided, request a per-host cert
		if iss := strings.TrimSpace(opt.CertManagerIssuer); iss != "" && tlsSec == "" {
			anns["cert-manager.io/cluster-issuer"] = iss
			tlsSec = fmt.Sprintf("workspace-%s-tls", id)
		}
		if v := strings.TrimSpace(opt.IngressAuthURL); v != "" {
			anns["nginx.ingress.kubernetes.io/auth-url"] = v
		}
		if v := strings.TrimSpace(opt.IngressAuthSignin); v != "" {
			anns["nginx.ingress.kubernetes.io/auth-signin"] = v
		}
		// OwnerRef to the Deployment
		owner := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: name, UID: ""}
		_ = owner
		// We can’t easily get UID without a get; do a best-effort without owner for MVP
		if err := c.EnsureIngress(ctx, ns, name, host, name, 8080, tlsSec, anns, iclass, metav1.OwnerReference{}); err != nil {
			return name, id, err
		}
	}
	return name, id, nil
}

// DeleteManaged deletes Deployments and Services labeled with guildnet.io/managed=true in the given namespace.
func (c *Client) DeleteManaged(ctx context.Context, ns string) error {
	if ns == "" {
		ns = "default"
	}
	sel := metav1.ListOptions{LabelSelector: "guildnet.io/managed=true"}
	// Delete Deployments
	if deps, err := c.K.AppsV1().Deployments(ns).List(ctx, sel); err == nil {
		for _, d := range deps.Items {
			_ = c.K.AppsV1().Deployments(ns).Delete(ctx, d.Name, metav1.DeleteOptions{})
		}
	}
	// Delete Services
	if svcs, err := c.K.CoreV1().Services(ns).List(ctx, sel); err == nil {
		for _, s := range svcs.Items {
			_ = c.K.CoreV1().Services(ns).Delete(ctx, s.Name, metav1.DeleteOptions{})
		}
	}
	return nil
}

// EnsureIngress creates/updates an Ingress mapping host -> service:port with optional TLS and annotations.
func (c *Client) EnsureIngress(ctx context.Context, ns, name, host, service string, port int32, tlsSecret string, annotations map[string]string, ingressClass string, owner metav1.OwnerReference) error {
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: annotations,
			Labels:      map[string]string{"app": name, "guildnet.io/managed": "true"},
			OwnerReferences: func() []metav1.OwnerReference {
				if owner.Name == "" {
					return nil
				}
				ow := owner
				ow.Controller = func() *bool { b := true; return &b }()
				ow.BlockOwnerDeletion = func() *bool { b := true; return &b }()
				return []metav1.OwnerReference{ow}
			}(),
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: func() *string {
				if ingressClass == "" {
					return nil
				}
				v := ingressClass
				return &v
			}(),
			Rules: []networkingv1.IngressRule{{
				Host: host,
				IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{
					Path: "/",
				}}}},
			}},
		},
	}
	// Build backend and TLS separately to keep code readable
	pt := networkingv1.PathTypePrefix
	ing.Spec.Rules[0].HTTP.Paths[0].PathType = &pt
	ing.Spec.Rules[0].HTTP.Paths[0].Backend = networkingv1.IngressBackend{
		Service: &networkingv1.IngressServiceBackend{
			Name: service,
			Port: networkingv1.ServiceBackendPort{Number: port},
		},
	}
	if tlsSecret != "" {
		ing.Spec.TLS = []networkingv1.IngressTLS{{
			Hosts:      []string{host},
			SecretName: tlsSecret,
		}}
	}

	if _, err := c.K.NetworkingV1().Ingresses(ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
		_, err = c.K.NetworkingV1().Ingresses(ns).Update(ctx, ing, metav1.UpdateOptions{})
		return err
	}
	_, err := c.K.NetworkingV1().Ingresses(ns).Create(ctx, ing, metav1.CreateOptions{})
	return err
}

// ListServers returns Deployments managed by GuildNet mapped into model.Server.
func (c *Client) ListServers(ctx context.Context, ns string) ([]*model.Server, error) {
	if ns == "" {
		ns = "default"
	}
	depList, err := c.K.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{LabelSelector: "guildnet.io/managed=true"})
	if err != nil {
		return nil, err
	}
	out := []*model.Server{}
	for _, d := range depList.Items {
		id := d.Labels["guildnet.io/id"]
		if id == "" {
			id = d.Name
		}
		status := "pending"
		if d.Status.ReadyReplicas > 0 {
			status = "running"
		}
		ports := []model.Port{}
		// try service ports
		svc, _ := c.K.CoreV1().Services(ns).Get(ctx, d.Name, metav1.GetOptions{})
		if svc != nil {
			for _, sp := range svc.Spec.Ports {
				ports = append(ports, model.Port{Name: sp.Name, Port: int(sp.Port)})
			}
		}
		env := map[string]string{}
		if len(d.Spec.Template.Spec.Containers) > 0 {
			for _, e := range d.Spec.Template.Spec.Containers[0].Env {
				env[e.Name] = e.Value
			}
		}
		s := &model.Server{
			ID:     id,
			Name:   d.Name,
			Image:  firstImage(d),
			Status: status,
			Ports:  ports,
			Env:    env,
		}
		// Prefer LoadBalancer IP if assigned (MetalLB), else domain URL
		if svc != nil && svc.Status.LoadBalancer.Ingress != nil && len(svc.Status.LoadBalancer.Ingress) > 0 {
			ip := svc.Status.LoadBalancer.Ingress[0].IP
			if ip == "" {
				ip = svc.Status.LoadBalancer.Ingress[0].Hostname
			}
			// Pick a port, preferring HTTPS (8443/443), then 8080, then first
			p := 0
			if len(svc.Spec.Ports) > 0 {
				for _, sp := range svc.Spec.Ports {
					if sp.Port == 8443 || sp.Port == 443 {
						p = int(sp.Port)
						break
					}
				}
				if p == 0 {
					for _, sp := range svc.Spec.Ports {
						if sp.Port == 8080 {
							p = int(sp.Port)
							break
						}
					}
				}
				if p == 0 {
					p = int(svc.Spec.Ports[0].Port)
				}
			} else if len(d.Spec.Template.Spec.Containers) > 0 && len(d.Spec.Template.Spec.Containers[0].Ports) > 0 {
				p = int(d.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort)
			} else {
				p = 8080
			}
			if ip != "" {
				scheme := "http"
				if p == 8443 || p == 443 {
					scheme = "https"
				}
				s.URL = fmt.Sprintf("%s://%s:%d/", scheme, ip, p)
			}
		} else if true { // do not depend on WORKSPACE_DOMAIN env; prefer ingress discovery only
			// leave URL empty when not LB; UI constructs proxy URL
		}
		out = append(out, s)
	}
	return out, nil
}

func firstImage(d appsv1.Deployment) string {
	if len(d.Spec.Template.Spec.Containers) > 0 {
		return d.Spec.Template.Spec.Containers[0].Image
	}
	return ""
}

// ResolveServiceAddress returns host and port candidates for a given id/name.
func (c *Client) ResolveServiceAddress(ctx context.Context, ns, idOrName string) (host string, port int, https bool, err error) {
	if ns == "" {
		ns = "default"
	}
	// prefer by name; fallback by label selection
	svc, err1 := c.K.CoreV1().Services(ns).Get(ctx, idOrName, metav1.GetOptions{})
	if err1 != nil {
		// try by label selector guildnet.io/id
		list, err2 := c.K.CoreV1().Services(ns).List(ctx, metav1.ListOptions{LabelSelector: fmt.Sprintf("guildnet.io/id=%s", idOrName)})
		if err2 != nil || len(list.Items) == 0 {
			return "", 0, false, fmt.Errorf("service not found for %s", idOrName)
		}
		svc = &list.Items[0]
	}
	if svc.Spec.ClusterIP == "" || svc.Spec.ClusterIP == "None" {
		return "", 0, false, fmt.Errorf("service has no clusterIP")
	}
	// choose port: prefer HTTPS (8443/443), else 8080, else first
	var p int32
	https = false
	for _, sp := range svc.Spec.Ports {
		if sp.Port == 8443 || sp.Port == 443 {
			p = sp.Port
			https = true
			break
		}
	}
	if p == 0 {
		for _, sp := range svc.Spec.Ports {
			if sp.Port == 8080 {
				p = sp.Port
				https = false
				break
			}
		}
	}
	if p == 0 && len(svc.Spec.Ports) > 0 {
		p = svc.Spec.Ports[0].Port
	}
	if p == 0 {
		return "", 0, false, fmt.Errorf("service has no ports")
	}
	return svc.Spec.ClusterIP, int(p), https, nil
}

// GetServer returns a single server by id or name.
func (c *Client) GetServer(ctx context.Context, ns, idOrName string) (*model.Server, error) {
	list, err := c.ListServers(ctx, ns)
	if err != nil {
		return nil, err
	}
	for _, s := range list {
		if s.ID == idOrName || s.Name == idOrName {
			return s, nil
		}
	}
	return nil, fmt.Errorf("not found")
}

// GetLogs tails recent container logs from the first pod of the server's Deployment.
func (c *Client) GetLogs(ctx context.Context, ns, idOrName, level string, limit int) ([]model.LogLine, error) {
	if ns == "" {
		ns = "default"
	}
	// find deployment by name or label
	var dep *appsv1.Deployment
	if d, err := c.K.AppsV1().Deployments(ns).Get(ctx, idOrName, metav1.GetOptions{}); err == nil {
		dep = d
	} else {
		lst, err2 := c.K.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{LabelSelector: fmt.Sprintf("guildnet.io/id=%s", idOrName)})
		if err2 != nil || len(lst.Items) == 0 {
			return nil, fmt.Errorf("deployment not found")
		}
		dd := lst.Items[0]
		dep = &dd
	}
	if dep == nil {
		return nil, fmt.Errorf("deployment not found")
	}
	// list pods
	pods, err := c.K.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: fmt.Sprintf("app=%s", dep.Name)})
	if err != nil || len(pods.Items) == 0 {
		return nil, fmt.Errorf("no pods")
	}
	pod := pods.Items[0]
	container := ""
	if len(pod.Spec.Containers) > 0 {
		container = pod.Spec.Containers[0].Name
	}
	// fetch logs
	tail := int64(limit)
	req := c.K.CoreV1().Pods(ns).GetLogs(pod.Name, &corev1.PodLogOptions{Container: container, TailLines: &tail})
	data, err := req.Do(ctx).Raw()
	if err != nil {
		return nil, err
	}
	// split by lines
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	out := make([]model.LogLine, 0, len(lines))
	for _, ln := range lines {
		out = append(out, model.LogLine{T: model.NowISO(), LVL: level, MSG: ln})
	}
	return out, nil
}
