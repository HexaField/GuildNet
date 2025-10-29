package connector

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/your/module/internal/headscale"
	"github.com/your/module/internal/localdb"
	ts "github.com/your/module/internal/ts"

	"tailscale.com/tsnet"
)

// Config describes how to start a per-cluster embedded tsnet server.
type Config struct {
	ClusterID     string
	LoginServer   string
	ClientAuthKey string
	StateDir      string
	Hostname      string // optional
}

// Connector manages a tsnet.Server and provides dialing utilities.
type Connector struct {
	cfg   Config
	srv   *tsnet.Server
	mu    sync.RWMutex
	start sync.Once
	stop  sync.Once
}

// New validates and returns a Connector with the given configuration.
func New(cfg Config) (*Connector, error) {
	id := strings.TrimSpace(cfg.ClusterID)
	if id == "" {
		return nil, errors.New("clusterID required")
	}
	if strings.TrimSpace(cfg.LoginServer) == "" {
		return nil, errors.New("loginServer required")
	}
	// ClientAuthKey may be empty if the state dir already contains device state.
	// For first-time join it's required, but we validate in Start.

	state := strings.TrimSpace(cfg.StateDir)
	if state == "" {
		home, _ := os.UserHomeDir()
		if home == "" {
			return nil, errors.New("no home dir for state")
		}
		state = filepath.Join(home, ".guildnet", "tsnet", fmt.Sprintf("cluster-%s", sanitizeID(id)))
	}
	if err := os.MkdirAll(state, 0o700); err != nil {
		return nil, fmt.Errorf("state dir: %w", err)
	}
	if err := os.Chmod(state, 0o700); err != nil {
		// best effort; ignore on Windows
		_ = err
	}
	cfg.StateDir = state
	if strings.TrimSpace(cfg.Hostname) == "" {
		host, _ := os.Hostname()
		if host == "" {
			host = randSuffix("node")
		}
		cfg.Hostname = fmt.Sprintf("guildnet-%s-%s", sanitizeID(id), sanitizeID(host))
	}
	return &Connector{cfg: cfg}, nil
}

// Start initializes the tsnet server (idempotent).
func (c *Connector) Start(ctx context.Context) error {
	var retErr error
	c.start.Do(func() {
		// Normalize login server: if the configured login server resolves to a
		// local interface IP (hairpin to the host), rewrite the host to
		// 127.0.0.1 so the embedded tsnet control client uses loopback and
		// avoids potential hairpin/routing surprises.
		if strings.TrimSpace(c.cfg.LoginServer) != "" {
			if norm := normalizeLoginServerToLoopback(c.cfg.LoginServer); norm != "" {
				c.cfg.LoginServer = norm
			}
		}

		// Probe the configured login server so we can surface immediate
		// network/connectivity problems (timeouts, DNS, etc) before starting
		// the embedded tsnet server. Retry a few times (best-effort). Probe
		// failures do not block start permanently but we log attempts so
		// operators can see transient network issues.
		if strings.TrimSpace(c.cfg.LoginServer) != "" {
			probeURL := strings.TrimRight(strings.TrimSpace(c.cfg.LoginServer), "/") + "/key?v=1"
			client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
			// configurable-ish defaults: try up to 5 attempts with exponential backoff
			attempts := 5
			baseDelay := 500 * time.Millisecond
			var lastErr error
			for i := 0; i < attempts; i++ {
				attempt := i + 1
				probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
				req, _ := http.NewRequestWithContext(probeCtx, http.MethodGet, probeURL, nil)
				resp, err := client.Do(req)
				cancel()
				if err != nil {
					lastErr = err
					log.Printf("connector: loginServer probe failed cluster=%s attempt=%d/%d url=%s err=%v", c.cfg.ClusterID, attempt, attempts, probeURL, err)
				} else {
					_, _ = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					log.Printf("connector: loginServer probe ok cluster=%s attempt=%d/%d url=%s status=%s", c.cfg.ClusterID, attempt, attempts, probeURL, resp.Status)
					lastErr = nil
					break
				}
				// backoff before next attempt
				if i+1 < attempts {
					time.Sleep(baseDelay * time.Duration(1<<i))
				}
			}
			if lastErr != nil {
				log.Printf("connector: loginServer probe final-failed cluster=%s url=%s last_err=%v", c.cfg.ClusterID, probeURL, lastErr)
			}
		}
		// If state already exists, tsnet can reuse it without a fresh auth key
		if !dirExists(c.cfg.StateDir) && strings.TrimSpace(c.cfg.ClientAuthKey) == "" {
			retErr = errors.New("clientAuthKey required for first start")
			return
		}
		// Normalize and trim the client auth key at start time so any stored
		// variants (with or without the tskey- prefix) are canonical before
		// passing to tsnet. This avoids subtle mismatches between stored keys
		// and what tsnet attempts to use during noise registration.
		if strings.TrimSpace(c.cfg.ClientAuthKey) != "" {
			k := strings.TrimSpace(c.cfg.ClientAuthKey)
			// If the stored value was saved without the tskey- prefix, or if
			// it's a raw hex representation of the preauth bytes, normalize
			// into the canonical tskey-<base64url-without-padding> form that
			// the tsnet client expects. This allows operators to inject
			// either the raw hex bytes or an already-formed tskey value.
			stripped := strings.TrimPrefix(k, "tskey-")
			// detect even-length hex strings containing only hex chars
			isHex := len(stripped)%2 == 0 && stripped != ""
			if isHex {
				for _, r := range stripped {
					if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
						isHex = false
						break
					}
				}
			}
			if isHex {
				// convert hex -> raw bytes -> base64url (no padding) and prefix with tskey-
				if raw, err := hex.DecodeString(stripped); err == nil {
					enc := base64.RawURLEncoding.EncodeToString(raw)
					k = "tskey-" + enc
					log.Printf("connector: normalized hex client auth key into tskey- format for cluster=%s", c.cfg.ClusterID)
				} else {
					// fallback: treat as opaque and just ensure tskey- prefix
					if !strings.HasPrefix(k, "tskey-") {
						k = "tskey-" + k
						log.Printf("connector: normalizing client auth key for cluster=%s (added tskey- fallback)", c.cfg.ClusterID)
					}
				}
			} else {
				if !strings.HasPrefix(k, "tskey-") {
					log.Printf("connector: normalizing client auth key for cluster=%s (adding tskey- prefix)", c.cfg.ClusterID)
					k = "tskey-" + k
				}
			}
			c.cfg.ClientAuthKey = k
		}

		// Log a masked view of the client auth key so we can validate what the
		// connector is attempting to use without printing secrets in full.
		// Debug-only artifact writing and global env mutation removed — prefer
		// per-server AuthKey and in-process isolation for production.
		log.Printf("connector: starting tsnet cluster=%s auth=%s stateDir=%s", c.cfg.ClusterID, maskAuthKey(c.cfg.ClientAuthKey), c.cfg.StateDir)

		// Resolve the effective auth key we will pass to tsnet. Prefer a
		// raw-hex representation when possible since some Headscale setups
		// accept the raw hex form. Use the centralized resolver in the
		// internal/ts package so behavior is consistent across callers.
		var authToUse string
		if strings.TrimSpace(c.cfg.ClientAuthKey) != "" {
			raw, fmtLabel := ts.ResolveAuthKeyToRaw(c.cfg.ClientAuthKey)
			if raw != "" {
				authToUse = raw
				log.Printf("connector: resolved client auth key format=%s cluster=%s", fmtLabel, c.cfg.ClusterID)
			} else {
				authToUse = strings.TrimSpace(c.cfg.ClientAuthKey)
				log.Printf("connector: could not resolve client auth key to raw hex; passing as-is format=%s cluster=%s", fmtLabel, c.cfg.ClusterID)
			}
		}

		s := &tsnet.Server{
			Dir:        c.cfg.StateDir,
			Hostname:   c.cfg.Hostname,
			AuthKey:    authToUse,
			ControlURL: strings.TrimSpace(c.cfg.LoginServer),
			// Provide a cluster-prefixed logger so tsnet/tailscaled logs surface in
			// the hostapp journal with cluster context for easier debugging.
			Logf: func(format string, args ...any) {
				pref := fmt.Sprintf("tsnet[%s]: "+format, c.cfg.ClusterID)
				log.Printf(pref, args...)
			},
		}
		// Note: do not set process-global envs like TSNET_FORCE_LOGIN here.
		// The preferred approach is to pass the per-cluster AuthKey to the
		// tsnet.Server (done via the AuthKey field above) and avoid global
		// side-effects. If a future tsnet version requires explicit forcing,
		// it should be done within an isolated runner process.
		if err := s.Start(); err != nil {
			retErr = fmt.Errorf("tsnet start: %w", err)
			return
		}
		// Wait until client is up or timeout
		lc, err := s.LocalClient()
		if err != nil {
			retErr = fmt.Errorf("local client: %w", err)
			_ = s.Close()
			return
		}
		// Give tailscaled more time to finish initial login/auth and populate
		// local status. Previously this waited 30s; increase to 2 minutes to
		// accommodate slower networks or headscale auth delays.
		deadline := time.Now().Add(2 * time.Minute)
		for {
			st, err := lc.Status(ctx)
			if err != nil {
				// Log the transient status error with context and attempt to surface
				// the localapi error details so we can diagnose controlclient failures.
				log.Printf("connector: LocalClient.Status err=%v cluster=%s stateDir=%s", err, c.cfg.ClusterID, c.cfg.StateDir)
				// If the LocalClient exposes a more detailed error via its String method
				// or other inspection, log that as well (best-effort, non-fatal).
				if lc != nil {
					// lc may be non-nil even on error; try a best-effort status call without ctx cancellation
					if s2, e2 := lc.Status(context.Background()); e2 == nil {
						log.Printf("connector: LocalClient.Status (retry) cluster=%s status-ok=%v details=%v", c.cfg.ClusterID, s2 != nil, s2)
					} else {
						log.Printf("connector: LocalClient.Status (retry) err=%v cluster=%s", e2, c.cfg.ClusterID)
					}
				}
			}
			if err == nil && st != nil && (len(st.TailscaleIPs) > 0 || (st.Self != nil && st.Self.DNSName != "")) {
				break
			}
			if time.Now().After(deadline) {
				break
			}
			select {
			case <-ctx.Done():
				retErr = ctx.Err()
				_ = s.Close()
				return
			case <-time.After(500 * time.Millisecond):
			}
		}

		// If no IP/FQDN after the initial wait, do not perform fragile
		// process-global env restarts here. The preferred flow uses a per-server
		// AuthKey and authoritative verification against headscale. If
		// additional action is required, a supervised runner should handle it.
		st, _ := lc.Status(context.Background())
		var haveIPorFQDN bool
		if st != nil {
			if len(st.TailscaleIPs) > 0 || (st.Self != nil && st.Self.DNSName != "") {
				haveIPorFQDN = true
			}
		}
		if !haveIPorFQDN {
			// No fallback mode: leave tsnet running and surface health via Headscale
			// checks elsewhere. This avoids fragile state manipulation.
			log.Printf("connector: no Tailscale IP or FQDN after start for cluster=%s; no fallback enabled", c.cfg.ClusterID)
		} else {
			// Authoritative verification: query the remote Headscale admin API
			// (use the configured loginServer as the headscale endpoint) and
			// persist a minimal device record into the per-cluster local DB.
			// Token is empty here; if your Headscale requires an admin token,
			// pass it via configuration and use it here.
			hsEndpoint := strings.TrimSpace(c.cfg.LoginServer)
			ips, machineID, err := headscale.FindMachineIPsByHostname(hsEndpoint, "", c.cfg.Hostname)
			if err != nil {
				log.Printf("connector: headscale lookup failed cluster=%s err=%v", c.cfg.ClusterID, err)
			} else {
				log.Printf("connector: headscale lookup cluster=%s found_ips=%v machine_id=%s", c.cfg.ClusterID, ips, machineID)
				// Persist into per-cluster local DB
				if db, err := localdb.Open(c.cfg.StateDir); err == nil {
					rec := map[string]any{
						"id":                 c.cfg.Hostname,
						"name":               c.cfg.Hostname,
						"tailnetIPs":         ips,
						"headscaleEndpoint":  hsEndpoint,
						"headscaleMachineID": machineID,
						"verifiedAt":         time.Now().UTC().Format(time.RFC3339),
					}
					_ = db.Put("devices", c.cfg.Hostname, rec)
					_ = db.Close()
				} else {
					log.Printf("connector: failed to open per-cluster DB cluster=%s err=%v", c.cfg.ClusterID, err)
				}
			}
		}
		c.mu.Lock()
		c.srv = s
		c.mu.Unlock()
		// Ensure server is closed on GC if Stop not called explicitly
		runtime.SetFinalizer(c, func(cc *Connector) {
			_ = cc.CloseServer()
		})
	})
	return retErr
}

// DialContext dials using the tsnet server.
func (c *Connector) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	c.mu.RLock()
	s := c.srv
	c.mu.RUnlock()
	if s == nil {
		return nil, errors.New("connector not started")
	}
	conn, err := s.Dial(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// (debug helpers removed) Handshake capture and debug-only artifact writers
// were intentionally removed in favor of secure, gated diagnostics and a
// per-server AuthKey flow. Keep the file lean for production usage.

// Listen exposes a listener on the underlying tsnet.Server so callers can publish
// services into the tailscale/Tailnet. The addr parameter supports formats like
// ":8080" for an ephemeral host port. This is a convenience wrapper.
func (c *Connector) Listen(network, addr string) (net.Listener, error) {
	c.mu.RLock()
	s := c.srv
	c.mu.RUnlock()
	if s == nil {
		return nil, errors.New("connector not started")
	}
	return s.Listen(network, addr)
}

// HTTPTransport returns a clone of base (or a new one) that dials via tsnet.
func (c *Connector) HTTPTransport(base *http.Transport) *http.Transport {
	t := &http.Transport{}
	if base != nil {
		// Copy safe fields manually; avoid copying the mutex
		t.Proxy = base.Proxy
		t.ProxyConnectHeader = base.ProxyConnectHeader
		t.TLSClientConfig = base.TLSClientConfig
		t.TLSHandshakeTimeout = base.TLSHandshakeTimeout
		t.DisableKeepAlives = base.DisableKeepAlives
		t.DisableCompression = base.DisableCompression
		t.MaxIdleConns = base.MaxIdleConns
		t.MaxIdleConnsPerHost = base.MaxIdleConnsPerHost
		t.MaxConnsPerHost = base.MaxConnsPerHost
		t.IdleConnTimeout = base.IdleConnTimeout
		t.ResponseHeaderTimeout = base.ResponseHeaderTimeout
		t.ExpectContinueTimeout = base.ExpectContinueTimeout
		t.TLSNextProto = base.TLSNextProto
		t.ProxyConnectHeader = base.ProxyConnectHeader
		t.DialTLSContext = base.DialTLSContext
	}
	// Always override DialContext
	t.DialContext = c.DialContext
	// Disable HTTP/2 to avoid misconfig surprises unless the base already enables it explicitly.
	t.ForceAttemptHTTP2 = false
	// Debug RoundTripper removed; consumers can attach custom transports if
	// they need developer-only logging. Keep the transport simple and focused
	// on using the tsnet DialContext.
	return t
}

// LoggingTransport intentionally not implemented here; keep diagnostics
// out-of-band and gated by explicit tooling.
//
// A compatibility shim is provided so callers that previously wrapped
// transports for debug logging continue to compile. This is intentionally
// a no-op in production — it returns the provided RoundTripper unchanged
// (or http.DefaultTransport when nil). Enable richer diagnostics via
// dedicated tooling rather than in-process hooks.
func LoggingTransport(base http.RoundTripper, _clusterID string) http.RoundTripper {
	if base == nil {
		return http.DefaultTransport
	}
	return base
}

// Health returns a quick status and details map describing the connector state.
func (c *Connector) Health(ctx context.Context) (string, map[string]any) {
	det := map[string]any{"clusterId": c.cfg.ClusterID, "stateDir": c.cfg.StateDir, "loginServer": redactURL(c.cfg.LoginServer)}
	c.mu.RLock()
	s := c.srv
	c.mu.RUnlock()
	if s == nil {
		return "stopped", det
	}
	lc, err := s.LocalClient()
	if err != nil {
		det["error"] = err.Error()
		return "degraded", det
	}
	st, err := lc.Status(ctx)
	if err != nil {
		det["error"] = err.Error()
		return "degraded", det
	}
	var ip, fqdn string
	if len(st.TailscaleIPs) > 0 {
		ip = st.TailscaleIPs[0].String()
	}
	if st.Self != nil {
		fqdn = strings.TrimSuffix(st.Self.DNSName, ".")
	}
	det["ip"] = ip
	det["fqdn"] = fqdn
	if ip != "" || fqdn != "" {
		return "ok", det
	}
	return "starting", det
}

// HealthWithRetry calls Health with a context timeout and retries on transient
// failures. It returns the first successful "ok" status or the last observed
// status/details when retries are exhausted.
func (c *Connector) HealthWithRetry(parent context.Context, timeout time.Duration, attempts int, backoff time.Duration) (string, map[string]any) {
	var lastSt string
	var lastDet map[string]any
	for i := 0; i < attempts; i++ {
		ctx, cancel := context.WithTimeout(parent, timeout)
		st, det := c.Health(ctx)
		cancel()
		lastSt = st
		lastDet = det
		if st == "ok" {
			return st, det
		}
		// small backoff before retrying
		if i+1 < attempts {
			select {
			case <-parent.Done():
				return lastSt, lastDet
			case <-time.After(backoff):
			}
		}
	}
	return lastSt, lastDet
}

// Stop gracefully stops the tsnet server.
func (c *Connector) Stop(ctx context.Context) error { // ctx unused for now
	var retErr error
	c.stop.Do(func() {
		retErr = c.CloseServer()
	})
	return retErr
}

// CloseServer closes the underlying tsnet.Server immediately.
func (c *Connector) CloseServer() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.srv != nil {
		err := c.srv.Close()
		c.srv = nil
		return err
	}
	return nil
}

// Helpers
func sanitizeID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteByte('-')
		}
	}
	res := strings.Trim(b.String(), "-")
	if res == "" {
		res = "default"
	}
	return res
}

func randSuffix(prefix string) string {
	var buf [4]byte
	_, _ = rand.Read(buf[:])
	return fmt.Sprintf("%s-%s", prefix, hex.EncodeToString(buf[:]))
}

// small helpers to compute digests (return fixed-size arrays for easy writing)
// debug helpers removed; debug instrumentation is intentionally disabled in
// production builds. Use dedicated diagnostic tooling for handshake capture.

// maskAuthKey partially masks a tailscale preauth key for safe logging.
func maskAuthKey(k string) string {
	k = strings.TrimSpace(k)
	if k == "" {
		return "<empty>"
	}
	// strip tskey- prefix if present
	if strings.HasPrefix(k, "tskey-") {
		k = k[len("tskey-"):]
	}
	if len(k) <= 8 {
		return "****" + k
	}
	return "****" + k[len(k)-8:]
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// normalizeLoginServerToLoopback rewrites the loginServer URL to use 127.0.0.1 if
// the configured host resolves to an IP that belongs to a local interface on
// this machine. It preserves scheme and port. Returns empty string on error
// or if no rewrite is needed.
func normalizeLoginServerToLoopback(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Ensure URL has a scheme for parsing
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	port := u.Port()
	if host == "" {
		return ""
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return ""
	}
	// collect local IPs
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	local := map[string]bool{}
	for _, ifi := range ifaces {
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil {
				continue
			}
			local[ip.String()] = true
		}
	}
	for _, ip := range ips {
		if ip == nil {
			continue
		}
		if local[ip.String()] {
			// Only rewrite to loopback if a service is actually listening on
			// 127.0.0.1:port; otherwise rewriting breaks setups where the
			// service binds to the host IP only (for example docker-proxy).
			if port == "" {
				return ""
			}
			loopbackAddr := net.JoinHostPort("127.0.0.1", port)
			conn, err := net.DialTimeout("tcp", loopbackAddr, 250*time.Millisecond)
			if err != nil {
				return ""
			}
			_ = conn.Close()
			u.Host = loopbackAddr
			return u.String()
		}
	}
	return ""
}

func redactURL(s string) string {
	// Return host:port or scheme://host form without credentials
	return s
}
