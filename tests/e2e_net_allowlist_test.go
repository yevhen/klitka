package tests

import (
	"context"
	"fmt"
	"net"
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

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start http server: %v", err)
	}
	allowedPort := listener.Addr().(*net.TCPAddr).Port
	localServer := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}),
	}
	go func() {
		_ = localServer.Serve(listener)
	}()
	defer func() {
		_ = localServer.Close()
	}()

	addr := server.Listeners[0].Addr().String()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	allowedURL := fmt.Sprintf("http://localhost:%d", allowedPort)
	allowArgs := []string{"--allow-host", "localhost", "--block-private=false", "--", "curl", "-fsS", allowedURL}
	output, err := runCLIExec(ctx, addr, allowArgs)
	if err != nil {
		t.Fatalf("allowed host failed: %v (output: %s)", err, output)
	}
	if !strings.Contains(string(output), "ok") {
		t.Fatalf("unexpected response: %s", output)
	}

	blockedArgs := []string{"--allow-host", "localhost", "--block-private=false", "--", "curl", "-fsS", "http://example.com"}
	output, err = runCLIExec(ctx, addr, blockedArgs)
	if err == nil {
		t.Fatalf("expected blocked host to fail (output: %s)", output)
	}
}
