//go:build !windows

package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func requireVMBackend(t *testing.T) {
	t.Helper()
	kernel, initrd := ensureGuestAssets(t)
	ensureQemu(t)
	t.Setenv("KLITKAVM_BACKEND", "vm")
	t.Setenv("KLITKAVM_GUEST_KERNEL", kernel)
	t.Setenv("KLITKAVM_GUEST_INITRD", initrd)
	t.Setenv("KLITKAVM_TMPDIR", "/tmp")
}

func requireVirtiofsd(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("virtiofsd"); err != nil {
		t.Skip("virtiofsd not found in PATH")
	}
}

func ensureQemu(t *testing.T) {
	t.Helper()
	candidate := "qemu-system-x86_64"
	if runtime.GOARCH == "arm64" {
		candidate = "qemu-system-aarch64"
	}
	if _, err := exec.LookPath(candidate); err != nil {
		t.Fatalf("%s not found in PATH", candidate)
	}
}

func ensureGuestAssets(t *testing.T) (string, string) {
	t.Helper()
	repoRoot := repoRoot(t)
	outDir := filepath.Join(repoRoot, "guest", "image", "out")
	kernel := filepath.Join(outDir, "vmlinuz")
	initrd := filepath.Join(outDir, "initramfs.cpio.gz")

	if fileExists(kernel) && fileExists(initrd) {
		return kernel, initrd
	}

	buildScript := filepath.Join(repoRoot, "guest", "image", "build.sh")
	cmd := exec.Command("bash", buildScript)
	cmd.Dir = repoRoot
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("guest build failed: %v (output: %s)", err, output)
	}

	if !fileExists(kernel) || !fileExists(initrd) {
		t.Fatalf("guest build did not produce kernel/initrd in %s", outDir)
	}

	return kernel, initrd
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func repoRoot(t *testing.T) string {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}
	return filepath.Dir(wd)
}
