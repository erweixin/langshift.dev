package egress

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

type resolverResult struct {
	addresses []netip.Addr
	err       error
}

type resolverStub struct {
	results []resolverResult
	calls   int
	hosts   []string
}

func (resolver *resolverStub) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	resolver.hosts = append(resolver.hosts, network+":"+host)
	index := resolver.calls
	resolver.calls++
	if index >= len(resolver.results) {
		index = len(resolver.results) - 1
	}
	if index < 0 {
		return nil, errors.New("no resolver result")
	}
	return resolver.results[index].addresses, resolver.results[index].err
}

func addresses(values ...string) []netip.Addr {
	result := make([]netip.Addr, len(values))
	for index, value := range values {
		result[index] = netip.MustParseAddr(value)
	}
	return result
}

func TestEndpointPolicyAcceptsOnlyPublicBoundHTTPSOrigin(t *testing.T) {
	resolver := &resolverStub{results: []resolverResult{{addresses: addresses("8.8.8.8", "2606:4700:4700::1111")}}}
	policy := EndpointPolicy{Resolver: resolver}
	endpoint, err := policy.Validate(t.Context(), "https://API.Example.com./v1?discarded=true", "api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.BoundHost != "api.example.com" || endpoint.Origin != "https://api.example.com" || endpoint.DialPort != "443" || endpoint.RequestURL != "https://api.example.com/v1" {
		t.Fatalf("unexpected endpoint: %#v", endpoint)
	}
	if resolver.calls != 1 || len(resolver.hosts) != 1 || resolver.hosts[0] != "ip:api.example.com" {
		t.Fatalf("unexpected resolution: %#v", resolver)
	}
	if _, err = policy.Validate(t.Context(), "https://api.example.com/v1", "other.example.com"); !errors.Is(err, ErrEndpointDrift) {
		t.Fatalf("bound-host substitution error=%v", err)
	}
}

func TestEndpointPolicyRejectsURLConfusionAndUnsafeNames(t *testing.T) {
	public := resolverResult{addresses: addresses("8.8.8.8")}
	for _, rawURL := range []string{
		"http://api.example.com/v1",
		"https://user:secret@api.example.com/v1",
		"https://api.example.com/v1#fragment",
		"https://api.example.com:8443/v1",
		"https://127.0.0.1/v1",
		"https://localhost/v1",
		"https://provider.local/v1",
		"https://metadata.google.internal/v1",
		"https://instance-data.ec2.internal/v1",
		"https://bücher.example/v1",
		"https://singlelabel/v1",
		"https://-bad.example/v1",
	} {
		t.Run(rawURL, func(t *testing.T) {
			policy := EndpointPolicy{Resolver: &resolverStub{results: []resolverResult{public}}}
			if _, err := policy.Validate(t.Context(), rawURL, ""); !errors.Is(err, ErrInvalidEndpoint) && !errors.Is(err, ErrUnsafeEndpoint) {
				t.Fatalf("unsafe endpoint accepted or wrong error: %v", err)
			}
		})
	}
}

func TestEndpointPolicyRejectsEverySpecialAddressAndMixedDNSAnswer(t *testing.T) {
	unsafeAddresses := []string{
		"0.0.0.1", "10.0.0.1", "100.64.0.1", "127.0.0.1", "169.254.169.254", "172.16.0.1",
		"192.0.0.1", "192.0.2.1", "192.168.1.1", "198.18.0.1", "198.51.100.1", "203.0.113.1",
		"224.0.0.1", "240.0.0.1", "::", "::1", "64:ff9b::1", "100::1", "2001:db8::1",
		"2002::1", "fc00::1", "fe80::1", "ff02::1", "::ffff:127.0.0.1",
	}
	for _, value := range unsafeAddresses {
		t.Run(value, func(t *testing.T) {
			resolver := &resolverStub{results: []resolverResult{{addresses: addresses(value)}}}
			if _, err := (EndpointPolicy{Resolver: resolver}).Validate(t.Context(), "https://api.example.com", "api.example.com"); !errors.Is(err, ErrUnsafeEndpoint) {
				t.Fatalf("address %s accepted: %v", value, err)
			}
		})
	}
	mixed := &resolverStub{results: []resolverResult{{addresses: addresses("8.8.8.8", "10.0.0.7")}}}
	if _, err := (EndpointPolicy{Resolver: mixed}).Validate(t.Context(), "https://api.example.com", ""); !errors.Is(err, ErrUnsafeEndpoint) {
		t.Fatalf("mixed public/private DNS answer accepted: %v", err)
	}
	platform := &resolverStub{results: []resolverResult{{addresses: addresses("8.8.8.8")}}}
	policy := EndpointPolicy{Resolver: platform, PlatformNetworks: []netip.Prefix{netip.MustParsePrefix("8.8.8.0/24")}}
	if _, err := policy.Validate(t.Context(), "https://api.example.com", ""); !errors.Is(err, ErrUnsafeEndpoint) {
		t.Fatalf("platform network accepted: %v", err)
	}
}

func TestEndpointPolicyRequiresResolutionAndExplicitCustomPortAllowlist(t *testing.T) {
	failing := &resolverStub{results: []resolverResult{{err: errors.New("resolver detail must not escape")}}}
	if _, err := (EndpointPolicy{Resolver: failing}).Validate(t.Context(), "https://api.example.com", ""); !errors.Is(err, ErrUnsafeEndpoint) || errors.Is(err, failing.results[0].err) {
		t.Fatalf("resolution failure did not fail closed: %v", err)
	}
	resolver := &resolverStub{results: []resolverResult{{addresses: addresses("8.8.8.8")}}}
	endpoint, err := (EndpointPolicy{Resolver: resolver, AllowedPorts: map[uint16]struct{}{9443: {}}}).Validate(t.Context(), "https://api.example.com:9443/v1", "api.example.com")
	if err != nil || endpoint.DialPort != "9443" || endpoint.Origin != "https://api.example.com:9443" {
		t.Fatalf("explicit custom port rejected: %#v err=%v", endpoint, err)
	}
}
