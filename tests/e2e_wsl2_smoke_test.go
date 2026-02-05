//go:build windows

package tests

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWSL2Smoke(t *testing.T) {
	if _, err := exec.LookPath("wsl.exe"); err != nil {
		t.Skip("wsl.exe not found")
	}

	if !hasWSLDistro() {
		t.Skip("no WSL distro installed")
	}

	repoRoot, err := findRepoRootWindows()
	if err != nil {
		t.Fatalf("failed to locate repo root: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "run", "./cli", "exec", "--", "uname", "-a")
	cmd.Dir = repoRoot
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("klitkavm exec failed: %v\noutput: %s", err, output)
	}

	if !strings.Contains(string(output), "Linux") {
		t.Fatalf("expected Linux output, got: %s", output)
	}
}

func hasWSLDistro() bool {
	cmd := exec.Command("wsl.exe", "-l", "-q")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) != ""
}

func findRepoRootWindows() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	path := cwd
	for {
		if fileExists(filepath.Join(path, "go.mod")) {
			return path, nil
		}
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}
	return "", errors.New("go.mod not found")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
