package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	clientcmd "k8s.io/client-go/tools/clientcmd"
)

// DeterministicIDFromKubeconfig computes a stable cluster id from the
// provided kubeconfig bytes. It picks the first cluster entry found in the
// kubeconfig and derives a SHA-256 hex digest of normalized serverURL + '::' + caData
// to produce a deterministic id. The result is shortened to 32 hex chars to keep
// names manageable.
func DeterministicIDFromKubeconfig(kc string) (string, error) {
	// Try to parse kubeconfig (YAML or JSON) using clientcmd.
	cfg, err := clientcmd.Load([]byte(kc))
	if err == nil && cfg != nil {
		// Pick first cluster entry
		for _, cl := range cfg.Clusters {
			server := strings.TrimSpace(cl.Server)
			// Normalize server url by removing trailing slash
			server = strings.TrimRight(server, "/")
			ca := ""
			if len(cl.CertificateAuthorityData) > 0 {
				ca = string(cl.CertificateAuthorityData)
			}
			payload := fmt.Sprintf("%s::%s", server, ca)
			sum := sha256.Sum256([]byte(payload))
			return hex.EncodeToString(sum[:])[:32], nil
		}
	}
	// Fallback: hash entire kubeconfig payload as a last resort.
	sum := sha256.Sum256([]byte(kc))
	return hex.EncodeToString(sum[:])[:32], nil
}
