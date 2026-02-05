package tests

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/klitkavm/klitkavm/daemon"
	klitkavmv1connect "github.com/klitkavm/klitkavm/proto/gen/go/klitkavm/v1/klitkavmv1connect"
)

func TestE2ERwMount(t *testing.T) {
	requireVMBackend(t)
	requireVirtiofsd(t)
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

	tempDir := t.TempDir()
	guestPath := "/mnt/host"
	mountFlag := tempDir + ":" + guestPath + ":rw"

	fileName := "rw.txt"
	writeOutput, err := runCLIExecRW(ctx, addr, []string{"--mount", mountFlag, "--", "touch", filepath.Join(guestPath, fileName)})
	if err != nil {
		t.Fatalf("rw write failed: %v (output: %s)", err, writeOutput)
	}

	hostFile := filepath.Join(tempDir, fileName)
	if _, err := os.Stat(hostFile); err != nil {
		t.Fatalf("expected host file to exist: %v", err)
	}
}

func runCLIExecRW(ctx context.Context, addr string, args []string) ([]byte, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	repoRoot := filepath.Dir(wd)
	cliPath := filepath.Join(repoRoot, "cli")

	cmdArgs := append([]string{"run", cliPath, "exec"}, args...)
	cmd := exec.CommandContext(ctx, "go", cmdArgs...)
	cmd.Env = append(os.Environ(), "KLITKAVM_TCP="+addr)
	cmd.Dir = repoRoot
	return cmd.CombinedOutput()
}
