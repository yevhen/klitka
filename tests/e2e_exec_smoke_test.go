//go:build !windows

package tests

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestE2EExecSmoke(t *testing.T) {
	requireVMBackend(t)
	addr := startTestDaemon(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}
	repoRoot := filepath.Dir(wd)
	cliPath := filepath.Join(repoRoot, "cli")
	cmd := exec.CommandContext(ctx, "go", "run", cliPath, "exec", "--", "uname", "-a")
	cmd.Env = append(os.Environ(), "KLITKA_TCP="+addr)
	cmd.Dir = repoRoot

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cli exec failed: %v (output: %s)", err, output)
	}

	if !strings.Contains(strings.ToLower(string(output)), "linux") && !strings.Contains(strings.ToLower(string(output)), "darwin") {
		t.Fatalf("unexpected output: %s", output)
	}
}
