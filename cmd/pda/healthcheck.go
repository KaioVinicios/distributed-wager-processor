package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/KaioVinicios/pda/internal/config"
)

const probeTimeout = 3 * time.Second

// runHealthcheck is the `pda healthcheck` subcommand used by the compose
// healthcheck (the distroless image has no shell or curl). It reads HTTP_ADDR
// directly so probing does not require the full configuration.
func runHealthcheck() int {
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = config.DefaultHTTPAddr
	}
	url, err := probeURL(addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	return probe(ctx, http.DefaultClient, url)
}

// probeURL derives the local readiness URL from HTTP_ADDR.
func probeURL(httpAddr string) (string, error) {
	host, port, err := net.SplitHostPort(httpAddr)
	if err != nil {
		return "", fmt.Errorf("healthcheck: invalid HTTP_ADDR: %w", err)
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/health/ready", nil
}

// probe returns the process exit code: 0 when readiness answers 200, 1 otherwise.
func probe(ctx context.Context, client *http.Client, url string) int {
	//nolint:gosec // G704: the URL comes from this process's own HTTP_ADDR and probes the local instance only
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp, err := client.Do(req) //nolint:gosec // G704: same local-probe request as above
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", resp.StatusCode)
		return 1
	}
	return 0
}
