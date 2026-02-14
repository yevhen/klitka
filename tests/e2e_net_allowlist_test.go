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

	allowArgs := []string{"--allow-host", allowedHost, "--block-private=false", "--", "curl", "-fsS", allowedURL}
	output, err := runCLIExec(ctx, addr, allowArgs)
	if err != nil {
		t.Fatalf("allowed host failed: %v (output: %s)", err, output)
	}
	if !strings.Contains(string(output), "ok") {
		t.Fatalf("unexpected response: %s", output)
	}

	blockedArgs := []string{"--allow-host", allowedHost, "--block-private=false", "--", "curl", "-fsS", blockedURL}
	output, err = runCLIExec(ctx, addr, blockedArgs)
	if err == nil {
		t.Fatalf("expected blocked host to fail (output: %s)", output)
	}
}
