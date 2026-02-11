//go:build !windows

package tests

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestE2ERwMount(t *testing.T) {
	requireVMBackend(t)
	addr := startTestDaemon(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tempDir := t.TempDir()
	guestPath := "/mnt/host"
	mountFlag := tempDir + ":" + guestPath + ":rw"

	readyFile := filepath.Join(tempDir, ".ready")
	if err := os.WriteFile(readyFile, []byte("ok"), 0o644); err != nil {
		t.Fatalf("failed to write ready file: %v", err)
	}

	writeOutput, err := runCLIExec(ctx, addr, []string{"--mount", mountFlag, "--", "sh", "-c", "for i in $(seq 1 20); do [ -f /mnt/host/.ready ] && break; sleep 0.1; done; touch /mnt/host/rw.txt"})
	if err != nil {
		t.Fatalf("rw write failed: %v (output: %s)", err, writeOutput)
	}

	hostFile := filepath.Join(tempDir, "rw.txt")
	if _, err := os.Stat(hostFile); err != nil {
		t.Fatalf("expected host file to exist: %v", err)
	}
}
