package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	ts "github.com/your/module/internal/ts"
)

func main() {
	var controlURL string
	var authKey string
	var stateDir string
	var hostname string
	flag.StringVar(&controlURL, "control-url", "", "Headscale control URL, e.g. http://127.0.0.1:8082")
	flag.StringVar(&authKey, "auth-key", "", "preauth key (raw hex or tskey-...)")
	flag.StringVar(&stateDir, "state-dir", "", "directory for tsnet state files")
	flag.StringVar(&hostname, "hostname", "tsnet-test", "tsnet hostname to register")
	flag.Parse()

	if controlURL == "" || authKey == "" {
		// Try environment fallbacks
		if controlURL == "" {
			controlURL = os.Getenv("HEADSCALE_URL")
		}
		if authKey == "" {
			authKey = os.Getenv("HEADSCALE_AUTHKEY")
		}
	}

	if controlURL == "" || authKey == "" {
		fmt.Fprintln(os.Stderr, "control-url and auth-key must be provided (flags or HEADSCALE_URL/HEADSCALE_AUTHKEY)")
		os.Exit(2)
	}

	if stateDir == "" {
		// default to user's home under .guildnet for a realistic run
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "could not determine home dir: %v\n", err)
			os.Exit(3)
		}
		stateDir = filepath.Join(home, ".guildnet", "tsnet-test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	opts := ts.Options{
		StateDir: stateDir,
		Hostname: hostname,
		LoginURL: controlURL,
		AuthKey:  authKey,
	}

	fmt.Printf("starting tsnet with control=%s state=%s\n", controlURL, stateDir)

	s, err := ts.StartServer(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "StartServer error: %v\n", err)
		os.Exit(4)
	}
	defer func() {
		_ = s.Close()
	}()

	// Wait for Info (IP or FQDN)
	info, err := ts.Info(ctx, s)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Info error: %v\n", err)
		os.Exit(5)
	}

	fmt.Printf("Result: ip=%q fqdn=%q\n", info.IP, info.FQDN)
	if info.IP == "" {
		os.Exit(6)
	}
	os.Exit(0)
}
