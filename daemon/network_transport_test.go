package daemon

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	klitkav1 "github.com/yevhen/klitka/proto/gen/go/klitka/v1"
)

func TestLookupIPsStrictRejectsIPv6Literal(t *testing.T) {
	proxy := &proxyServer{policy: networkPolicy{egressMode: klitkav1.EgressMode_EGRESS_MODE_STRICT}}

	_, err := proxy.lookupIPs(context.Background(), "::1")
	if err == nil {
		t.Fatalf("expected strict mode IPv6 literal to be rejected")
	}
	if !strings.Contains(err.Error(), "ipv6 egress unsupported in strict mode") {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := atomic.LoadUint64(&proxy.blockedEgress); got != 1 {
		t.Fatalf("expected blocked egress counter increment, got %d", got)
	}
}

func TestLookupIPsStrictSyntheticLocalhostUsesIPv4Only(t *testing.T) {
	proxy := &proxyServer{policy: networkPolicy{
		egressMode: klitkav1.EgressMode_EGRESS_MODE_STRICT,
		dnsMode:    klitkav1.DNSMode_DNS_MODE_SYNTHETIC,
	}}

	ips, err := proxy.lookupIPs(context.Background(), "localhost")
	if err != nil {
		t.Fatalf("lookupIPs failed: %v", err)
	}
	if len(ips) != 1 {
		t.Fatalf("expected only IPv4 address in strict mode, got %d", len(ips))
	}
	if ips[0].To4() == nil {
		t.Fatalf("expected IPv4 address, got %s", ips[0].String())
	}
}
