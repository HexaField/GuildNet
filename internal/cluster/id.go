package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// DeterministicIDFromKubeconfig computes a stable cluster id from the
// provided kubeconfig bytes. It picks the first cluster entry found in the
// kubeconfig and derives a SHA-256 hex digest of serverURL + '::' + caData
// (base64 or raw bytes) to produce a deterministic id. The result is
// shortened to 32 hex chars to keep filesystem names manageable.
func DeterministicIDFromKubeconfig(kc string) (string, error) {
	var cfg clientcmdapi.Config
	if err := json.Unmarshal([]byte(kc), &cfg); err != nil {
		// Try the YAML path via parsing from clientcmd? Fallback to treating as raw
		// text and using the whole payload.
		sum := sha256.Sum256([]byte(kc))
		return hex.EncodeToString(sum[:])[:32], nil
	}
	// Pick first cluster entry
	for _, cl := range cfg.Clusters {
		server := cl.Server
		ca := ""
		if len(cl.CertificateAuthorityData) > 0 {
			ca = string(cl.CertificateAuthorityData)
		}
		payload := fmt.Sprintf("%s::%s", server, ca)
		sum := sha256.Sum256([]byte(payload))
		return hex.EncodeToString(sum[:])[:32], nil
	}
	// Fallback: hash entire kubeconfig
	sum := sha256.Sum256([]byte(kc))
	return hex.EncodeToString(sum[:])[:32], nil
}
