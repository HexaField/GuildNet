package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestDeterministicIDFromKubeconfig_YAML(t *testing.T) {
	yaml := `apiVersion: v1
clusters:
- cluster:
    server: https://1.2.3.4/
    certificate-authority-data: YmFzZTY0Y2E=
  name: test
contexts: []
`
	id, err := DeterministicIDFromKubeconfig(yaml)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// Ensure id length is 32 hex chars (our implementation truncates to 32)
	if len(id) != 32 {
		t.Fatalf("expected id length 32 got %d", len(id))
	}
}

func TestDeterministicIDFromKubeconfig_Normalization(t *testing.T) {
	y1 := `clusters:
- cluster:
    server: https://example.com/
    certificate-authority-data: Y2E=
  name: c
`
	y2 := `clusters:
- cluster:
    server: https://example.com
    certificate-authority-data: Y2E=
  name: c
`
	id1, err := DeterministicIDFromKubeconfig(y1)
	if err != nil {
		t.Fatalf("err1: %v", err)
	}
	id2, err := DeterministicIDFromKubeconfig(y2)
	if err != nil {
		t.Fatalf("err2: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("expected normalized ids equal got %s and %s", id1, id2)
	}
}

func TestDeterministicIDFromKubeconfig_FallbackHashes(t *testing.T) {
	raw := "not a kubeconfig"
	id, err := DeterministicIDFromKubeconfig(raw)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	sum := sha256.Sum256([]byte(raw))
	exp := hex.EncodeToString(sum[:])[:32]
	if id != exp {
		t.Fatalf("expected fallback hash %s got %s", exp, id)
	}
}
