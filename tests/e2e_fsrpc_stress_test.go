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

func TestE2EFSRPCStressCreateDeleteTree(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	hostDir := t.TempDir()
	readyFile := filepath.Join(hostDir, ".ready")
	if err := os.WriteFile(readyFile, []byte("ok"), 0o644); err != nil {
		t.Fatalf("failed to write ready file: %v", err)
	}

	mountFlag := hostDir + ":/mnt/host:rw"
	script := `set -eu
for i in $(seq 1 30); do [ -f /mnt/host/.ready ] && break; sleep 0.1; done
rm -rf /mnt/host/stress
mkdir -p /mnt/host/stress
for i in $(seq 1 30); do
  dir=/mnt/host/stress/dir-$i
  mkdir -p "$dir"
  for j in $(seq 1 20); do
    file="$dir/file-$j.txt"
    printf "%s-%s" "$i" "$j" > "$file"
    cat "$file" >/dev/null
    rm "$file"
  done
done`

	output, err := runCLIExec(ctx, addr, []string{"--mount", mountFlag, "--", "sh", "-c", script})
	if err != nil {
		t.Fatalf("stress exec failed: %v (output: %s)", err, output)
	}

	entries, err := os.ReadDir(filepath.Join(hostDir, "stress"))
	if err != nil {
		t.Fatalf("failed to read host stress dir: %v", err)
	}
	if len(entries) != 30 {
		t.Fatalf("expected 30 stress dirs, got %d entries", len(entries))
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			t.Fatalf("expected only directories in stress output, got file %q", entry.Name())
		}
		dirEntries, err := os.ReadDir(filepath.Join(hostDir, "stress", entry.Name()))
		if err != nil {
			t.Fatalf("failed to read stress subdir %q: %v", entry.Name(), err)
		}
		if len(dirEntries) != 0 {
			t.Fatalf("expected empty stress subdir %q, got %d entries", entry.Name(), len(dirEntries))
		}
	}
}
