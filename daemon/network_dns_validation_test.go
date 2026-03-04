package daemon

import (
	"strings"
	"testing"

	klitkav1 "github.com/yevhen/klitka/proto/gen/go/klitka/v1"
)

func TestBuildNetworkPolicyTrustedRequiresResolvers(t *testing.T) {
	policy, err := buildNetworkPolicy(&klitkav1.NetworkPolicy{
		DnsMode: klitkav1.DNSMode_DNS_MODE_TRUSTED,
	})
	if err != nil {
		t.Fatalf("buildNetworkPolicy failed unexpectedly: %v", err)
	}
	if err := validateNetworkPolicy(policy); err == nil {
		t.Fatalf("expected trusted DNS mode without resolvers to fail validation")
	}
}

func TestBuildNetworkPolicyRejectsInvalidTrustedResolverEndpoint(t *testing.T) {
	_, err := buildNetworkPolicy(&klitkav1.NetworkPolicy{
		DnsMode:           klitkav1.DNSMode_DNS_MODE_TRUSTED,
		TrustedDnsServers: []string{"not-an-ip"},
	})
	if err == nil {
		t.Fatalf("expected invalid trusted dns endpoint to fail")
	}
	if !strings.Contains(err.Error(), "invalid trusted dns server") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildNetworkPolicyAcceptsValidTrustedResolverEndpoints(t *testing.T) {
	policy, err := buildNetworkPolicy(&klitkav1.NetworkPolicy{
		DnsMode:           klitkav1.DNSMode_DNS_MODE_TRUSTED,
		TrustedDnsServers: []string{"1.1.1.1", "[2606:4700:4700::1111]:53"},
	})
	if err != nil {
		t.Fatalf("buildNetworkPolicy failed: %v", err)
	}
	if err := validateNetworkPolicy(policy); err != nil {
		t.Fatalf("validateNetworkPolicy failed: %v", err)
	}
	if len(policy.trustedDNSServers) != 2 {
		t.Fatalf("expected 2 trusted resolvers, got %d", len(policy.trustedDNSServers))
	}
	if policy.trustedDNSServers[0] != "1.1.1.1:53" {
		t.Fatalf("unexpected normalized resolver: %s", policy.trustedDNSServers[0])
	}
}
