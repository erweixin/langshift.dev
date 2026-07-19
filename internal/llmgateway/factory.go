// Package llmgateway composes immutable provider registry entries with the
// endpoint-bound egress broker and protocol adapters.
package llmgateway

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"time"

	"github.com/langshift/lites/internal/llmgateway/egress"
	"github.com/langshift/lites/internal/llmgateway/provider"
)

var ErrFactoryConfiguration = errors.New("LLM gateway factory configuration is invalid")

type ClientFactory struct {
	Policy        egress.EndpointPolicy
	Secrets       egress.SecretSource
	DialContext   egress.DialContext
	BaseTransport http.RoundTripper
	RootCAs       *x509.CertPool
}

func (factory ClientFactory) Build(ctx context.Context, resolved provider.ResolvedProvider, credential provider.ManagedCredential) (provider.Client, error) {
	if ctx == nil || factory.Policy.Resolver == nil || factory.Secrets == nil {
		return nil, ErrFactoryConfiguration
	}
	endpoint, err := factory.Policy.Validate(ctx, resolved.Provider.Endpoint, resolved.Provider.BoundHost)
	if err != nil {
		return nil, err
	}
	header, prefix := "", ""
	switch credential.Mode {
	case "bearer":
		header, prefix = "Authorization", "Bearer "
	case "x-api-key":
		header = "X-Api-Key"
	default:
		return nil, ErrFactoryConfiguration
	}
	return egress.NewBroker(ctx, egress.ClientConfig{
		Endpoint: endpoint, Policy: factory.Policy, Secrets: factory.Secrets,
		Credential:  egress.CredentialBinding{SecretRef: credential.SecretRef, SecretVersion: credential.SecretVersion, HeaderName: header, ValuePrefix: prefix},
		DialContext: factory.DialContext, BaseTransport: factory.BaseTransport, RootCAs: factory.RootCAs,
		RequestTimeout: resolved.Provider.RequestTimeout, DialTimeout: 10 * time.Second,
		TLSHandshake: 10 * time.Second, ResponseHeader: 90 * time.Second, IdleConnTimeout: 90 * time.Second,
		MaximumRedirects: 0, MaximumBodyBytes: resolved.Provider.MaximumResponseBytes,
	})
}

func NewAdapter(resolved provider.ResolvedProvider) (provider.Adapter, error) {
	maximumEvent := int(resolved.Provider.MaximumResponseBytes)
	if maximumEvent > 16<<20 {
		maximumEvent = 16 << 20
	}
	switch resolved.Provider.Type {
	case "openai", "openai_compatible":
		return provider.NewOpenAI(provider.OpenAIConfig{MaximumRequestBytes: resolved.Provider.MaximumRequestBytes, MaximumEventBytes: maximumEvent, MaximumTextBytes: int(resolved.Provider.MaximumResponseBytes)})
	case "anthropic":
		return provider.NewAnthropic(provider.AnthropicConfig{APIVersion: resolved.Provider.AnthropicVersion, MaximumRequestBytes: resolved.Provider.MaximumRequestBytes, MaximumEventBytes: maximumEvent, MaximumTextBytes: int(resolved.Provider.MaximumResponseBytes)})
	default:
		return nil, ErrFactoryConfiguration
	}
}
