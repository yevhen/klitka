package tests

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"net/http"

	"github.com/klitkavm/klitkavm/daemon"
	klitkavmv1connect "github.com/klitkavm/klitkavm/proto/gen/go/klitkavm/v1/klitkavmv1connect"
)

func TestE2EShellPTY(t *testing.T) {
	service := daemon.NewService()
	path, handler := klitkavmv1connect.NewDaemonServiceHandler(service)
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

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}
	repoRoot := filepath.Dir(wd)
	cliPath := filepath.Join(repoRoot, "cli")
	cmd := exec.CommandContext(ctx, "go", "run", cliPath, "shell")
	cmd.Env = append(os.Environ(), "KLITKAVM_TCP="+addr)
	cmd.Dir = repoRoot
	cmd.Stdin = strings.NewReader("echo hi\nexit\n")

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cli shell failed: %v (output: %s)", err, output)
	}

	if !strings.Contains(strings.ToLower(string(output)), "hi") {
		t.Fatalf("unexpected output: %s", output)
	}
}
