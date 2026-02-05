package tests

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/klitkavm/klitkavm/daemon"
	klitkavmv1connect "github.com/klitkavm/klitkavm/proto/gen/go/klitkavm/v1/klitkavmv1connect"
)

func TestE2ENetworkAllowlist(t *testing.T) {
	requireVMBackend(t)
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

	allowedHost := "example.com"
	allowedURL := "https://example.com"
	allowArgs := []string{"--allow-host", allowedHost, "--block-private=false", "--", "curl", "-fsS", allowedURL}
	output, err := runCLIExec(ctx, addr, allowArgs)
	if err != nil {
		t.Fatalf("allowed host failed: %v (output: %s)", err, output)
	}
	if !strings.Contains(string(output), "Example Domain") {
		t.Fatalf("unexpected response: %s", output)
	}

	blockedArgs := []string{"--allow-host", allowedHost, "--block-private=false", "--", "curl", "-fsS", "https://example.org"}
	output, err = runCLIExec(ctx, addr, blockedArgs)
	if err == nil {
		t.Fatalf("expected blocked host to fail (output: %s)", output)
	}
}
