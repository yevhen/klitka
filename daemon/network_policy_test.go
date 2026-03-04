package daemon

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestNetworkPolicyAllowPath(t *testing.T) {
	policy := networkPolicy{
		allowHosts: []string{"api.example.com"},
	}
	if err := policy.checkHost("api.example.com"); err != nil {
		t.Fatalf("expected allowlisted host to pass, got error: %v", err)
	}
}

func TestNetworkPolicyDenyOverridesAllow(t *testing.T) {
	policy := networkPolicy{
		allowHosts: []string{"api.example.com"},
		denyHosts:  []string{"api.example.com"},
	}
	if err := policy.checkHost("api.example.com"); err == nil {
		t.Fatalf("expected deny list to override allow list")
	}
}

func TestProxyEnforceHostBlocksPrivateEvenWhenAllowlisted(t *testing.T) {
	proxy := &proxyServer{
		policy: networkPolicy{
			allowHosts:        []string{"127.0.0.1", "169.254.169.254"},
			blockPrivateRange: true,
		},
	}

	for _, host := range []string{"127.0.0.1", "169.254.169.254"} {
		host := host
		t.Run(host, func(t *testing.T) {
			if err := proxy.enforceHost(context.Background(), host); err == nil {
				t.Fatalf("expected private host %s to be blocked", host)
			}
		})
	}

	if got := atomic.LoadUint64(&proxy.blockedEgress); got != 2 {
		t.Fatalf("expected blocked egress counter 2, got %d", got)
	}
}

func TestProxyEnforceHostBlocksLoopbackAndMetadata(t *testing.T) {
	proxy := &proxyServer{policy: networkPolicy{blockPrivateRange: true}}

	for _, host := range []string{"127.0.0.1", "169.254.169.254"} {
		host := host
		t.Run(host, func(t *testing.T) {
			if err := proxy.enforceHost(context.Background(), host); err == nil {
				t.Fatalf("expected %s to be blocked by private range policy", host)
			}
		})
	}
}
