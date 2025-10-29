package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	conn "github.com/your/module/internal/ts/connector"
)

func main() {
	var controlURL string
	var authKey string
	var stateDir string
	var clusterID string
	var hostname string
	flag.StringVar(&controlURL, "control-url", "", "Headscale control URL, e.g. http://127.0.0.1:8082")
	flag.StringVar(&authKey, "auth-key", "", "preauth key (raw hex or tskey-...)")
	flag.StringVar(&stateDir, "state-dir", "", "directory for tsnet state files")
	flag.StringVar(&clusterID, "cluster-id", "test-connector", "cluster id to use in connector")
	flag.StringVar(&hostname, "hostname", "connector-test-node", "hostname to register with tailscale/headscale")
	flag.Parse()

	if controlURL == "" || authKey == "" {
		if controlURL == "" {
			controlURL = os.Getenv("HEADSCALE_URL")
		}
		if authKey == "" {
			authKey = os.Getenv("HEADSCALE_AUTHKEY")
		}
	}
	if controlURL == "" || authKey == "" {
		fmt.Fprintln(os.Stderr, "control-url and auth-key must be provided (flags or env)")
		os.Exit(2)
	}
	if stateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("could not determine home dir: %v", err)
		}
		stateDir = filepath.Join(home, ".guildnet", "tsnet-connector-test", clusterID)
	}

	cfg := conn.Config{
		ClusterID:     clusterID,
		LoginServer:   controlURL,
		ClientAuthKey: authKey,
		StateDir:      stateDir,
		Hostname:      hostname,
	}

	c, err := conn.New(cfg)
	if err != nil {
		log.Fatalf("connector.New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	log.Printf("starting connector for cluster=%s login=%s state=%s", clusterID, controlURL, stateDir)
	if err := c.Start(ctx); err != nil {
		log.Fatalf("connector.Start: %v", err)
	}

	// Poll health for a short period
	for i := 0; i < 12; i++ {
		st, det := c.Health(context.Background())
		log.Printf("health: %s details=%v", st, det)
		if st == "ok" {
			log.Printf("connector reports ok; exiting")
			os.Exit(0)
		}
		time.Sleep(5 * time.Second)
	}

	log.Printf("connector did not reach ok state within wait period")
	os.Exit(1)
}
