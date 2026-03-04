//go:build !windows

package tests

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const guestGatewayHost = "10.0.2.2"

func TestE2EDirectTCPOtherPortsBlockedStrict(t *testing.T) {
	requireStrictTransport(t)
	requireVMBackend(t)
	addr := startTestDaemon(t)

	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("failed to start tcp sink: %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
	})

	accepted := make(chan struct{}, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = conn.Close()
			accepted <- struct{}{}
		}
	}()

	port := listener.Addr().(*net.TCPAddr).Port
	script := fmt.Sprintf(`unset HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy https_proxy all_proxy
if command -v nc >/dev/null 2>&1; then
  printf 'ping' | nc -w 2 %s %d >/dev/null 2>&1 && echo TCP_CONNECTED || echo TCP_BLOCKED
elif command -v netcat >/dev/null 2>&1; then
  printf 'ping' | netcat -w 2 %s %d >/dev/null 2>&1 && echo TCP_CONNECTED || echo TCP_BLOCKED
elif command -v curl >/dev/null 2>&1 && curl --version 2>/dev/null | grep -q 'Protocols:.*telnet'; then
  curl --max-time 3 telnet://%s:%d >/dev/null 2>&1 && echo TCP_CONNECTED || echo TCP_BLOCKED
else
  echo NO_TCP_TOOL
fi
`, guestGatewayHost, port, guestGatewayHost, port, guestGatewayHost, port)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, _ := runCLIExec(ctx, addr, []string{
		"--egress-mode", "strict",
		"--",
		"sh", "-c", script,
	})
	text := string(output)
	if strings.Contains(text, "NO_TCP_TOOL") {
		t.Skip("guest image lacks tools for direct TCP probe")
	}
	if strings.Contains(text, "TCP_CONNECTED") {
		t.Fatalf("expected strict mode to block direct TCP egress (output: %s)", text)
	}

	select {
	case <-accepted:
		t.Fatalf("direct TCP connection reached host sink in strict mode")
	case <-time.After(1500 * time.Millisecond):
	}
}

func TestE2EUDPNon53BlockedStrict(t *testing.T) {
	requireStrictTransport(t)
	requireVMBackend(t)
	addr := startTestDaemon(t)

	udpAddr, err := net.ResolveUDPAddr("udp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("failed to resolve udp addr: %v", err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		t.Fatalf("failed to start udp sink: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})

	packet := make(chan struct{}, 1)
	go func() {
		buf := make([]byte, 64)
		_ = conn.SetReadDeadline(time.Now().Add(4 * time.Second))
		if n, _, readErr := conn.ReadFromUDP(buf); readErr == nil && n > 0 {
			packet <- struct{}{}
		}
	}()

	port := conn.LocalAddr().(*net.UDPAddr).Port
	script := fmt.Sprintf(`unset HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy https_proxy all_proxy
if command -v nc >/dev/null 2>&1; then
  printf 'ping' | nc -u -w 2 %s %d >/dev/null 2>&1 && echo UDP_SENT || echo UDP_BLOCKED
elif command -v netcat >/dev/null 2>&1; then
  printf 'ping' | netcat -u -w 2 %s %d >/dev/null 2>&1 && echo UDP_SENT || echo UDP_BLOCKED
else
  echo NO_UDP_TOOL
fi
`, guestGatewayHost, port, guestGatewayHost, port)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, _ := runCLIExec(ctx, addr, []string{
		"--egress-mode", "strict",
		"--",
		"sh", "-c", script,
	})
	text := string(output)
	if strings.Contains(text, "NO_UDP_TOOL") {
		t.Skip("guest image lacks tools for UDP probe")
	}

	select {
	case <-packet:
		t.Fatalf("direct UDP packet reached host sink in strict mode (output: %s)", text)
	case <-time.After(1500 * time.Millisecond):
	}
}

func TestE2EIPv6BlockedStrict(t *testing.T) {
	requireStrictTransport(t)
	requireVMBackend(t)
	addr := startTestDaemon(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	output, err := runCLIExec(ctx, addr, []string{
		"--allow-host", "::1",
		"--block-private=false",
		"--egress-mode", "strict",
		"--",
		"sh", "-c", "curl -sS --max-time 5 http://[::1]:80 || true",
	})
	if err != nil {
		t.Fatalf("unexpected cli error for ipv6 strict check: %v (output: %s)", err, output)
	}
	if !strings.Contains(string(output), "ipv6 egress unsupported in strict mode") {
		t.Fatalf("expected explicit strict ipv6 error, got: %s", output)
	}
}

func TestE2EDoHEndpointRespectsAllowlistStrict(t *testing.T) {
	requireStrictTransport(t)
	requireVMBackend(t)
	addr := startTestDaemon(t)

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("doh-ok"))
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("failed to parse tls server url: %v", err)
	}
	host := parsed.Hostname()
	target := server.URL + "/dns-query"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	allowedOut, allowedErr := runCLIExec(ctx, addr, []string{
		"--allow-host", host,
		"--block-private=false",
		"--egress-mode", "strict",
		"--",
		"curl", "-kfsS", target,
	})
	if allowedErr != nil {
		t.Fatalf("expected DoH endpoint to work when allowlisted: %v (output: %s)", allowedErr, allowedOut)
	}
	if !strings.Contains(string(allowedOut), "doh-ok") {
		t.Fatalf("unexpected DoH allowlisted output: %s", allowedOut)
	}

	blockedOut, blockedErr := runCLIExec(ctx, addr, []string{
		"--allow-host", "example.com",
		"--block-private=false",
		"--egress-mode", "strict",
		"--",
		"curl", "-kfsS", target,
	})
	if blockedErr == nil {
		t.Fatalf("expected DoH endpoint to be blocked when not allowlisted (output: %s)", blockedOut)
	}
}
