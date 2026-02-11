//go:build !windows

package tests

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/yevhen/klitka/daemon"
	klitkav1connect "github.com/yevhen/klitka/proto/gen/go/klitka/v1/klitkav1connect"
)

func requireVMBackend(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "linux" && !kvmAvailable() && os.Getenv("KLITKA_ALLOW_TCG") == "" {
		t.Skip("/dev/kvm not available; set KLITKA_ALLOW_TCG=1 to run with TCG")
	}
	kernel, initrd := ensureGuestAssets(t)
	ensureQemu(t)
	t.Setenv("KLITKA_BACKEND", "vm")
	t.Setenv("KLITKA_GUEST_KERNEL", kernel)
	t.Setenv("KLITKA_GUEST_INITRD", initrd)
	t.Setenv("KLITKA_TMPDIR", "/tmp")
}

func requireVirtiofsd(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") != "" && os.Getenv("KLITKA_ALLOW_VIRTIOFS") == "" {
		t.Skip("virtiofsd tests disabled in CI; set KLITKA_ALLOW_VIRTIOFS=1 to enable")
	}
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

func kvmAvailable() bool {
	fd, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = fd.Close()
	return true
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

func startTestDaemon(t *testing.T) string {
	t.Helper()

	service := daemon.NewService()
	path, handler := klitkav1connect.NewDaemonServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, handler)

	server, err := daemon.StartServer(mux, daemon.ServerOptions{TCPAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("failed to start daemon: %v", err)
	}
	t.Cleanup(func() {
		_ = server.HTTP.Close()
	})

	return server.Listeners[0].Addr().String()
}

func runCLIExec(ctx context.Context, addr string, args []string) ([]byte, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	repoRoot := filepath.Dir(wd)
	cliPath := filepath.Join(repoRoot, "cli")

	cmdArgs := append([]string{"run", cliPath, "exec"}, args...)
	cmd := exec.CommandContext(ctx, "go", cmdArgs...)
	cmd.Env = append(os.Environ(), "KLITKA_TCP="+addr)
	cmd.Dir = repoRoot
	return cmd.CombinedOutput()
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
