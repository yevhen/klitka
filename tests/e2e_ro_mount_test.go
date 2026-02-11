//go:build !windows

package tests

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestE2ERoMount(t *testing.T) {
	requireVMBackend(t)
	addr := startTestDaemon(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tempDir := t.TempDir()
	hostFile := filepath.Join(tempDir, "hello.txt")
	if err := os.WriteFile(hostFile, []byte("hello"), 0o644); err != nil {
		t.Fatalf("failed to write host file: %v", err)
	}

	guestPath := "/mnt/host"
	mountFlag := tempDir + ":" + guestPath + ":ro"

	readOutput, err := runCLIExec(ctx, addr, []string{"--mount", mountFlag, "--", "sh", "-c", "for i in $(seq 1 20); do [ -f /mnt/host/hello.txt ] && break; sleep 0.1; done; cat /mnt/host/hello.txt"})
	if err != nil {
		t.Fatalf("read exec failed: %v (output: %s)", err, readOutput)
	}
	if !strings.Contains(string(readOutput), "hello") {
		t.Fatalf("unexpected read output: %s", readOutput)
	}

	writeOutput, err := runCLIExec(ctx, addr, []string{"--mount", mountFlag, "--", "sh", "-c", "for i in $(seq 1 20); do [ -f /mnt/host/hello.txt ] && break; sleep 0.1; done; touch /mnt/host/new.txt"})
	if err == nil {
		t.Fatalf("expected write to fail (output: %s)", writeOutput)
	}
}
