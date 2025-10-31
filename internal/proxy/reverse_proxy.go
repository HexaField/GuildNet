package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Options struct {
	MaxBody int64
	Timeout time.Duration
	Dial    func(ctx context.Context, network, address string) (any, error)
	Logger  *log.Logger
	// ResolveServer: given a logical server ID and desired subPath, return target scheme, host:port, and normalized path
	ResolveServer func(ctx context.Context, serverID string, subPath string) (scheme string, hostport string, path string, err error)
	// Optional: APIProxy builds a RoundTripper to reach in-cluster services via the Kubernetes API server proxy.
	// When non-nil and the hostport appears to be a ClusterIP or *.svc address, this transport will be used.
	APIProxy func() (http.RoundTripper, func(req *http.Request, scheme, hostport, subPath string), bool)
}

type ReverseProxy struct {
	opts Options
}

func NewReverseProxy(opts Options) *ReverseProxy { return &ReverseProxy{opts: opts} }

func (p *ReverseProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Defensive: ensure a panic inside the reverse proxy never results in an empty TLS close to clients.
	// If any unexpected panic occurs, return a 502 with a minimal body so curl and browsers don't report
	// "Empty reply from server". This also prevents the whole server from crashing.
	defer func() {
		if rec := recover(); rec != nil {
			if p.opts.Logger != nil {
				p.opts.Logger.Printf("proxy: recovered from panic: %v", rec)
			} else {
				log.Printf("proxy: recovered from panic: %v", rec)
			}
			// Best-effort error response; if headers already sent this will be ignored by net/http.
			http.Error(w, "upstream error: proxy panic", http.StatusBadGateway)
		}
	}()
	// Attach a request id if available for correlation
	reqID := r.Header.Get("X-Request-Id")
	q := r.URL.Query()
	to := q.Get("to")
	subPath := q.Get("path")
	scheme := q.Get("scheme")

	// Also support path-based form: /proxy/{to}/<rest>
	// When path-based form is used, ignore query "to"/"path".
	var serverIDForAPI string
	if to == "" || subPath == "" {
		if strings.HasPrefix(r.URL.Path, "/proxy/") {
			suffix := strings.TrimPrefix(r.URL.Path, "/proxy/")
			// Support /proxy/server/{id}/<rest> form first
			if strings.HasPrefix(suffix, "server/") && p.opts.ResolveServer != nil {
				tail := strings.TrimPrefix(suffix, "server/")
				var id, rest string
				if i := strings.IndexByte(tail, '/'); i >= 0 {
					id, rest = tail[:i], tail[i:]
				} else {
					id, rest = tail, "/"
				}
				if idu, err := url.PathUnescape(id); err == nil {
					id = idu
				}
				if rest == "" {
					rest = "/"
				}
				// Delegate to resolver
				sch, hostport, path, err := p.opts.ResolveServer(r.Context(), id, rest)
				if err != nil {
					http.Error(w, "server resolution failed: "+err.Error(), http.StatusBadGateway)
					return
				}
				scheme = sch
				to = hostport
				subPath = path
				serverIDForAPI = id
			} else {
				// legacy path-based: /proxy/{to}/{rest}
				var rest string
				if i := strings.IndexByte(suffix, '/'); i >= 0 {
					to, rest = suffix[:i], suffix[i:]
				} else {
					to, rest = suffix, "/"
				}
				if uTo, err := url.PathUnescape(to); err == nil {
					to = uTo
				}
				if rest == "" {
					rest = "/"
				}
				subPath = rest
			}
		}
	}

	// When using server-aware form with API proxy available, skip strict host:port validation.
	skipTargetValidation := serverIDForAPI != "" && p.opts.APIProxy != nil

	if subPath == "" || !strings.HasPrefix(subPath, "/") || (to == "" && !skipTargetValidation) {
		if p.opts.Logger != nil {
			p.opts.Logger.Printf("proxy bad-request req_id=%s to=%q path=%q url=%s", reqID, to, subPath, r.URL.String())
		}
		http.Error(w, "missing or invalid to/path", http.StatusBadRequest)
		return
	}
	// validate target (unless skipping due to API proxy server mode)
	if !skipTargetValidation {
		host, ps, err := net.SplitHostPort(to)
		if err != nil {
			if p.opts.Logger != nil {
				p.opts.Logger.Printf("proxy invalid-to req_id=%s to=%q err=%v", reqID, to, err)
			}
			http.Error(w, "invalid to", http.StatusBadRequest)
			return
		}
		_ = host
		port, err := strconv.Atoi(ps)
		if err != nil || port <= 0 || port > 65535 {
			if p.opts.Logger != nil {
				p.opts.Logger.Printf("proxy invalid-port req_id=%s to=%q err=%v", reqID, to, err)
			}
			http.Error(w, "invalid port", http.StatusBadRequest)
			return
		}
		// Default scheme by port if not provided
		if scheme == "" {
			if port == 443 || port == 8443 {
				scheme = "https"
			} else {
				scheme = "http"
			}
		}
	} else {
		// server-aware with API proxy: default scheme if omitted
		if scheme == "" {
			scheme = "http"
		}
	}
	if scheme != "http" && scheme != "https" {
		http.Error(w, "invalid scheme", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), p.opts.Timeout)
	defer cancel()

	targetURL := &url.URL{Scheme: scheme, Host: to, Path: subPath}

	// Prepare transports: API proxy transport (if available) and standard transport for direct/pf
	var apiRT http.RoundTripper
	var setAPIDirector func(req *http.Request, scheme, hostport, subPath string)
	if p.opts.APIProxy != nil {
		if rt, set, ok := p.opts.APIProxy(); ok {
			apiRT = rt
			setAPIDirector = set
		}
	}
	stdRT := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			c, err := p.opts.Dial(ctx, network, address)
			if err != nil {
				return nil, err
			}
			conn, ok := c.(net.Conn)
			if !ok {
				return nil, errors.New("dialer returned non-Conn")
			}
			return conn, nil
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: p.opts.Timeout,
		ForceAttemptHTTP2:     false,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
	}
	transport := http.RoundTripper(stdRT)
	if apiRT != nil {
		transport = &dualTransport{std: stdRT, api: apiRT}
	}

	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			// Preserve original method, body, and most headers. Adjust URL.
			// Mirror ProxyPreserveHost (Apache) behavior via X-Forwarded-Host/Proto for upstream awareness.
			if r.Host != "" {
				req.Header.Set("X-Forwarded-Host", r.Host)
			}
			if r.TLS != nil {
				req.Header.Set("X-Forwarded-Proto", "https")
			} else {
				req.Header.Set("X-Forwarded-Proto", "http")
			}
			// add forwarded prefix for upstreams (code-server) to generate correct links
			// Honor existing X-Forwarded-Prefix if provided by an upstream router (e.g., cluster-scoped prefix)
			if req.Header.Get("X-Forwarded-Prefix") == "" {
				if base := basePrefixFromPath(r.URL.Path); base != "" {
					req.Header.Set("X-Forwarded-Prefix", base)
				}
			}
			if setAPIDirector != nil {
				// Include the logical server ID for service/pod discovery by API proxy layer
				if serverIDForAPI != "" {
					req.Header.Set("X-Guild-Server-ID", serverIDForAPI)
				}
				setAPIDirector(req, targetURL.Scheme, targetURL.Host, targetURL.Path)
			} else {
				req.URL.Scheme = targetURL.Scheme
				req.URL.Host = targetURL.Host
				req.Host = targetURL.Host
				req.URL.Path = singleJoiningSlash("", targetURL.Path)
			}
			// Remove our control params from query string
			q2 := req.URL.Query()
			q2.Del("to")
			q2.Del("path")
			q2.Del("scheme")
			req.URL.RawQuery = q2.Encode()
			// Best-effort header sanitization; hop-by-hop headers will be stripped by ReverseProxy internally too.
		},
		Transport: transport,
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			if p.opts.Logger != nil {
				p.opts.Logger.Printf("proxy error req_id=%s method=%s url=%s to=%s path=%s err=%v", reqID, req.Method, req.URL.String(), to, subPath, err)
			} else {
				// Fallback to standard logger so errors are visible in typical stdout/stderr logs
				log.Printf("proxy error req_id=%s method=%s url=%s to=%s path=%s err=%v", reqID, req.Method, req.URL.String(), to, subPath, err)
			}
			http.Error(rw, fmt.Sprintf("upstream error: %v", err), http.StatusBadGateway)
		},
		FlushInterval: 100 * time.Millisecond,
		BufferPool:    nil,
	}

	// Rewrite response headers for iframe/subpath compatibility (Location, Set-Cookie, CSP)
	rp.ModifyResponse = func(resp *http.Response) (err error) {
		// Guard against unexpected panics during header rewriting to avoid empty replies to clients.
		defer func() {
			if rec := recover(); rec != nil {
				if p.opts.Logger != nil {
					p.opts.Logger.Printf("proxy: ModifyResponse recovered panic: %v", rec)
				} else {
					log.Printf("proxy: ModifyResponse recovered panic: %v", rec)
				}
				// Tell ReverseProxy to synthesize a 502 to the client.
				err = errors.New("modify-response panic")
			}
		}()
		// Debug: log upstream headers that affect embedding so callers can trace why an
		// iframe may be blocked (e.g., ingress reintroducing X-Frame-Options/CSP).
		// Emit upstream header summary to configured logger or to standard logger as a fallback
		hdrs := map[string]string{
			"X-Frame-Options":              resp.Header.Get("X-Frame-Options"),
			"Content-Security-Policy":      resp.Header.Get("Content-Security-Policy"),
			"Location":                     resp.Header.Get("Location"),
			"Cross-Origin-Embedder-Policy": resp.Header.Get("Cross-Origin-Embedder-Policy"),
			"Cross-Origin-Opener-Policy":   resp.Header.Get("Cross-Origin-Opener-Policy"),
		}
		// Log Set-Cookie counts to avoid printing cookie values
		sc := len(resp.Header.Values("Set-Cookie"))
		msg := fmt.Sprintf("proxy: upstream resp for %s status=%d set-cookie-count=%d headers=%v", resp.Request.URL.String(), resp.StatusCode, sc, hdrs)
		if p.opts.Logger != nil {
			p.opts.Logger.Print(msg)
		} else {
			log.Print(msg)
		}
		// Determine baseHref from incoming path or forwarded prefix
		base := resp.Request.Header.Get("X-Forwarded-Prefix")
		if base == "" {
			base = basePrefixFromPath(r.URL.Path)
			if base == "" {
				base = "/proxy"
			}
		}
		// COOP/COEP safe for embedding
		resp.Header.Del("X-Frame-Options")
		resp.Header.Set("Cross-Origin-Opener-Policy", "same-origin-allow-popups")
		// Avoid requiring cross-origin isolation which can break iframe subresources
		resp.Header.Del("Cross-Origin-Embedder-Policy")
		resp.Header.Set("Cross-Origin-Resource-Policy", "cross-origin")
		// Relax CSP for frame-ancestors; if none, add permissive
		if csp := resp.Header.Get("Content-Security-Policy"); csp != "" {
			resp.Header.Set("Content-Security-Policy", relaxFrameAncestors(csp))
		} else {
			resp.Header.Set("Content-Security-Policy", "frame-ancestors *")
		}
		// Ensure service worker can scope itself under the proxy base
		// Use only the path component of base
		basePath := base
		if u, err := url.Parse(base); err == nil && u != nil && u.Path != "" {
			basePath = u.Path
		}
		if basePath == "" {
			basePath = "/"
		}
		resp.Header.Set("Service-Worker-Allowed", basePath)
		// Be conservative with referrers in embedded IDE
		if resp.Header.Get("Referrer-Policy") == "" {
			resp.Header.Set("Referrer-Policy", "no-referrer")
		}
		if loc := resp.Header.Get("Location"); loc != "" {
			resp.Header.Set("Location", rewriteLocation(loc, base))
		}
		// Cookie adjustments for code-server session on subpath
		if cookies := resp.Header.Values("Set-Cookie"); len(cookies) > 0 {
			resp.Header.Del("Set-Cookie")
			for _, c := range cookies {
				resp.Header.Add("Set-Cookie", rewriteSetCookieForIframe(c, base))
			}
		}
		return nil
	}

	// Serve via context with timeout
	rp.ServeHTTP(w, r.WithContext(ctx))
}

// singleJoiningSlash returns a/b ensuring only one slash joins.
func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

// basePrefixFromPath finds the proxy base prefix (including any outer prefix) for a request path.
// Examples:
// - /proxy/server/foo/bar -> /proxy/server/foo
// - /api/cluster/abc/proxy/server/foo/bar -> /api/cluster/abc/proxy/server/foo
// - /proxy/foo/bar -> /proxy/foo
func basePrefixFromPath(pth string) string {
	if pth == "" {
		return ""
	}
	// Prefer server form
	if i := strings.Index(pth, "/proxy/server/"); i >= 0 {
		rem := strings.TrimPrefix(pth[i:], "/proxy/server/")
		seg := strings.SplitN(rem, "/", 2)
		if len(seg) > 0 && seg[0] != "" {
			return pth[:i] + "/proxy/server/" + seg[0]
		}
	}
	// Fallback legacy form
	if i := strings.Index(pth, "/proxy/"); i >= 0 {
		rem := strings.TrimPrefix(pth[i:], "/proxy/")
		seg := strings.SplitN(rem, "/", 2)
		if len(seg) > 0 && seg[0] != "" {
			return pth[:i] + "/proxy/" + seg[0]
		}
	}
	return ""
}

//

// rewriteLocation rewrites absolute or root-relative Location headers to stay under the proxy baseHref.
func rewriteLocation(loc string, baseHref string) string {
	if loc == "" {
		return loc
	}
	// Absolute URL: replace scheme+host and keep path
	if u, err := url.Parse(loc); err == nil && u.IsAbs() {
		// If upstream redirects to "/" or absolute path, map into baseHref
		if u.Path == "" || strings.HasPrefix(u.Path, "/") {
			return strings.TrimRight(baseHref, "/") + u.Path
		}
		// fallback: join
		return strings.TrimRight(baseHref, "/") + "/" + u.String()
	}
	// Root-relative path
	if strings.HasPrefix(loc, "/") {
		return strings.TrimRight(baseHref, "/") + loc
	}
	// Relative path: join with base
	return strings.TrimRight(baseHref, "/") + "/" + loc
}

// Note: We no longer rewrite Set-Cookie to avoid interfering with upstream auth flows.

// htmlEscapeTrunc returns an HTML-escaped version of s, truncated to n runes with an ellipsis if needed.
// (no-op) readiness HTML removed, so htmlEscapeTrunc not needed anymore.

// relaxFrameAncestors updates a CSP string to allow embedding while preserving other directives.
// If frame-ancestors exists, it's replaced with frame-ancestors *; otherwise it's appended.
func relaxFrameAncestors(csp string) string {
	if csp == "" {
		return "frame-ancestors *"
	}
	parts := strings.Split(csp, ";")
	found := false
	for i, p := range parts {
		if strings.Contains(strings.ToLower(p), "frame-ancestors") {
			parts[i] = " frame-ancestors *"
			found = true
		}
	}
	if !found {
		parts = append(parts, " frame-ancestors *")
	}
	// Rejoin, trimming redundant whitespace
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		s := strings.TrimSpace(p)
		if s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, "; ")
}

// rewriteSetCookieForIframe adjusts Set-Cookie for proxied iframe contexts:
// - removes Domain attribute to scope to current host
// - ensures Secure and SameSite=None
// - adds Partitioned when supported by browsers
// - coerces Path to the proxy base (best effort) when upstream sets root
func rewriteSetCookieForIframe(h string, baseHref string) string {
	if h == "" {
		return h
	}
	// Split attributes
	parts := strings.Split(h, ";")
	out := make([]string, 0, len(parts)+3)
	// First part is name=value
	if len(parts) > 0 {
		nv := strings.TrimSpace(parts[0])
		out = append(out, nv)
	}
	hasSecure := false
	hasSameSite := false
	hasPath := false
	for i := 1; i < len(parts); i++ {
		p := strings.TrimSpace(parts[i])
		if p == "" {
			continue
		}
		lp := strings.ToLower(p)
		if strings.HasPrefix(lp, "domain=") {
			// drop Domain
			continue
		}
		if strings.HasPrefix(lp, "samesite=") {
			hasSameSite = true
			// force None
			continue
		}
		if lp == "secure" {
			hasSecure = true
			continue
		}
		if strings.HasPrefix(lp, "path=") {
			hasPath = true
			// Normalize path to baseHref path only (strip scheme/host)
			path := baseHref
			if u, err := url.Parse(baseHref); err == nil {
				path = u.Path
			}
			if path == "" {
				path = "/"
			}
			out = append(out, "Path="+path)
			continue
		}
		// keep other attributes
		out = append(out, p)
	}
	if !hasSecure {
		out = append(out, "Secure")
	}
	if !hasSameSite {
		out = append(out, "SameSite=None")
	}
	// Add Partitioned to allow third-party cookie partitioning in iframes when available
	out = append(out, "Partitioned")
	if !hasPath {
		path := baseHref
		if u, err := url.Parse(baseHref); err == nil {
			path = u.Path
		}
		if path == "" {
			path = "/"
		}
		out = append(out, "Path="+path)
	}
	return strings.Join(out, "; ")
}

// dualTransport chooses API transport for Kubernetes API server endpoints and std for direct/PF/ClusterIP.
type dualTransport struct{ std, api http.RoundTripper }

func (d *dualTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	p := req.URL.Path
	// Use API transport only when talking to the kube-apiserver endpoints
	if d.api != nil && (strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/apis/")) {
		return d.api.RoundTrip(req)
	}
	return d.std.RoundTrip(req)
}
