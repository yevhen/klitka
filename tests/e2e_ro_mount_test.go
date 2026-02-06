//go:build !windows

package tests

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klitka/klitka/daemon"
	klitkav1connect "github.com/klitka/klitka/proto/gen/go/klitka/v1/klitkav1connect"
)

func TestE2ERoMount(t *testing.T) {
	requireVMBackend(t)
	requireVirtiofsd(t)
	service := daemon.NewService()
	path, handler := klitkav1connect.NewDaemonServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, handler)

	server, err := daemon.StartServer(mux, daemon.ServerOptions{TCPAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("failed to start daemon: %v", err)
	}
	defer func() {
		_ = server.HTTP.Close()
	}()

	addr := server.Listeners[0].Addr().String()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tempDir := t.TempDir()
	hostFile := filepath.Join(tempDir, "hello.txt")
	if err := os.WriteFile(hostFile, []byte("hello"), 0o644); err != nil {
		t.Fatalf("failed to write host file: %v", err)
	}

	guestPath := "/mnt/host"
	mountFlag := tempDir + ":" + guestPath + ":ro"

	readOutput, err := runCLIExec(ctx, addr, []string{"--mount", mountFlag, "--", "cat", filepath.Join(guestPath, "hello.txt")})
	if err != nil {
		t.Fatalf("read exec failed: %v (output: %s)", err, readOutput)
	}
	if !strings.Contains(string(readOutput), "hello") {
		t.Fatalf("unexpected read output: %s", readOutput)
	}

	writeOutput, err := runCLIExec(ctx, addr, []string{"--mount", mountFlag, "--", "touch", filepath.Join(guestPath, "new.txt")})
	if err == nil {
		t.Fatalf("expected write to fail (output: %s)", writeOutput)
	}
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
