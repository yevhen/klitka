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

func TestE2ENetworkAllowlist(t *testing.T) {
	runAllowlistScenario(t, "compat")
}

func TestE2ENetworkAllowlistStrict(t *testing.T) {
	requireStrictTransport(t)
	runAllowlistScenario(t, "strict")
}

func TestE2EBypassProxyDenied(t *testing.T) {
	requireStrictTransport(t)
	requireVMBackend(t)
	addr := startTestDaemon(t)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start http server: %v", err)
	}
	localServer := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}),
	}
	go func() {
		_ = localServer.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = localServer.Close()
	})

	port := listener.Addr().(*net.TCPAddr).Port
	targetURL := fmt.Sprintf("http://localhost:%d", port)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	script := fmt.Sprintf("unset HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy https_proxy all_proxy; curl -fsS --max-time 5 %s", targetURL)
	args := []string{
		"--allow-host", "localhost",
		"--block-private=false",
		"--egress-mode", "strict",
		"--",
		"sh", "-c", script,
	}

	output, err := runCLIExec(ctx, addr, args)
	if err == nil {
		t.Fatalf("expected direct egress bypass to fail in strict mode (output: %s)", output)
	}
}

func TestE2EDenyOverridesAllowStrict(t *testing.T) {
	requireStrictTransport(t)
	requireVMBackend(t)
	addr := startTestDaemon(t)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start http server: %v", err)
	}
	localServer := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}),
	}
	go func() {
		_ = localServer.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = localServer.Close()
	})

	targetURL := fmt.Sprintf("http://localhost:%d", listener.Addr().(*net.TCPAddr).Port)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	args := []string{
		"--allow-host", "localhost",
		"--deny-host", "localhost",
		"--block-private=false",
		"--egress-mode", "strict",
		"--",
		"curl", "-fsS", targetURL,
	}
	output, err := runCLIExec(ctx, addr, args)
	if err == nil {
		t.Fatalf("expected deny to override allow in strict mode (output: %s)", output)
	}
}

func TestE2EPrivateRangeBlockStrict(t *testing.T) {
	requireStrictTransport(t)
	requireVMBackend(t)
	addr := startTestDaemon(t)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start http server: %v", err)
	}
	localServer := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}),
	}
	go func() {
		_ = localServer.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = localServer.Close()
	})

	targetURL := fmt.Sprintf("http://localhost:%d", listener.Addr().(*net.TCPAddr).Port)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	args := []string{
		"--allow-host", "localhost",
		"--block-private=true",
		"--egress-mode", "strict",
		"--",
		"curl", "-fsS", targetURL,
	}
	output, err := runCLIExec(ctx, addr, args)
	if err == nil {
		t.Fatalf("expected private range enforcement to block localhost in strict mode (output: %s)", output)
	}
}

func TestE2ELoopbackAndMetadataBlockedStrict(t *testing.T) {
	requireStrictTransport(t)
	requireVMBackend(t)
	addr := startTestDaemon(t)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start http server: %v", err)
	}
	localServer := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}),
	}
	go func() {
		_ = localServer.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = localServer.Close()
	})

	tests := []struct {
		name string
		url  string
	}{
		{name: "loopback", url: fmt.Sprintf("http://localhost:%d", listener.Addr().(*net.TCPAddr).Port)},
		{name: "metadata", url: "http://169.254.169.254/latest/meta-data"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			args := []string{
				"--block-private=true",
				"--egress-mode", "strict",
				"--",
				"curl", "-fsS", "--max-time", "5", tc.url,
			}
			output, err := runCLIExec(ctx, addr, args)
			if err == nil {
				t.Fatalf("expected %s target to be blocked in strict mode (output: %s)", tc.name, output)
			}
		})
	}
}

func runAllowlistScenario(t *testing.T, egressMode string) {
	t.Helper()
	requireVMBackend(t)
	addr := startTestDaemon(t)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start http server: %v", err)
	}
	localServer := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}),
	}
	go func() {
		_ = localServer.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = localServer.Close()
	})

	allowedPort := listener.Addr().(*net.TCPAddr).Port
	allowedHost := "localhost"
	allowedURL := fmt.Sprintf("http://localhost:%d", allowedPort)
	blockedURL := fmt.Sprintf("http://127.0.0.1:%d", allowedPort)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	allowArgs := []string{
		"--allow-host", allowedHost,
		"--block-private=false",
		"--egress-mode", egressMode,
		"--",
		"curl", "-fsS", allowedURL,
	}
	output, err := runCLIExec(ctx, addr, allowArgs)
	if err != nil {
		t.Fatalf("allowed host failed: %v (output: %s)", err, output)
	}
	if !strings.Contains(string(output), "ok") {
		t.Fatalf("unexpected response: %s", output)
	}

	blockedArgs := []string{
		"--allow-host", allowedHost,
		"--block-private=false",
		"--egress-mode", egressMode,
		"--",
		"curl", "-fsS", blockedURL,
	}
	output, err = runCLIExec(ctx, addr, blockedArgs)
	if err == nil {
		t.Fatalf("expected blocked host to fail (output: %s)", output)
	}
}
