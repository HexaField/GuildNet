package ts

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"time"

	"tailscale.com/tsnet"
)

// Options holds tsnet server init configuration.
type Options struct {
	StateDir string
	Hostname string
	LoginURL string
	AuthKey  string
}

// StartServer initializes and starts a tsnet.Server.
func StartServer(ctx context.Context, opts Options) (*tsnet.Server, error) {
	// Resolve provided auth key to the raw-hex representation that Headscale
	// commonly stores and accepts. Log the chosen format for diagnostics.
	authIn := strings.TrimSpace(opts.AuthKey)
	auth, fmtLabel := ResolveAuthKeyToRaw(authIn)
	if auth != "" {
		fmt.Printf("ts: resolved auth key format=%s provided=%s\n", fmtLabel, maskKeyPrefix(authIn))
	} else if authIn != "" {
		// fallback: pass original
		auth = authIn
		fmt.Printf("ts: could not resolve auth key; passing as-is format=%s provided=%s\n", fmtLabel, maskKeyPrefix(authIn))
	}

	s := &tsnet.Server{
		Dir:      opts.StateDir,
		Hostname: opts.Hostname,
		AuthKey:  auth,
		// ControlURL was renamed from LoginServer in older tailscale versions; current API uses ControlURL
		ControlURL: strings.TrimSpace(opts.LoginURL),
		// Surface tsnet/tailscaled logs into the hostapp logs for easier
		// debugging of authentication/registration issues.
		Logf: func(format string, args ...any) {
			// Prefix to make it clear these lines come from tsnet
			logLine := fmt.Sprintf("tsnet: "+format, args...)
			// Use the package-level fmt to emit since this file doesn't import log yet
			fmt.Println(logLine)
		},
	}

	// Log what we're passing into tsnet for debugging
	if opts.LoginURL != "" {
		fmt.Printf("ts: Starting tsnet with ControlURL=%s AuthKeySet=%t\n", opts.LoginURL, auth != "")
	} else {
		fmt.Printf("ts: Starting tsnet with no ControlURL AuthKeySet=%t\n", auth != "")
	}

	if err := s.Start(); err != nil {
		return nil, fmt.Errorf("tsnet start: %w", err)
	}

	return s, nil
}

// Listen creates a listener on the tsnet server.
func Listen(ctx context.Context, s *tsnet.Server, network, addr string) (net.Listener, error) {
	// tsnet.Listen does not require context in current API
	_ = ctx
	return s.Listen(network, addr)
}

// DialContext dials using the tsnet server's netstack.
func DialContext(ctx context.Context, s *tsnet.Server, network, addr string) (net.Conn, error) {
	return s.Dial(ctx, network, addr)
}

// Info retrieves the current node's IP and MagicDNS name.
type InfoResult struct {
	IP   string
	FQDN string
}

func Info(ctx context.Context, s *tsnet.Server) (*InfoResult, error) {
	lc, err := s.LocalClient()
	if err != nil {
		return nil, err
	}
	// Wait until we have an IP or timeout
	deadline := time.Now().Add(30 * time.Second)
	var ipStr, fqdn string
	for {
		st, err := lc.Status(ctx)
		if err == nil && st != nil {
			if len(st.TailscaleIPs) > 0 {
				ipStr = st.TailscaleIPs[0].String()
			}
			if st.Self != nil {
				fqdn = strings.TrimSuffix(st.Self.DNSName, ".")
			}
			if ipStr != "" || fqdn != "" {
				break
			}
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	return &InfoResult{IP: ipStr, FQDN: fqdn}, nil
}

// NormalizeAuthKey converts various preauth key encodings into the canonical
// tskey-<base64url-no-padding> form that tsnet expects. It accepts keys that
// are already in tskey- form, raw hex strings, or opaque values. For hex
// input it returns the tskey- prefix + base64url(no padding) encoding. For a
// non-hex string that doesn't start with tskey-, it will add the tskey-
// prefix as a fallback to preserve previous behavior.
func NormalizeAuthKey(k string) string {
	k = strings.TrimSpace(k)
	if k == "" {
		return k
	}
	if strings.HasPrefix(k, "tskey-") {
		return k
	}
	stripped := k
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
		if raw, err := hex.DecodeString(stripped); err == nil {
			enc := base64.RawURLEncoding.EncodeToString(raw)
			return "tskey-" + enc
		}
	}
	// Fallback: add tskey- prefix to preserve older behavior where plain
	// strings were accepted; this is conservative and keeps existing flows.
	if !strings.HasPrefix(k, "tskey-") {
		return "tskey-" + k
	}
	return k
}

// tskeyToHex attempts to decode a tskey-<base64url-no-pad> value back into
// the raw hex string representation. Returns an error if decoding fails.
func tskeyToHex(tsk string) (string, error) {
	if !strings.HasPrefix(tsk, "tskey-") {
		return "", fmt.Errorf("not a tskey value")
	}
	b64 := strings.TrimPrefix(tsk, "tskey-")
	// RawURLEncoding handles base64url without padding
	raw, err := base64.RawURLEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// ResolveAuthKeyToRaw attempts to deterministically resolve the provided
// preauth key into the raw-hex representation that Headscale commonly
// stores and accepts. It returns the raw hex string (if possible) and a
// short label describing which conversion was performed (for logging).
//
// Behavior:
//   - If input already looks like a hex string, return the normalized
//     lower-case hex and format "raw-hex".
//   - If input is a tskey-<base64url> value, decode and return hex with
//     format "tskey->hex".
//   - Otherwise return the original trimmed value and format "opaque".
func ResolveAuthKeyToRaw(k string) (string, string) {
	k = strings.TrimSpace(k)
	if k == "" {
		return "", "empty"
	}
	// If it already looks like a tskey- base64url value, decode to hex
	if strings.HasPrefix(k, "tskey-") {
		if hexv, err := tskeyToHex(k); err == nil && hexv != "" {
			return strings.ToLower(hexv), "tskey->hex"
		}
		// If decode failed, fall through and return opaque form
		return k, "tskey-invalid"
	}

	// Detect hex strings (even length, hex characters only)
	stripped := k
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
		return strings.ToLower(stripped), "raw-hex"
	}

	// Fallback: return as-is (opaque) so callers can still try to start
	return k, "opaque"
}

// maskKeyPrefix returns a short masked representation for logs (keeps prefix
// but hides the rest). e.g. tskey-zoI... -> tskey-zoI***
func maskKeyPrefix(k string) string {
	if len(k) <= 8 {
		return k
	}
	return k[:8] + "***"
}
