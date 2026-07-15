package egress

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type secretSourceStub struct {
	value        []byte
	err          error
	ref, version string
	calls        int
}

func (source *secretSourceStub) Resolve(_ context.Context, ref, version string) ([]byte, error) {
	source.calls++
	source.ref, source.version = ref, version
	return append([]byte(nil), source.value...), source.err
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestBrokerInjectsSecretOnlyAfterExactOriginBinding(t *testing.T) {
	resolver := &resolverStub{results: []resolverResult{{addresses: addresses("8.8.8.8")}}}
	policy := EndpointPolicy{Resolver: resolver}
	endpoint, err := policy.Validate(t.Context(), "https://api.example.com/v1/", "api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	secrets := &secretSourceStub{value: []byte("secret-value")}
	var received *http.Request
	base := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		received = request
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: request}, nil
	})
	broker, err := NewBroker(t.Context(), ClientConfig{Endpoint: endpoint, Policy: policy, Secrets: secrets, Credential: CredentialBinding{SecretRef: "vault://tenant/key", SecretVersion: "v7", HeaderName: "Authorization", ValuePrefix: "Bearer "}, RequestTimeout: time.Minute, MaximumRedirects: 2, MaximumBodyBytes: 1024, BaseTransport: base})
	if err != nil {
		t.Fatal(err)
	}
	headers := http.Header{"Content-Type": []string{"application/json"}}
	response, err := broker.Do(t.Context(), http.MethodPost, "chat/completions", headers, bytes.NewBufferString(`{"model":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if received == nil || received.URL.String() != "https://api.example.com/v1/chat/completions" || received.Header.Get("Authorization") != "Bearer secret-value" || secrets.calls != 1 || secrets.ref != "vault://tenant/key" || secrets.version != "v7" {
		t.Fatalf("credential was not exactly bound: request=%#v secret=%#v", received, secrets)
	}
	if headers.Get("Authorization") != "" {
		t.Fatal("broker mutated caller headers with a secret")
	}
	for _, target := range []string{"https://evil.example/v1", "//evil.example/v1", "http://api.example.com/v1", "/v1#fragment"} {
		if _, err = broker.Do(t.Context(), http.MethodPost, target, nil, nil); !errors.Is(err, ErrEndpointDrift) {
			t.Fatalf("target %q escaped origin: %v", target, err)
		}
	}
	if _, err = broker.Do(t.Context(), http.MethodPost, "/v1", http.Header{"X-Api-Key": []string{"caller-secret"}}, nil); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("caller-supplied credential accepted: %v", err)
	}
}

func TestBoundDialerPinsValidatedIPAndBlocksDNSRebinding(t *testing.T) {
	rebinding := &resolverStub{results: []resolverResult{
		{addresses: addresses("8.8.8.8")},
		{addresses: addresses("169.254.169.254")},
	}}
	policy := EndpointPolicy{Resolver: rebinding}
	endpoint, err := policy.Validate(t.Context(), "https://api.example.com/v1", "api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	dialCalls := 0
	dial := boundDialer(endpoint, policy, func(context.Context, string, string) (net.Conn, error) {
		dialCalls++
		return nil, errors.New("must not dial")
	})
	if _, err = dial(t.Context(), "tcp", "api.example.com:443"); !errors.Is(err, ErrUnsafeEndpoint) || dialCalls != 0 {
		t.Fatalf("DNS rebinding was not blocked: calls=%d err=%v", dialCalls, err)
	}

	stable := &resolverStub{results: []resolverResult{{addresses: addresses("8.8.8.8")}}}
	policy = EndpointPolicy{Resolver: stable}
	endpoint, err = policy.Validate(t.Context(), "https://api.example.com", "api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	var dialed string
	dial = boundDialer(endpoint, policy, func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = address
		client, server := net.Pipe()
		_ = server.Close()
		return client, nil
	})
	connection, err := dial(t.Context(), "tcp", "api.example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	if dialed != "8.8.8.8:443" {
		t.Fatalf("dial was not IP-pinned: %q", dialed)
	}
	if _, err = dial(t.Context(), "tcp", "evil.example:443"); !errors.Is(err, ErrEndpointDrift) {
		t.Fatalf("host drift accepted: %v", err)
	}
}

func TestBrokerRedirectPolicyRejectsOriginSchemePortAndLoops(t *testing.T) {
	endpointURL, _ := url.Parse("https://api.example.com/v1")
	endpoint := Endpoint{URL: endpointURL, Origin: "https://api.example.com", BoundHost: "api.example.com", DialPort: "443"}
	policy := boundRedirectPolicy(endpoint, 2)
	previous := []*http.Request{{URL: endpointURL}}
	for _, raw := range []string{"https://evil.example/v1", "http://api.example.com/v1", "https://api.example.com:8443/v1", "https://user@api.example.com/v1"} {
		target, _ := url.Parse(raw)
		if err := policy(&http.Request{URL: target}, previous); !errors.Is(err, ErrRedirectBlocked) {
			t.Fatalf("redirect %q accepted: %v", raw, err)
		}
	}
	same, _ := url.Parse("https://api.example.com/v2")
	if err := policy(&http.Request{URL: same}, previous); err != nil {
		t.Fatalf("same-origin redirect rejected: %v", err)
	}
	if err := policy(&http.Request{URL: same}, []*http.Request{{}, {}, {}}); !errors.Is(err, ErrRedirectBlocked) {
		t.Fatalf("redirect loop accepted: %v", err)
	}
}

func TestBrokerFailsClosedOnSecretResolutionAndResponseSize(t *testing.T) {
	resolver := &resolverStub{results: []resolverResult{{addresses: addresses("8.8.8.8")}}}
	policy := EndpointPolicy{Resolver: resolver}
	endpoint, err := policy.Validate(t.Context(), "https://api.example.com", "api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	base := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("12345")), Request: request}, nil
	})
	secrets := &secretSourceStub{value: []byte("secret-value")}
	broker, err := NewBroker(t.Context(), ClientConfig{Endpoint: endpoint, Policy: policy, Secrets: secrets, Credential: CredentialBinding{SecretRef: "vault://key", SecretVersion: "1", HeaderName: "X-Api-Key"}, RequestTimeout: time.Minute, MaximumBodyBytes: 4, BaseTransport: base})
	if err != nil {
		t.Fatal(err)
	}
	response, err := broker.Do(t.Context(), http.MethodPost, "/v1", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err = io.ReadAll(response.Body); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("oversized response accepted: %v", err)
	}
	secrets.err = errors.New("vault detail")
	if _, err = broker.Do(t.Context(), http.MethodPost, "/v1", nil, nil); !errors.Is(err, ErrInvalidCredential) || strings.Contains(err.Error(), "vault detail") {
		t.Fatalf("secret resolution did not fail closed: %v", err)
	}
}

func TestBrokerConfigurationAllowsOnlyKnownCredentialHeaders(t *testing.T) {
	endpointURL, _ := url.Parse("https://api.example.com")
	endpoint := Endpoint{URL: endpointURL, Origin: "https://api.example.com", BoundHost: "api.example.com", DialPort: "443"}
	base := ClientConfig{Endpoint: endpoint, Policy: EndpointPolicy{Resolver: &resolverStub{results: []resolverResult{{addresses: addresses("8.8.8.8")}}}}, Secrets: &secretSourceStub{value: []byte("secret-value")}, RequestTimeout: time.Minute, MaximumBodyBytes: 1024, BaseTransport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("unused") })}
	for _, credential := range []CredentialBinding{
		{SecretRef: "ref", SecretVersion: "v", HeaderName: "Cookie"},
		{SecretRef: "ref", SecretVersion: "v", HeaderName: "Authorization", ValuePrefix: "Basic "},
		{SecretRef: "", SecretVersion: "v", HeaderName: "X-Goog-Api-Key"},
	} {
		candidate := base
		candidate.Credential = credential
		if _, err := NewBroker(t.Context(), candidate); !errors.Is(err, ErrInvalidCredential) {
			t.Fatalf("invalid credential accepted: %#v err=%v", credential, err)
		}
	}
}
