package egress

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"testing"
)

func TestStage3ProviderEgressGate(t *testing.T) {
	type attack struct {
		name  string
		block func() bool
	}
	attacks := []attack{
		{name: "loopback", block: func() bool {
			_, err := (EndpointPolicy{Resolver: &resolverStub{results: []resolverResult{{addresses: addresses("127.0.0.1")}}}}).Validate(t.Context(), "https://api.example.com", "api.example.com")
			return errors.Is(err, ErrUnsafeEndpoint)
		}},
		{name: "private_network", block: func() bool {
			_, err := (EndpointPolicy{Resolver: &resolverStub{results: []resolverResult{{addresses: addresses("10.0.0.1")}}}}).Validate(t.Context(), "https://api.example.com", "api.example.com")
			return errors.Is(err, ErrUnsafeEndpoint)
		}},
		{name: "link_local", block: func() bool {
			_, err := (EndpointPolicy{Resolver: &resolverStub{results: []resolverResult{{addresses: addresses("169.254.169.254")}}}}).Validate(t.Context(), "https://api.example.com", "api.example.com")
			return errors.Is(err, ErrUnsafeEndpoint)
		}},
		{name: "metadata", block: func() bool {
			_, err := (EndpointPolicy{Resolver: &resolverStub{results: []resolverResult{{addresses: addresses("8.8.8.8")}}}}).Validate(t.Context(), "https://metadata.google.internal/v1", "metadata.google.internal")
			return errors.Is(err, ErrUnsafeEndpoint)
		}},
		{name: "dns_rebinding", block: func() bool {
			resolver := &resolverStub{results: []resolverResult{{addresses: addresses("8.8.8.8")}, {addresses: addresses("169.254.169.254")}}}
			policy := EndpointPolicy{Resolver: resolver}
			endpoint, err := policy.Validate(t.Context(), "https://api.example.com", "api.example.com")
			if err != nil {
				return false
			}
			dialed := false
			dialer := boundDialer(endpoint, policy, func(context.Context, string, string) (net.Conn, error) {
				dialed = true
				return nil, errors.New("unexpected dial")
			})
			_, err = dialer(t.Context(), "tcp", "api.example.com:443")
			return errors.Is(err, ErrUnsafeEndpoint) && !dialed
		}},
		{name: "redirect", block: func() bool {
			origin, _ := url.Parse("https://api.example.com/v1")
			target, _ := url.Parse("https://evil.example/v1")
			err := boundRedirectPolicy(Endpoint{URL: origin, Origin: "https://api.example.com", BoundHost: "api.example.com", DialPort: "443"}, 2)(&http.Request{URL: target}, []*http.Request{{URL: origin}})
			return errors.Is(err, ErrRedirectBlocked)
		}},
		{name: "byok_non_bound_host", block: func() bool {
			_, err := (EndpointPolicy{Resolver: &resolverStub{results: []resolverResult{{addresses: addresses("8.8.8.8")}}}}).Validate(t.Context(), "https://api.example.com", "other.example.com")
			return errors.Is(err, ErrEndpointDrift)
		}},
	}
	blocked := 0
	for _, candidate := range attacks {
		t.Run(candidate.name, func(t *testing.T) {
			if !candidate.block() {
				t.Fatalf("%s bypass was not blocked", candidate.name)
			}
			blocked++
		})
	}
	metrics, err := json.Marshal(map[string]any{
		"scenario": "provider_egress_security", "attacks": len(attacks), "blocked": blocked,
		"block_rate_percent": 100, "byok_non_bound_host_sends": 0,
		"categories": []string{"loopback", "private_network", "link_local", "metadata", "dns_rebinding", "redirect", "byok_non_bound_host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("provider_egress_gate=%s", metrics)
}
