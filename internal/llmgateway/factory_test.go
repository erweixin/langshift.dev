package llmgateway

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/langshift/lites/internal/llmgateway/egress"
	"github.com/langshift/lites/internal/llmgateway/provider"
)

type resolverStub struct{ calls int }

func (resolver *resolverStub) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	resolver.calls++
	return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
}

type secretsStub struct{ ref, version string }

func (source *secretsStub) Resolve(_ context.Context, ref, version string) ([]byte, error) {
	source.ref, source.version = ref, version
	return []byte("provider-secret"), nil
}

type transportFunc func(*http.Request) (*http.Response, error)

func (function transportFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestFactoryBindsRegistryOriginCredentialAndAdapter(t *testing.T) {
	registry, err := provider.LoadRegistry(strings.NewReader(`{
      "schema_version":1,"snapshot_id":"registry","version":1,"providers":[{
        "id":"openai-primary","type":"openai","status":"active","endpoint":"https://api.openai.com/v1/","bound_host":"api.openai.com","region":"us-east-1",
        "credential":{"mode":"bearer","secret_ref":"lites/providers/openai","secret_version":"4"},
        "request_timeout":"2m","maximum_request_bytes":8388608,"maximum_response_bytes":33554432,
        "models":[{"id":"reasoning","version":"2026-07-01","wire_model":"gpt-5.6-2026-07-01","pricing_version":"price-v1","capabilities":["text"],"maximum_input_tokens":1000000,"maximum_output_tokens":128000,"input_microunits_per_million":1,"output_microunits_per_million":1,"input_credit_units_per_million":1000000,"output_credit_units_per_million":4000000}]
      }]}`))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := registry.Resolve("openai-primary", "reasoning", "2026-07-01", "api.openai.com", "price-v1")
	if err != nil {
		t.Fatal(err)
	}
	credential, _ := resolved.Credential("lites/byok/tenant/credential", "11")
	resolver, secrets := &resolverStub{}, &secretsStub{}
	var received *http.Request
	factory := ClientFactory{Policy: egress.EndpointPolicy{Resolver: resolver}, Secrets: secrets, BaseTransport: transportFunc(func(request *http.Request) (*http.Response, error) {
		received = request
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"resp_1","model":"gpt-5.6-2026-07-01","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`)), Request: request}, nil
	})}
	client, err := factory.Build(t.Context(), resolved, credential)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewAdapter(resolved)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := adapter.Prepare(provider.Request{Model: resolved.Model.WireModel, MaxOutputTokens: 10, Messages: []provider.Message{{Role: "user", Content: []provider.ContentBlock{{Type: "text", Text: "hello"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.ExecutePrepared(t.Context(), client, prepared, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "ok" || received == nil || received.URL.String() != "https://api.openai.com/v1/responses" || received.Header.Get("Authorization") != "Bearer provider-secret" || secrets.ref != "lites/byok/tenant/credential" || secrets.version != "11" || resolver.calls != 2 {
		t.Fatalf("result=%#v request=%#v secrets=%#v resolver=%d", result, received, secrets, resolver.calls)
	}
}
