package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/langshift/lites/internal/gateway"
	"github.com/langshift/lites/internal/security/trustedcontext"
)

var gatewayKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type config struct {
	databaseURL, databaseURLFile, listenAddress, healthAddress                            string
	serverCertificateFile, serverKeyFile                                                  string
	identityURL, identityCAFile, identityCertFile, identityKeyFile, identityTLSServerName string
	realtimeURL, realtimeCAFile, realtimeCertFile, realtimeKeyFile, realtimeTLSServerName string
	behaviorURL, behaviorCAFile, behaviorCertFile, behaviorKeyFile, behaviorTLSServerName string
	agentURL, agentCAFile, agentCertFile, agentKeyFile, agentTLSServerName                string
	productURL, productCAFile, productCertFile, productKeyFile, productTLSServerName      string
	secretBundleFile, trustedIssuer, trustedAudience, realtimeTrustedAudience             string
	behaviorTrustedAudience, agentTrustedAudience, productTrustedAudience                 string
	trustedKeyringFile                                                                    string
	publicOrigins, trustedProxyCIDRs                                                      []string
	environment, serviceVersion, region                                                   string
	otlpEndpoint, otlpCAFile, otlpCertFile, otlpKeyFile, otlpTLSName, otlpBearerTokenFile string
	allowInsecureDevelopment                                                              bool
	databaseMaxConnections                                                                int
	trustedContextTTL                                                                     time.Duration
	traceSampleRatio                                                                      float64
}

type gatewaySecrets struct {
	SigningKey, SessionPepper, CSRFPepper, FingerprintPepper    []byte
	AnonymousHandleKey, AnonymousHandlePepper, AnonymousCSRFKey []byte
	SigningKeyID, AnonymousHandleKeyID                          string
	SigningNotBefore, SigningNotAfter                           time.Time
}

type encodedGatewaySecrets struct {
	Version                     string    `json:"version"`
	TrustedContextKeyID         string    `json:"trusted_context_key_id"`
	TrustedContextPrivateKey    string    `json:"trusted_context_private_key"`
	TrustedContextNotBefore     time.Time `json:"trusted_context_not_before"`
	TrustedContextNotAfter      time.Time `json:"trusted_context_not_after"`
	SessionPepper               string    `json:"session_pepper"`
	CSRFPepper                  string    `json:"csrf_pepper"`
	FingerprintPepper           string    `json:"fingerprint_pepper"`
	AnonymousHandleKeyID        string    `json:"anonymous_handle_key_id"`
	AnonymousHandleSigningKey   string    `json:"anonymous_handle_signing_key"`
	AnonymousHandleDigestPepper string    `json:"anonymous_handle_digest_pepper"`
	AnonymousCSRFKey            string    `json:"anonymous_csrf_key"`
}

func loadConfig() (config, error) {
	allowInsecure, err := optionalBool("ALLOW_INSECURE_DEVELOPMENT", false)
	if err != nil {
		return config{}, err
	}
	maxConnections, err := optionalInt("DATABASE_MAX_CONNECTIONS", 64)
	if err != nil {
		return config{}, err
	}
	contextTTL, err := optionalDuration("TRUSTED_CONTEXT_TTL", 2*time.Minute)
	if err != nil {
		return config{}, err
	}
	traceRatio, err := optionalFloat("TRACE_SAMPLE_RATIO", 0.1)
	if err != nil {
		return config{}, err
	}
	databaseURLFile := os.Getenv("DATABASE_URL_FILE")
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURLFile != "" {
		if databaseURL != "" {
			return config{}, errors.New("DATABASE_URL and DATABASE_URL_FILE are mutually exclusive")
		}
		databaseURL, err = readSecret(databaseURLFile, 8192)
		if err != nil {
			return config{}, errors.New("DATABASE_URL_FILE is unreadable")
		}
	}
	value := config{
		databaseURL: databaseURL, databaseURLFile: databaseURLFile, databaseMaxConnections: maxConnections, listenAddress: envString("LISTEN_ADDRESS", ":8443"), healthAddress: envString("HEALTH_ADDRESS", "127.0.0.1:8080"), serverCertificateFile: os.Getenv("SERVER_TLS_CERT_FILE"), serverKeyFile: os.Getenv("SERVER_TLS_KEY_FILE"),
		identityURL: os.Getenv("IDENTITY_UPSTREAM_URL"), identityCAFile: os.Getenv("IDENTITY_UPSTREAM_ROOT_CA_FILE"), identityCertFile: os.Getenv("IDENTITY_UPSTREAM_CLIENT_CERT_FILE"), identityKeyFile: os.Getenv("IDENTITY_UPSTREAM_CLIENT_KEY_FILE"), identityTLSServerName: os.Getenv("IDENTITY_UPSTREAM_TLS_SERVER_NAME"),
		realtimeURL: os.Getenv("REALTIME_UPSTREAM_URL"), realtimeCAFile: os.Getenv("REALTIME_UPSTREAM_ROOT_CA_FILE"), realtimeCertFile: os.Getenv("REALTIME_UPSTREAM_CLIENT_CERT_FILE"), realtimeKeyFile: os.Getenv("REALTIME_UPSTREAM_CLIENT_KEY_FILE"), realtimeTLSServerName: os.Getenv("REALTIME_UPSTREAM_TLS_SERVER_NAME"),
		behaviorURL: os.Getenv("BEHAVIOR_UPSTREAM_URL"), behaviorCAFile: os.Getenv("BEHAVIOR_UPSTREAM_ROOT_CA_FILE"), behaviorCertFile: os.Getenv("BEHAVIOR_UPSTREAM_CLIENT_CERT_FILE"), behaviorKeyFile: os.Getenv("BEHAVIOR_UPSTREAM_CLIENT_KEY_FILE"), behaviorTLSServerName: os.Getenv("BEHAVIOR_UPSTREAM_TLS_SERVER_NAME"),
		agentURL: os.Getenv("AGENT_UPSTREAM_URL"), agentCAFile: os.Getenv("AGENT_UPSTREAM_ROOT_CA_FILE"), agentCertFile: os.Getenv("AGENT_UPSTREAM_CLIENT_CERT_FILE"), agentKeyFile: os.Getenv("AGENT_UPSTREAM_CLIENT_KEY_FILE"), agentTLSServerName: os.Getenv("AGENT_UPSTREAM_TLS_SERVER_NAME"),
		productURL: os.Getenv("PRODUCT_UPSTREAM_URL"), productCAFile: os.Getenv("PRODUCT_UPSTREAM_ROOT_CA_FILE"), productCertFile: os.Getenv("PRODUCT_UPSTREAM_CLIENT_CERT_FILE"), productKeyFile: os.Getenv("PRODUCT_UPSTREAM_CLIENT_KEY_FILE"), productTLSServerName: os.Getenv("PRODUCT_UPSTREAM_TLS_SERVER_NAME"),
		secretBundleFile: os.Getenv("GATEWAY_SECRET_BUNDLE_FILE"), trustedKeyringFile: os.Getenv("TRUSTED_CONTEXT_KEYRING_FILE"), trustedIssuer: envString("TRUSTED_CONTEXT_ISSUER", "lites-gateway"), trustedAudience: envString("TRUSTED_CONTEXT_AUDIENCE", "identity-service"), realtimeTrustedAudience: envString("REALTIME_TRUSTED_CONTEXT_AUDIENCE", "realtime-gateway"), behaviorTrustedAudience: envString("BEHAVIOR_TRUSTED_CONTEXT_AUDIENCE", "behavior-control-plane"), agentTrustedAudience: envString("AGENT_TRUSTED_CONTEXT_AUDIENCE", "agent-control-plane"), productTrustedAudience: envString("PRODUCT_TRUSTED_CONTEXT_AUDIENCE", "product-service"), trustedContextTTL: contextTTL, publicOrigins: splitNonempty(os.Getenv("PUBLIC_ORIGINS")), trustedProxyCIDRs: splitNonempty(os.Getenv("TRUSTED_PROXY_CIDRS")), allowInsecureDevelopment: allowInsecure,
		environment: os.Getenv("LITES_ENVIRONMENT"), serviceVersion: os.Getenv("LITES_VERSION"), region: os.Getenv("LITES_REGION"), otlpEndpoint: os.Getenv("OTLP_GRPC_ENDPOINT"), otlpCAFile: os.Getenv("OTLP_ROOT_CA_FILE"), otlpCertFile: os.Getenv("OTLP_CLIENT_CERT_FILE"), otlpKeyFile: os.Getenv("OTLP_CLIENT_KEY_FILE"), otlpTLSName: os.Getenv("OTLP_TLS_SERVER_NAME"), otlpBearerTokenFile: os.Getenv("OTLP_BEARER_TOKEN_FILE"), traceSampleRatio: traceRatio,
	}
	return value, value.validate()
}

func (value config) validate() error {
	if !validUpstreamURL(value.identityURL, value.allowInsecureDevelopment) {
		return errors.New("IDENTITY_UPSTREAM_URL is invalid")
	}
	if !validUpstreamURL(value.realtimeURL, value.allowInsecureDevelopment) {
		return errors.New("REALTIME_UPSTREAM_URL is invalid")
	}
	if !validUpstreamURL(value.behaviorURL, value.allowInsecureDevelopment) {
		return errors.New("BEHAVIOR_UPSTREAM_URL is invalid")
	}
	if !validUpstreamURL(value.agentURL, value.allowInsecureDevelopment) {
		return errors.New("AGENT_UPSTREAM_URL is invalid")
	}
	if !validUpstreamURL(value.productURL, value.allowInsecureDevelopment) {
		return errors.New("PRODUCT_UPSTREAM_URL is invalid")
	}
	audiences := []string{value.trustedAudience, value.realtimeTrustedAudience, value.behaviorTrustedAudience, value.agentTrustedAudience, value.productTrustedAudience}
	if value.databaseURL == "" || value.listenAddress == "" || value.healthAddress == "" || value.secretBundleFile == "" || value.trustedKeyringFile == "" || value.trustedIssuer == "" || !allDistinctNonempty(audiences) || value.trustedContextTTL <= 0 || value.trustedContextTTL > 5*time.Minute || len(value.publicOrigins) == 0 || value.environment == "" || value.serviceVersion == "" || value.region == "" || value.databaseMaxConnections < 8 || value.databaseMaxConnections > 512 || value.traceSampleRatio < 0 || value.traceSampleRatio > 1 || (value.serverCertificateFile == "") != (value.serverKeyFile == "") || (value.identityCertFile == "") != (value.identityKeyFile == "") || (value.realtimeCertFile == "") != (value.realtimeKeyFile == "") || (value.behaviorCertFile == "") != (value.behaviorKeyFile == "") || (value.agentCertFile == "") != (value.agentKeyFile == "") || (value.productCertFile == "") != (value.productKeyFile == "") || (value.otlpCertFile == "") != (value.otlpKeyFile == "") {
		return errors.New("required gateway configuration is missing or invalid")
	}
	if _, err := gateway.ParseTrustedProxyCIDRs(value.trustedProxyCIDRs); err != nil {
		return errors.New("TRUSTED_PROXY_CIDRS contains an invalid network")
	}
	seen := map[string]struct{}{}
	for _, origin := range value.publicOrigins {
		parsed, parseErr := url.Parse(origin)
		if parseErr != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || origin != parsed.Scheme+"://"+parsed.Host {
			return errors.New("PUBLIC_ORIGINS contains an invalid origin")
		}
		if _, exists := seen[origin]; exists {
			return errors.New("PUBLIC_ORIGINS contains a duplicate")
		}
		seen[origin] = struct{}{}
	}
	if !value.allowInsecureDevelopment && (value.databaseURLFile == "" || value.serverCertificateFile == "" || value.identityCAFile == "" || value.identityCertFile == "" || value.identityTLSServerName == "" || value.realtimeCAFile == "" || value.realtimeCertFile == "" || value.realtimeTLSServerName == "" || value.behaviorCAFile == "" || value.behaviorCertFile == "" || value.behaviorTLSServerName == "" || value.agentCAFile == "" || value.agentCertFile == "" || value.agentTLSServerName == "" || value.productCAFile == "" || value.productCertFile == "" || value.productTLSServerName == "" || value.otlpEndpoint == "" || (value.otlpBearerTokenFile == "" && value.otlpCertFile == "")) {
		return errors.New("production requires file-backed database credentials, TLS, upstream mTLS, and authenticated telemetry")
	}
	return nil
}

func verifySigningKeyring(path string, secrets gatewaySecrets, now time.Time, ttl time.Duration) error {
	keys, windows, err := trustedcontext.LoadPublicKeyring(path)
	if err != nil {
		return errors.New("trusted context public keyring is unavailable or invalid")
	}
	publicKey, found := keys[secrets.SigningKeyID]
	window, windowFound := windows[secrets.SigningKeyID]
	derived := ed25519.PrivateKey(secrets.SigningKey).Public().(ed25519.PublicKey)
	if !found || !windowFound || !hmac.Equal(publicKey, derived) || !window.NotBefore.Equal(secrets.SigningNotBefore) || !window.NotAfter.Equal(secrets.SigningNotAfter) || now.Before(window.NotBefore) || !window.NotAfter.After(now.Add(ttl)) {
		return errors.New("gateway signing key does not match the trusted context public keyring")
	}
	return nil
}

func loadGatewaySecrets(path string, now time.Time, ttl time.Duration) (gatewaySecrets, error) {
	file, err := os.Open(path)
	if err != nil {
		return gatewaySecrets{}, errors.New("gateway secret bundle is unavailable")
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil || len(encoded) > 64*1024 {
		return gatewaySecrets{}, errors.New("gateway secret bundle is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var source encodedGatewaySecrets
	if err = decoder.Decode(&source); err != nil {
		return gatewaySecrets{}, errors.New("gateway secret bundle is invalid")
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || source.Version != "1.0.0" || !gatewayKeyIDPattern.MatchString(source.TrustedContextKeyID) || !gatewayKeyIDPattern.MatchString(source.AnonymousHandleKeyID) || source.TrustedContextNotBefore.IsZero() || source.TrustedContextNotAfter.IsZero() || source.TrustedContextNotAfter.Sub(source.TrustedContextNotBefore) > 400*24*time.Hour || now.Before(source.TrustedContextNotBefore) || !source.TrustedContextNotAfter.After(now.Add(ttl)) {
		return gatewaySecrets{}, errors.New("gateway secret bundle is invalid")
	}
	privateKey, err := base64.StdEncoding.DecodeString(source.TrustedContextPrivateKey)
	if err != nil || len(privateKey) != ed25519.PrivateKeySize {
		return gatewaySecrets{}, errors.New("gateway signing key is invalid")
	}
	encodedValues := []string{source.SessionPepper, source.CSRFPepper, source.FingerprintPepper, source.AnonymousHandleSigningKey, source.AnonymousHandleDigestPepper, source.AnonymousCSRFKey}
	values := make([][]byte, len(encodedValues))
	seen := map[string]struct{}{}
	for index, encodedValue := range encodedValues {
		values[index], err = base64.StdEncoding.DecodeString(encodedValue)
		if err != nil || len(values[index]) != 32 {
			return gatewaySecrets{}, errors.New("gateway purpose secret is invalid")
		}
		fingerprint := base64.StdEncoding.EncodeToString(values[index])
		if _, exists := seen[fingerprint]; exists {
			return gatewaySecrets{}, errors.New("gateway purpose secrets must be distinct")
		}
		seen[fingerprint] = struct{}{}
	}
	return gatewaySecrets{SigningKey: append([]byte(nil), privateKey...), SigningKeyID: source.TrustedContextKeyID, SigningNotBefore: source.TrustedContextNotBefore.UTC(), SigningNotAfter: source.TrustedContextNotAfter.UTC(), SessionPepper: values[0], CSRFPepper: values[1], FingerprintPepper: values[2], AnonymousHandleKeyID: source.AnonymousHandleKeyID, AnonymousHandleKey: values[3], AnonymousHandlePepper: values[4], AnonymousCSRFKey: values[5]}, nil
}

func (value config) upstreamClient() (*http.Client, *url.URL, error) {
	return configuredUpstreamClient(value.identityURL, value.identityCAFile, value.identityCertFile, value.identityKeyFile, value.identityTLSServerName, 35*time.Second)
}

func (value config) realtimeUpstreamClient() (*http.Client, *url.URL, error) {
	return configuredUpstreamClient(value.realtimeURL, value.realtimeCAFile, value.realtimeCertFile, value.realtimeKeyFile, value.realtimeTLSServerName, 0)
}

func (value config) behaviorUpstreamClient() (*http.Client, *url.URL, error) {
	return configuredUpstreamClient(value.behaviorURL, value.behaviorCAFile, value.behaviorCertFile, value.behaviorKeyFile, value.behaviorTLSServerName, 35*time.Second)
}

func (value config) agentUpstreamClient() (*http.Client, *url.URL, error) {
	return configuredUpstreamClient(value.agentURL, value.agentCAFile, value.agentCertFile, value.agentKeyFile, value.agentTLSServerName, 45*time.Second)
}

func (value config) productUpstreamClient() (*http.Client, *url.URL, error) {
	return configuredUpstreamClient(value.productURL, value.productCAFile, value.productCertFile, value.productKeyFile, value.productTLSServerName, 35*time.Second)
}

func allDistinctNonempty(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func configuredUpstreamClient(rawURL, caFile, certFile, keyFile, tlsServerName string, timeout time.Duration) (*http.Client, *url.URL, error) {
	endpoint, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, errors.New("upstream URL is invalid")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: tlsServerName}
	if caFile != "" {
		contents, readErr := os.ReadFile(caFile)
		if readErr != nil {
			return nil, nil, errors.New("upstream CA is unavailable")
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(contents) {
			return nil, nil, errors.New("upstream CA is invalid")
		}
		tlsConfig.RootCAs = roots
	}
	if certFile != "" {
		certificate, loadErr := tls.LoadX509KeyPair(certFile, keyFile)
		if loadErr != nil {
			return nil, nil, errors.New("upstream client certificate is invalid")
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	transport.MaxIdleConns = 256
	transport.MaxIdleConnsPerHost = 128
	transport.IdleConnTimeout = 60 * time.Second
	transport.ResponseHeaderTimeout = 10 * time.Second
	transport.ForceAttemptHTTP2 = true
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, endpoint, nil
}

func validUpstreamURL(raw string, allowInsecure bool) bool {
	upstream, err := url.Parse(raw)
	loopbackHTTP := err == nil && allowInsecure && upstream.Scheme == "http" && isLoopback(upstream.Hostname())
	return err == nil && upstream.Host != "" && upstream.User == nil && upstream.RawQuery == "" && upstream.Fragment == "" && (upstream.Path == "" || upstream.Path == "/") && (upstream.Scheme == "https" || loopbackHTTP)
}

func serverTLS(value config) (*tls.Config, error) {
	if value.allowInsecureDevelopment && value.serverCertificateFile == "" {
		return nil, nil
	}
	certificate, err := tls.LoadX509KeyPair(value.serverCertificateFile, value.serverKeyFile)
	if err != nil {
		return nil, errors.New("gateway TLS certificate is invalid")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}}, nil
}

func readSecret(path string, limit int64) (string, error) {
	if path == "" {
		return "", errors.New("secret path is empty")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, limit+1))
	value := strings.TrimSpace(string(contents))
	if err != nil || int64(len(contents)) > limit || value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("secret is empty, ambiguous, or too large")
	}
	return value, nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func envString(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func splitNonempty(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
func optionalBool(name string, fallback bool) (bool, error) {
	value, found := os.LookupEnv(name)
	if !found {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	return parsed, nil
}
func optionalInt(name string, fallback int) (int, error) {
	value, found := os.LookupEnv(name)
	if !found {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return parsed, nil
}
func optionalFloat(name string, fallback float64) (float64, error) {
	value, found := os.LookupEnv(name)
	if !found {
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number", name)
	}
	return parsed, nil
}
func optionalDuration(name string, fallback time.Duration) (time.Duration, error) {
	value, found := os.LookupEnv(name)
	if !found {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration", name)
	}
	return parsed, nil
}
