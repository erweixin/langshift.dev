package egress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrInvalidCredential = errors.New("provider credential binding is invalid")
	ErrRedirectBlocked   = errors.New("provider redirect is blocked")
	ErrResponseTooLarge  = errors.New("provider response exceeded its size limit")
)

// SendError tells the durable dispatch protocol whether a failed HTTP call may
// already have reached the provider. The cause remains sanitized by the layer
// which owns it.
type SendError struct {
	MayHaveSent bool
	Class       string
	Cause       error
}

func (sendError *SendError) Error() string { return "provider egress failed" }
func (sendError *SendError) Unwrap() error { return sendError.Cause }
func (sendError *SendError) RequestMayHaveBeenSent() bool {
	return sendError != nil && sendError.MayHaveSent
}
func (sendError *SendError) ProviderFailureClass() string {
	if sendError == nil {
		return ""
	}
	return sendError.Class
}

type SecretSource interface {
	Resolve(context.Context, string, string) ([]byte, error)
}

type CredentialBinding struct {
	SecretRef, SecretVersion string
	HeaderName, ValuePrefix  string
}

type DialContext func(context.Context, string, string) (net.Conn, error)

type Broker struct {
	endpoint Endpoint
	client   *http.Client
}

type ClientConfig struct {
	Endpoint         Endpoint
	Policy           EndpointPolicy
	Secrets          SecretSource
	Credential       CredentialBinding
	DialContext      DialContext
	RequestTimeout   time.Duration
	DialTimeout      time.Duration
	TLSHandshake     time.Duration
	ResponseHeader   time.Duration
	IdleConnTimeout  time.Duration
	MaximumRedirects int
	MaximumBodyBytes int64
	BaseTransport    http.RoundTripper
	RootCAs          *x509.CertPool
}

func NewBroker(ctx context.Context, config ClientConfig) (*Broker, error) {
	if ctx == nil || config.Endpoint.URL == nil || config.Endpoint.BoundHost == "" || config.Endpoint.Origin == "" || config.Endpoint.DialPort == "" || config.Policy.Resolver == nil || config.Secrets == nil || !validCredential(config.Credential) {
		return nil, ErrInvalidCredential
	}
	revalidated, err := config.Policy.Validate(ctx, config.Endpoint.URL.String(), config.Endpoint.BoundHost)
	if err != nil || revalidated.Origin != config.Endpoint.Origin || revalidated.DialPort != config.Endpoint.DialPort || revalidated.RequestURL != config.Endpoint.RequestURL {
		return nil, ErrUnsafeEndpoint
	}
	if config.RequestTimeout <= 0 || config.RequestTimeout > 5*time.Minute || config.MaximumRedirects < 0 || config.MaximumRedirects > 5 || config.MaximumBodyBytes < 1 || config.MaximumBodyBytes > 64<<20 {
		return nil, ErrInvalidCredential
	}
	transport := config.BaseTransport
	if transport == nil {
		if config.DialTimeout <= 0 || config.DialTimeout > time.Minute || config.TLSHandshake <= 0 || config.TLSHandshake > time.Minute || config.ResponseHeader <= 0 || config.ResponseHeader > 2*time.Minute || config.IdleConnTimeout <= 0 || config.IdleConnTimeout > 10*time.Minute {
			return nil, ErrInvalidCredential
		}
		dial := config.DialContext
		if dial == nil {
			dialer := &net.Dialer{Timeout: config.DialTimeout, KeepAlive: 30 * time.Second}
			dial = dialer.DialContext
		}
		transport = &http.Transport{
			Proxy:                  nil,
			DialContext:            boundDialer(config.Endpoint, config.Policy, dial),
			ForceAttemptHTTP2:      true,
			MaxIdleConns:           32,
			MaxIdleConnsPerHost:    8,
			IdleConnTimeout:        config.IdleConnTimeout,
			TLSHandshakeTimeout:    config.TLSHandshake,
			ResponseHeaderTimeout:  config.ResponseHeader,
			ExpectContinueTimeout:  time.Second,
			MaxResponseHeaderBytes: 1 << 20,
			TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, ServerName: config.Endpoint.BoundHost, RootCAs: config.RootCAs},
		}
	}
	authenticated := boundCredentialTransport{next: transport, endpoint: config.Endpoint, secrets: config.Secrets, credential: config.Credential, maximumBodyBytes: config.MaximumBodyBytes}
	client := &http.Client{Transport: authenticated, Timeout: config.RequestTimeout}
	client.CheckRedirect = boundRedirectPolicy(config.Endpoint, config.MaximumRedirects)
	return &Broker{endpoint: config.Endpoint, client: client}, nil
}

func (broker *Broker) Do(ctx context.Context, method, relativePath string, headers http.Header, body io.Reader) (*http.Response, error) {
	if broker == nil || broker.client == nil || broker.endpoint.URL == nil || ctx == nil || !validMethod(method) {
		return nil, ErrInvalidEndpoint
	}
	reference, err := url.Parse(relativePath)
	if err != nil || reference.IsAbs() || reference.Host != "" || reference.User != nil || reference.Fragment != "" || strings.HasPrefix(relativePath, "//") {
		return nil, ErrEndpointDrift
	}
	target := broker.endpoint.URL.ResolveReference(reference)
	if !sameOrigin(broker.endpoint, target) {
		return nil, ErrEndpointDrift
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, ErrInvalidEndpoint
	}
	request.Header = cloneHeader(headers)
	for _, sensitive := range []string{"authorization", "x-api-key", "x-goog-api-key", "proxy-authorization"} {
		if request.Header.Get(sensitive) != "" {
			return nil, ErrInvalidCredential
		}
	}
	return broker.client.Do(request)
}

type boundCredentialTransport struct {
	next             http.RoundTripper
	endpoint         Endpoint
	secrets          SecretSource
	credential       CredentialBinding
	maximumBodyBytes int64
}

func (transport boundCredentialTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || !sameOrigin(transport.endpoint, request.URL) {
		return nil, ErrEndpointDrift
	}
	credential, err := transport.secrets.Resolve(request.Context(), transport.credential.SecretRef, transport.credential.SecretVersion)
	if err != nil || len(credential) < 8 || len(credential) > 16<<10 {
		clear(credential)
		return nil, &SendError{MayHaveSent: false, Class: "auth_failed", Cause: ErrInvalidCredential}
	}
	defer clear(credential)
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set(transport.credential.HeaderName, transport.credential.ValuePrefix+string(credential))
	response, err := transport.next.RoundTrip(clone)
	if err != nil {
		return nil, &SendError{MayHaveSent: true, Cause: err}
	}
	if response != nil && response.Body != nil {
		response.Body = &boundedBody{source: response.Body, remaining: transport.maximumBodyBytes}
	}
	return response, nil
}

type boundedBody struct {
	source    io.ReadCloser
	remaining int64
}

func (body *boundedBody) Read(target []byte) (int, error) {
	if body == nil || body.source == nil {
		return 0, io.EOF
	}
	if body.remaining == 0 {
		var probe [1]byte
		count, err := body.source.Read(probe[:])
		if count > 0 {
			return 0, ErrResponseTooLarge
		}
		return 0, err
	}
	if int64(len(target)) > body.remaining {
		target = target[:body.remaining]
	}
	count, err := body.source.Read(target)
	body.remaining -= int64(count)
	return count, err
}

func (body *boundedBody) Close() error {
	if body == nil || body.source == nil {
		return nil
	}
	return body.source.Close()
}

func boundDialer(endpoint Endpoint, policy EndpointPolicy, dial DialContext) DialContext {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, ErrEndpointDrift
		}
		host, err = canonicalHost(host)
		if err != nil || host != endpoint.BoundHost || port != endpoint.DialPort {
			return nil, ErrEndpointDrift
		}
		addresses, err := policy.Resolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(addresses) == 0 {
			return nil, ErrUnsafeEndpoint
		}
		if err = policy.validateAddresses(host, addresses); err != nil {
			return nil, err
		}
		var failures []error
		for _, candidate := range addresses {
			candidate = candidate.Unmap()
			connection, dialErr := dial(ctx, network, net.JoinHostPort(candidate.String(), port))
			if dialErr == nil {
				return connection, nil
			}
			failures = append(failures, dialErr)
		}
		return nil, fmt.Errorf("provider endpoint dial failed: %w", errors.Join(failures...))
	}
}

func boundRedirectPolicy(endpoint Endpoint, maximum int) func(*http.Request, []*http.Request) error {
	return func(request *http.Request, previous []*http.Request) error {
		if request == nil || request.URL == nil || !sameOrigin(endpoint, request.URL) {
			return ErrRedirectBlocked
		}
		if len(previous) > maximum {
			return ErrRedirectBlocked
		}
		return nil
	}
}

func sameOrigin(endpoint Endpoint, candidate *url.URL) bool {
	if candidate == nil || candidate.Scheme != "https" || candidate.User != nil {
		return false
	}
	host, err := canonicalHost(candidate.Hostname())
	if err != nil || host != endpoint.BoundHost {
		return false
	}
	port := candidate.Port()
	if port == "" {
		port = "443"
	}
	return port == endpoint.DialPort
}

func validCredential(binding CredentialBinding) bool {
	if binding.SecretRef == "" || binding.SecretVersion == "" {
		return false
	}
	switch http.CanonicalHeaderKey(binding.HeaderName) {
	case "Authorization":
		return binding.ValuePrefix == "Bearer "
	case "X-Api-Key", "X-Goog-Api-Key":
		return binding.ValuePrefix == ""
	default:
		return false
	}
}

func validMethod(method string) bool {
	return method == http.MethodPost || method == http.MethodGet || method == http.MethodDelete
}

func cloneHeader(source http.Header) http.Header {
	if source == nil {
		return make(http.Header)
	}
	return source.Clone()
}
