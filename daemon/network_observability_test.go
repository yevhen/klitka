package daemon

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"

	klitkav1 "github.com/yevhen/klitka/proto/gen/go/klitka/v1"
)

func TestProxyShutdownCountersReflectDeniedTraffic(t *testing.T) {
	proxy, err := newProxyServer(networkPolicy{
		egressMode:        klitkav1.EgressMode_EGRESS_MODE_STRICT,
		dnsMode:           klitkav1.DNSMode_DNS_MODE_SYNTHETIC,
		allowHosts:        []string{"exfil.test"},
		blockPrivateRange: true,
	}, nil)
	if err != nil {
		t.Fatalf("newProxyServer failed: %v", err)
	}
	defer func() {
		_ = proxy.Close()
	}()

	_ = proxy.enforceHost(context.Background(), "other-host.test") // blocked by allowlist
	_ = proxy.enforceHost(context.Background(), "exfil.test")      // synthetic dns + private check path

	loggerOutput := &bytes.Buffer{}
	originalWriter := log.Writer()
	originalFlags := log.Flags()
	log.SetOutput(loggerOutput)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(originalWriter)
		log.SetFlags(originalFlags)
	}()

	if err := proxy.Close(); err != nil {
		t.Fatalf("proxy close failed: %v", err)
	}

	logs := loggerOutput.String()
	if !strings.Contains(logs, "network proxy stopped") {
		t.Fatalf("expected shutdown log entry, got: %s", logs)
	}
	if !strings.Contains(logs, "blocked_egress=2") {
		t.Fatalf("expected blocked_egress counter in shutdown logs, got: %s", logs)
	}
	if !strings.Contains(logs, "dns_blocked=1") {
		t.Fatalf("expected dns_blocked counter in shutdown logs, got: %s", logs)
	}
}
