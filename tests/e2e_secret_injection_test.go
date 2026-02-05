//go:build !windows

package tests

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/klitkavm/klitkavm/daemon"
	klitkavmv1connect "github.com/klitkavm/klitkavm/proto/gen/go/klitkavm/v1/klitkavmv1connect"
)

func TestE2ESecretInjection(t *testing.T) {
	requireVMBackend(t)
	requireVirtiofsd(t)
	t.Setenv("KLITKAVM_PROXY_INSECURE", "1")

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

	secret := "super-secret"
	headerCh := make(chan string, 1)

	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headerCh <- r.Header.Get("Authorization")
		_, _ = w.Write([]byte("ok"))
	}))
	defer tlsServer.Close()

	parsedURL, err := url.Parse(tlsServer.URL)
	if err != nil {
		t.Fatalf("failed to parse server url: %v", err)
	}
	host := parsedURL.Hostname()

	addr := server.Listeners[0].Addr().String()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	script := fmt.Sprintf("echo $API_KEY; curl -fsS -H \"Authorization: Bearer $API_KEY\" %s", tlsServer.URL)
	args := []string{
		"--allow-host", host,
		"--block-private=false",
		"--secret", fmt.Sprintf("API_KEY@%s=%s", host, secret),
		"--",
		"sh", "-c", script,
	}

	output, err := runCLIExec(ctx, addr, args)
	if err != nil {
		t.Fatalf("cli exec failed: %v (output: %s)", err, output)
	}

	outputStr := string(output)
	if strings.Contains(outputStr, secret) {
		t.Fatalf("secret leaked in output: %s", outputStr)
	}

	select {
	case header := <-headerCh:
		expected := "Bearer " + secret
		if header != expected {
			t.Fatalf("unexpected auth header: %q (expected %q)", header, expected)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for auth header")
	}
}
