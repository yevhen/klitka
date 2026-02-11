//go:build !windows

package tests

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestE2ENetworkAllowlist(t *testing.T) {
	requireVMBackend(t)
	addr := startTestDaemon(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	allowedHost := "example.com"
	allowedURL := "https://example.com"
	allowArgs := []string{"--allow-host", allowedHost, "--block-private=false", "--", "curl", "-fsS", allowedURL}
	output, err := runCLIExec(ctx, addr, allowArgs)
	if err != nil {
		t.Fatalf("allowed host failed: %v (output: %s)", err, output)
	}
	if !strings.Contains(string(output), "Example Domain") {
		t.Fatalf("unexpected response: %s", output)
	}

	blockedArgs := []string{"--allow-host", allowedHost, "--block-private=false", "--", "curl", "-fsS", "https://example.org"}
	output, err = runCLIExec(ctx, addr, blockedArgs)
	if err == nil {
		t.Fatalf("expected blocked host to fail (output: %s)", output)
	}
}
