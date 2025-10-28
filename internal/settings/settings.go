package settings

import (
	"fmt"
	"os"
	"strings"

	"github.com/your/module/internal/localdb"
	"github.com/your/module/internal/secrets"
)

// Tailscale holds tsnet control-plane settings managed at runtime.
type Tailscale struct {
	LoginServer string `json:"login_server"`
	PreauthKey  string `json:"preauth_key"`
	Hostname    string `json:"hostname"`
}

// Database holds DB connection settings.
type Database struct {
	Addr string `json:"addr"`
	User string `json:"user"`
	Pass string `json:"pass"`
}

// Global holds global runtime settings not tied to a specific cluster.
// Example: default Org ID for new nodes, UI CORS origin overrides, etc.
type Global struct {
	OrgID            string `json:"org_id"`
	FrontendOrigin   string `json:"frontend_origin,omitempty"`
	EmbedOperator    bool   `json:"embed_operator,omitempty"`
	DefaultNamespace string `json:"default_namespace,omitempty"`
	ListenLocal      string `json:"listen_local,omitempty"`
}

// Cluster holds per-cluster runtime settings that affect connectivity and proxying.
// These should be set dynamically via the UI and stored in localdb so any node can join using a join file.
type Cluster struct {
	// Optional human label (not used as key)
	Name string `json:"name,omitempty"`

	// Kubernetes namespace to operate in (default "default")
	Namespace string `json:"namespace,omitempty"`

	// API proxy base URL to reach the cluster from this host. When set, overrides kubeconfig host.
	APIProxyURL string `json:"api_proxy_url,omitempty"`
	// Force HTTP even if kubeconfig suggests HTTPS (useful for kubectl proxy or local dev)
	APIProxyForceHTTP bool `json:"api_proxy_force_http,omitempty"`
	// Disable API proxy rewriting entirely for this cluster
	DisableAPIProxy bool `json:"disable_api_proxy,omitempty"`

	// Proxy style preferences for user workloads
	PreferPodProxy bool `json:"prefer_pod_proxy,omitempty"`
	UsePortForward bool `json:"use_port_forward,omitempty"`

	// Ingress and domain knobs (optional; used when creating ingress resources)
	IngressDomain      string `json:"ingress_domain,omitempty"`
	IngressClassName   string `json:"ingress_class_name,omitempty"`
	WorkspaceTLSSecret string `json:"workspace_tls_secret,omitempty"`
	CertManagerIssuer  string `json:"cert_manager_issuer,omitempty"`
	IngressAuthURL     string `json:"ingress_auth_url,omitempty"`
	IngressAuthSignin  string `json:"ingress_auth_signin,omitempty"`
	ImagePullSecret    string `json:"image_pull_secret,omitempty"`

	// Default to expose workspaces as LoadBalancer when not specified per-workspace
	WorkspaceLBEnabled bool `json:"workspace_lb_enabled,omitempty"`

	// Optional org scope if multi-tenant DB is used per cluster scope
	OrgID string `json:"org_id,omitempty"`

	// Tailscale per-cluster connector (plain K8S multi-tailnet)
	TSLoginServer   string `json:"ts_login_server,omitempty"`
	TSClientAuthKey string `json:"-"` // never echo back
	TSRoutes        string `json:"ts_routes,omitempty"`
	TSStatePath     string `json:"ts_state_path,omitempty"`
	HeadscaleNS     string `json:"headscale_namespace,omitempty"`
}

// Manager wraps localdb for typed settings.
type Manager struct{ DB *localdb.DB }

const (
	bucket         = "settings"
	bucketClusters = "cluster-settings"
	keyTS          = "tailscale"
	keyDB          = "database"
	keyGlobal      = "global"
)

func EnsureBucket(db *localdb.DB) error { return db.EnsureBuckets(bucket, bucketClusters) }

func (m Manager) GetTailscale(out *Tailscale) error {
	var tmp map[string]any
	if err := m.DB.Get(bucket, keyTS, &tmp); err != nil {
		*out = Tailscale{}
		return nil
	}
	out.LoginServer = strings.TrimSpace(asString(tmp["login_server"]))
	out.PreauthKey = strings.TrimSpace(asString(tmp["preauth_key"]))
	out.Hostname = strings.TrimSpace(asString(tmp["hostname"]))
	return nil
}

func (m Manager) PutTailscale(ts Tailscale) error {
	rec := map[string]any{
		"login_server": strings.TrimSpace(ts.LoginServer),
		"preauth_key":  strings.TrimSpace(ts.PreauthKey),
		"hostname":     strings.TrimSpace(ts.Hostname),
	}
	return m.DB.Put(bucket, keyTS, rec)
}

func (m Manager) GetDatabase(out *Database) error {
	var tmp map[string]any
	if err := m.DB.Get(bucket, keyDB, &tmp); err != nil {
		*out = Database{}
		return nil
	}
	out.Addr = strings.TrimSpace(asString(tmp["addr"]))
	out.User = strings.TrimSpace(asString(tmp["user"]))
	out.Pass = strings.TrimSpace(asString(tmp["pass"]))
	return nil
}

func (m Manager) PutDatabase(db Database) error {
	rec := map[string]any{
		"addr": strings.TrimSpace(db.Addr),
		"user": strings.TrimSpace(db.User),
		"pass": strings.TrimSpace(db.Pass),
	}
	return m.DB.Put(bucket, keyDB, rec)
}

// Global settings CRUD
func (m Manager) GetGlobal(out *Global) error {
	var tmp map[string]any
	if err := m.DB.Get(bucket, keyGlobal, &tmp); err != nil {
		*out = Global{}
		return nil
	}
	out.OrgID = strings.TrimSpace(asString(tmp["org_id"]))
	out.FrontendOrigin = strings.TrimSpace(asString(tmp["frontend_origin"]))
	out.EmbedOperator = asBool(tmp["embed_operator"])
	out.DefaultNamespace = strings.TrimSpace(asString(tmp["default_namespace"]))
	out.ListenLocal = strings.TrimSpace(asString(tmp["listen_local"]))
	if out.ListenLocal == "" {
		out.ListenLocal = "127.0.0.1:8090"
	}
	return nil
}

func (m Manager) PutGlobal(g Global) error {
	rec := map[string]any{
		"org_id":            strings.TrimSpace(g.OrgID),
		"frontend_origin":   strings.TrimSpace(g.FrontendOrigin),
		"embed_operator":    g.EmbedOperator,
		"default_namespace": strings.TrimSpace(g.DefaultNamespace),
		"listen_local":      strings.TrimSpace(g.ListenLocal),
	}
	return m.DB.Put(bucket, keyGlobal, rec)
}

// Per-cluster settings CRUD. Keys are cluster IDs.
func (m Manager) GetCluster(clusterID string, out *Cluster) error {
	if strings.TrimSpace(clusterID) == "" {
		return fmt.Errorf("cluster id required")
	}
	var tmp map[string]any
	if err := m.DB.Get(bucketClusters, clusterID, &tmp); err != nil {
		*out = Cluster{}
		return nil
	}
	out.Name = strings.TrimSpace(asString(tmp["name"]))
	out.Namespace = strings.TrimSpace(asString(tmp["namespace"]))
	out.APIProxyURL = strings.TrimSpace(asString(tmp["api_proxy_url"]))
	out.APIProxyForceHTTP = asBool(tmp["api_proxy_force_http"])
	out.DisableAPIProxy = asBool(tmp["disable_api_proxy"])
	out.PreferPodProxy = asBool(tmp["prefer_pod_proxy"])
	out.UsePortForward = asBool(tmp["use_port_forward"])
	out.IngressDomain = strings.TrimSpace(asString(tmp["ingress_domain"]))
	out.IngressClassName = strings.TrimSpace(asString(tmp["ingress_class_name"]))
	out.WorkspaceTLSSecret = strings.TrimSpace(asString(tmp["workspace_tls_secret"]))
	out.CertManagerIssuer = strings.TrimSpace(asString(tmp["cert_manager_issuer"]))
	out.IngressAuthURL = strings.TrimSpace(asString(tmp["ingress_auth_url"]))
	out.IngressAuthSignin = strings.TrimSpace(asString(tmp["ingress_auth_signin"]))
	out.ImagePullSecret = strings.TrimSpace(asString(tmp["image_pull_secret"]))
	out.WorkspaceLBEnabled = asBool(tmp["workspace_lb_enabled"])
	out.OrgID = strings.TrimSpace(asString(tmp["org_id"]))
	// TS fields; client auth key intentionally omitted from GET
	out.TSLoginServer = strings.TrimSpace(asString(tmp["ts_login_server"]))
	out.TSRoutes = strings.TrimSpace(asString(tmp["ts_routes"]))
	out.TSStatePath = strings.TrimSpace(asString(tmp["ts_state_path"]))
	out.HeadscaleNS = strings.TrimSpace(asString(tmp["headscale_namespace"]))
	return nil
}

func (m Manager) PutCluster(clusterID string, cs Cluster) error {
	if strings.TrimSpace(clusterID) == "" {
		return fmt.Errorf("cluster id required")
	}
	rec := map[string]any{
		"name":                 strings.TrimSpace(cs.Name),
		"namespace":            strings.TrimSpace(cs.Namespace),
		"api_proxy_url":        strings.TrimSpace(cs.APIProxyURL),
		"api_proxy_force_http": cs.APIProxyForceHTTP,
		"disable_api_proxy":    cs.DisableAPIProxy,
		"prefer_pod_proxy":     cs.PreferPodProxy,
		"use_port_forward":     cs.UsePortForward,
		"ingress_domain":       strings.TrimSpace(cs.IngressDomain),
		"ingress_class_name":   strings.TrimSpace(cs.IngressClassName),
		"workspace_tls_secret": strings.TrimSpace(cs.WorkspaceTLSSecret),
		"cert_manager_issuer":  strings.TrimSpace(cs.CertManagerIssuer),
		"ingress_auth_url":     strings.TrimSpace(cs.IngressAuthURL),
		"ingress_auth_signin":  strings.TrimSpace(cs.IngressAuthSignin),
		"image_pull_secret":    strings.TrimSpace(cs.ImagePullSecret),
		"workspace_lb_enabled": cs.WorkspaceLBEnabled,
		"org_id":               strings.TrimSpace(cs.OrgID),
		"ts_login_server":      strings.TrimSpace(cs.TSLoginServer),
		"ts_routes":            strings.TrimSpace(cs.TSRoutes),
		"ts_state_path":        strings.TrimSpace(cs.TSStatePath),
		"headscale_namespace":  strings.TrimSpace(cs.HeadscaleNS),
	}
	// Store client auth key in credentials bucket to avoid accidental echo
	if strings.TrimSpace(cs.TSClientAuthKey) != "" && m.DB != nil {
		// Use the hostapp master key (if present) to encrypt stored secrets.
		// This uses the same envelope mechanism as the rest of the app via
		// internal/secrets.Manager. If no master key is configured, store
		// the plaintext value with encrypted=false for backwards compat.
		enc := false
		val := cs.TSClientAuthKey
		if mk := strings.TrimSpace(os.Getenv("GUILDNET_MASTER_KEY")); mk != "" {
			if sec, err := secrets.New(mk); err == nil {
				if e, err2 := sec.Encrypt(cs.TSClientAuthKey); err2 == nil {
					val = e
					enc = true
				}
			}
		}
		_ = m.DB.Put("credentials", fmt.Sprintf("cl:%s:ts_client_auth", clusterID), map[string]any{"value": val, "encrypted": enc})
	}
	return m.DB.Put(bucketClusters, clusterID, rec)
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return ""
	}
}

func asBool(v any) bool {
	if v == nil {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(strings.TrimSpace(t), "true") || strings.TrimSpace(t) == "1"
	case float64:
		return t != 0
	default:
		return false
	}
}
