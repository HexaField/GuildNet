package k8s

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	spdy "k8s.io/client-go/transport/spdy"

	connectorpkg "github.com/your/module/internal/ts/connector"
)

// PortForwardManager maintains on-demand port-forwards to pods.
// It is best-effort and optimized for single-user dev scenarios.
// Not production-hardened.

type PortForwardManager struct {
	cfg       *rest.Config
	namespace string
	clusterID string
	mu        sync.Mutex
	forwards  map[string]*pfEntry // key: ns/pod:port
}

type pfEntry struct {
	localPort int
	stopCh    chan struct{}
	readyCh   chan struct{}
}

func NewPortForwardManager(cfg *rest.Config, namespace string) *PortForwardManager {
	// Backward-compatible constructor (no explicit cluster id)
	return &PortForwardManager{cfg: cfg, namespace: namespace, forwards: make(map[string]*pfEntry)}
}

// NewPortForwardManagerWithCluster sets a cluster ID to ensure keys are globally unique across clusters.
func NewPortForwardManagerWithCluster(cfg *rest.Config, clusterID, namespace string) *PortForwardManager {
	return &PortForwardManager{cfg: cfg, namespace: namespace, clusterID: clusterID, forwards: make(map[string]*pfEntry)}
}

// Ensure ensures a port-forward is running to pod:podPort and returns localPort.
func (m *PortForwardManager) Ensure(ctx context.Context, namespace, pod string, podPort int) (int, error) {
	if namespace == "" {
		namespace = m.namespace
	}
	key := fmt.Sprintf("%s|%s/%s:%d", m.clusterID, namespace, pod, podPort)
	m.mu.Lock()
	if e, ok := m.forwards[key]; ok {
		lp := e.localPort
		m.mu.Unlock()
		// Quick probe to see if still serving
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", lp)), 300*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return lp, nil
		}
		// fallthrough to recreate
		m.mu.Lock()
		delete(m.forwards, key)
	}
	m.mu.Unlock()

	// Pick a free local port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	lp := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	// Build spdy roundtripper/dialer against the kube-apiserver from rest.Config
	rt, upgrader, err := spdy.RoundTripperFor(m.cfg)
	if err != nil {
		return 0, err
	}
	hostURL, err := url.Parse(m.cfg.Host)
	if err != nil {
		return 0, err
	}
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", namespace, pod)
	u := &url.URL{Scheme: hostURL.Scheme, Host: hostURL.Host, Path: path}
	// Wrap the roundtripper with the connector debug logger when enabled so
	// we capture X-Tailscale-Handshake values during development.
	wrappedRT := connectorpkg.LoggingTransport(rt, m.clusterID)
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: wrappedRT}, "POST", u)

	stopCh := make(chan struct{}, 1)
	readyCh := make(chan struct{}, 1)
	fw, err := portforward.New(dialer, []string{fmt.Sprintf("%d:%d", lp, podPort)}, stopCh, readyCh, nil, nil)
	if err != nil {
		return 0, err
	}
	go func() {
		_ = fw.ForwardPorts()
	}()
	select {
	case <-readyCh:
		// started
	case <-time.After(8 * time.Second):
		close(stopCh)
		return 0, fmt.Errorf("port-forward start timeout")
	}
	m.mu.Lock()
	m.forwards[key] = &pfEntry{localPort: lp, stopCh: stopCh, readyCh: readyCh}
	m.mu.Unlock()
	return lp, nil
}
