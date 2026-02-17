//go:build !windows

package tests

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestE2EDNSModeSyntheticBlocksExfil(t *testing.T) {
	requireVMBackend(t)
	addr := startTestDaemon(t)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start http server: %v", err)
	}
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}),
	}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
	})

	domain := "exfil.test"
	targetURL := fmt.Sprintf("http://%s:%d", domain, listener.Addr().(*net.TCPAddr).Port)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	args := []string{
		"--allow-host", domain,
		"--block-private=false",
		"--egress-mode", "strict",
		"--dns-mode", "synthetic",
		"--",
		"curl", "-fsS", targetURL,
	}
	output, err := runCLIExec(ctx, addr, args)
	if err == nil {
		t.Fatalf("expected synthetic DNS mode to block lookup (output: %s)", output)
	}
}

func TestE2EDNSModeTrustedUsesAllowedResolvers(t *testing.T) {
	requireVMBackend(t)
	addr := startTestDaemon(t)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start http server: %v", err)
	}
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}),
	}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
	})

	domain := "trusted.test"
	dnsAddr, queries := startTestDNSServer(t, map[string]string{domain: "127.0.0.1"})
	targetURL := fmt.Sprintf("http://%s:%d", domain, listener.Addr().(*net.TCPAddr).Port)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	args := []string{
		"--allow-host", domain,
		"--block-private=false",
		"--egress-mode", "strict",
		"--dns-mode", "trusted",
		"--trusted-dns", dnsAddr,
		"--",
		"curl", "-fsS", targetURL,
	}
	output, err := runCLIExec(ctx, addr, args)
	if err != nil {
		t.Fatalf("trusted DNS request failed: %v (output: %s)", err, output)
	}
	if !strings.Contains(string(output), "ok") {
		t.Fatalf("unexpected response: %s", output)
	}
	if !waitForDNSQuery(queries, domain, 5*time.Second) {
		t.Fatalf("trusted resolver did not receive query for %s", domain)
	}
}

func waitForDNSQuery(queries <-chan string, domain string, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		select {
		case query := <-queries:
			if strings.EqualFold(strings.TrimSuffix(query, "."), domain) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}
