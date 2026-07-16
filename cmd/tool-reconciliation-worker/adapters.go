package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"time"

	"github.com/langshift/lites/internal/toolreconciler"
)

type adapterEnvelope struct {
	SchemaVersion int             `json:"schema_version"`
	Handlers      []adapterConfig `json:"handlers"`
}

type adapterConfig struct {
	Name                  string `json:"name"`
	Endpoint              string `json:"endpoint"`
	RootCAFile            string `json:"root_ca_file"`
	ClientCertificateFile string `json:"client_certificate_file,omitempty"`
	ClientKeyFile         string `json:"client_key_file,omitempty"`
	TLSServerName         string `json:"tls_server_name"`
	BearerTokenFile       string `json:"bearer_token_file,omitempty"`
}

func loadLookupRegistrations(path string, timeout time.Duration, maximumResponse int) ([]toolreconciler.LookupRegistration, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("open reconciliation adapter inventory")
	}
	defer file.Close()
	var envelope adapterEnvelope
	decoder := json.NewDecoder(io.LimitReader(file, 4<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(&struct{}{}) != io.EOF || envelope.SchemaVersion != 1 || len(envelope.Handlers) > 4096 {
		return nil, errors.New("reconciliation adapter inventory is invalid")
	}
	namePattern := regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,127}$`)
	seen := map[string]struct{}{}
	registrations := make([]toolreconciler.LookupRegistration, 0, len(envelope.Handlers))
	for _, adapter := range envelope.Handlers {
		endpoint, parseErr := url.Parse(adapter.Endpoint)
		if parseErr != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || !namePattern.MatchString(adapter.Name) || adapter.RootCAFile == "" || adapter.TLSServerName == "" || (adapter.ClientCertificateFile == "") != (adapter.ClientKeyFile == "") || adapter.ClientCertificateFile == "" && adapter.BearerTokenFile == "" {
			return nil, errors.New("reconciliation adapter entry is invalid")
		}
		if _, duplicate := seen[adapter.Name]; duplicate {
			return nil, errors.New("duplicate reconciliation adapter")
		}
		seen[adapter.Name] = struct{}{}
		client, clientErr := adapterHTTPClient(adapter)
		if clientErr != nil {
			return nil, clientErr
		}
		token := ""
		if adapter.BearerTokenFile != "" {
			token, clientErr = readSecret(adapter.BearerTokenFile, 8192)
			if clientErr != nil {
				return nil, errors.New("read reconciliation adapter credential")
			}
		}
		registrations = append(registrations, toolreconciler.LookupRegistration{Name: adapter.Name, Lookup: toolreconciler.HTTPLookup{Endpoint: endpoint, Client: client, BearerToken: token, MaximumResponse: int64(maximumResponse), Timeout: timeout}})
	}
	return registrations, nil
}

func adapterHTTPClient(adapter adapterConfig) (*http.Client, error) {
	tlsConfiguration := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: adapter.TLSServerName}
	contents, err := os.ReadFile(adapter.RootCAFile)
	if err != nil {
		return nil, errors.New("read reconciliation adapter CA")
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, errors.New("load system certificate pool")
	}
	if !roots.AppendCertsFromPEM(contents) {
		return nil, errors.New("reconciliation adapter CA contains no certificates")
	}
	tlsConfiguration.RootCAs = roots
	if adapter.ClientCertificateFile != "" {
		certificate, certificateErr := tls.LoadX509KeyPair(adapter.ClientCertificateFile, adapter.ClientKeyFile)
		if certificateErr != nil {
			return nil, errors.New("load reconciliation adapter client certificate")
		}
		tlsConfiguration.Certificates = []tls.Certificate{certificate}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfiguration
	transport.MaxIdleConns, transport.MaxIdleConnsPerHost = 128, 32
	transport.ResponseHeaderTimeout = 30 * time.Second
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return client, nil
}
