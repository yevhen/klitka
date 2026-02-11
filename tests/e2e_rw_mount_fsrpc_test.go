//go:build !windows

package tests

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yevhen/klitka/daemon"
	klitkav1connect "github.com/yevhen/klitka/proto/gen/go/klitka/v1/klitkav1connect"
)

func TestE2ERwMountFSRPC(t *testing.T) {
	requireVMBackend(t)
	t.Setenv("KLITKA_FS_BACKEND", "fsrpc")

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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tempDir := t.TempDir()
	guestPath := "/mnt/host"
	mountFlag := tempDir + ":" + guestPath + ":rw"

	readyFile := filepath.Join(tempDir, ".ready")
	if err := os.WriteFile(readyFile, []byte("ok"), 0o644); err != nil {
		t.Fatalf("failed to write ready file: %v", err)
	}

	var output []byte
	output, err = runCLIExec(ctx, addr, []string{"--mount", mountFlag, "--", "sh", "-c", "for i in $(seq 1 20); do [ -f /mnt/host/.ready ] && break; sleep 0.1; done; mkdir -p /mnt/host/work && printf hello >/mnt/host/work/file.txt"})
	if err != nil {
		t.Fatalf("write exec failed: %v (output: %s)", err, output)
	}

	hostFile := filepath.Join(tempDir, "work", "file.txt")
	data, err := os.ReadFile(hostFile)
	if err != nil {
		t.Fatalf("expected host file to exist: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected host file content: %q", string(data))
	}

	output, err = runCLIExec(ctx, addr, []string{"--mount", mountFlag, "--", "sh", "-c", "for i in $(seq 1 20); do [ -f /mnt/host/.ready ] && break; sleep 0.1; done; rm /mnt/host/work/file.txt"})
	if err != nil {
		t.Fatalf("unlink exec failed: %v (output: %s)", err, output)
	}

	if _, err := os.Stat(filepath.Join(tempDir, "work", "file.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected file to be removed, stat err=%v", err)
	}
}
