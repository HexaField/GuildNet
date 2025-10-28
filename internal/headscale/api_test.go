package headscale

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFindMachineIPsByHostname_ArrayResponse(t *testing.T) {
	// prepare a fake headscale API that returns an array of machines
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`[{"id":"m-123","hostname":"guildnet-node","given_name":"node","ip_addresses":["100.101.102.103"]}]`))
	}))
	defer h.Close()

	ips, mid, err := FindMachineIPsByHostname(h.URL, "", "guildnet-node")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if mid != "m-123" {
		t.Fatalf("expected machine id m-123 got %q", mid)
	}
	if len(ips) != 1 || ips[0] != "100.101.102.103" {
		t.Fatalf("unexpected ips: %v", ips)
	}
}

func TestFindMachineIPsByHostname_WrappedResponse(t *testing.T) {
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"machines":[{"id":"m-456","hostname":"guildnet-node-2","given_name":"node2","ip_addresses":"[\"100.1.1.1\"]"}]}`))
	}))
	defer h.Close()

	ips, mid, err := FindMachineIPsByHostname(h.URL, "", "guildnet-node-2")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if mid != "m-456" {
		t.Fatalf("expected machine id m-456 got %q", mid)
	}
	if len(ips) != 1 || ips[0] != "100.1.1.1" {
		t.Fatalf("unexpected ips: %v", ips)
	}
}
