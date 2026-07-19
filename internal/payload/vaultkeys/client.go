package vaultkeys

import (
	"context"
	"crypto/subtle"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/hashicorp/vault/api"
)

type ClientConfig struct {
	Address                  string
	Namespace                string
	Mount                    string
	TokenFile                string
	CACertificateFile        string
	ClientCertificateFile    string
	ClientKeyFile            string
	TLSServerName            string
	AllowInsecureDevelopment bool
	// AllowLocalCompose permits exactly the Docker Desktop Vault dev service
	// for the engineering gate; all other non-loopback HTTP endpoints fail.
	AllowLocalCompose bool
}

type ClientReader struct {
	Client    *api.Client
	Mount     string
	TokenFile string
}

func (reader *ClientReader) Ready(ctx context.Context) error {
	if reader == nil || reader.Client == nil {
		return ErrKeyUnavailable
	}
	if reader.TokenFile != "" {
		if err := reader.reloadToken(); err != nil {
			return err
		}
	}
	health, err := reader.Client.Sys().HealthWithContext(ctx)
	if err != nil || health == nil || !health.Initialized || health.Sealed || (health.RemovedFromCluster != nil && *health.RemovedFromCluster) {
		return ErrKeyUnavailable
	}
	if reader.TokenFile != "" {
		if _, err = reader.Client.Auth().Token().LookupSelfWithContext(ctx); err != nil {
			return ErrKeyUnavailable
		}
	}
	return nil
}

func NewClientReader(configuration ClientConfig) (*ClientReader, error) {
	address, err := url.Parse(configuration.Address)
	if err != nil || address.Host == "" || address.User != nil || address.RawQuery != "" || address.Fragment != "" || configuration.Mount == "" || strings.ContainsAny(configuration.Mount, "/\\\x00\r\n") || (configuration.ClientCertificateFile == "") != (configuration.ClientKeyFile == "") {
		return nil, ErrKeyUnavailable
	}
	if configuration.AllowInsecureDevelopment {
		if address.Scheme != "http" && address.Scheme != "https" {
			return nil, ErrKeyUnavailable
		}
		if address.Scheme == "http" && !loopback(address.Hostname()) && !(configuration.AllowLocalCompose && address.Hostname() == "vault") {
			return nil, ErrKeyUnavailable
		}
	} else if address.Scheme != "https" || (configuration.TokenFile == "" && configuration.ClientCertificateFile == "") {
		return nil, ErrKeyUnavailable
	}
	vaultConfig := api.DefaultConfig()
	vaultConfig.Address = strings.TrimSuffix(configuration.Address, "/")
	if err = vaultConfig.ConfigureTLS(&api.TLSConfig{CACert: configuration.CACertificateFile, ClientCert: configuration.ClientCertificateFile, ClientKey: configuration.ClientKeyFile, TLSServerName: configuration.TLSServerName}); err != nil {
		return nil, ErrKeyUnavailable
	}
	client, err := api.NewClient(vaultConfig)
	if err != nil {
		return nil, ErrKeyUnavailable
	}
	if configuration.Namespace != "" {
		client.SetNamespace(configuration.Namespace)
	}
	reader := &ClientReader{Client: client, Mount: configuration.Mount, TokenFile: configuration.TokenFile}
	if configuration.TokenFile != "" {
		if err = reader.reloadToken(); err != nil {
			return nil, err
		}
	}
	return reader, nil
}

func (reader *ClientReader) Get(ctx context.Context, path string) (*api.KVSecret, error) {
	if reader == nil || reader.Client == nil || reader.Mount == "" || path == "" {
		return nil, ErrKeyUnavailable
	}
	if reader.TokenFile != "" {
		if err := reader.reloadToken(); err != nil {
			return nil, err
		}
	}
	secret, err := reader.Client.KVv2(reader.Mount).Get(ctx, path)
	if err != nil {
		return nil, ErrKeyUnavailable
	}
	return secret, nil
}

// GetVersion reads an exact KV v2 version. Callers which persist a secret
// version must never fall back to the current value during rotation.
func (reader *ClientReader) GetVersion(ctx context.Context, path string, version int) (*api.KVSecret, error) {
	if reader == nil || reader.Client == nil || reader.Mount == "" || path == "" || version < 1 {
		return nil, ErrKeyUnavailable
	}
	if reader.TokenFile != "" {
		if err := reader.reloadToken(); err != nil {
			return nil, err
		}
	}
	secret, err := reader.Client.KVv2(reader.Mount).GetVersion(ctx, path, version)
	if err != nil {
		return nil, ErrKeyUnavailable
	}
	return secret, nil
}

// PutAPIKey creates one immutable KV v2 secret. A retry with the exact same
// value returns the existing version; a different value at the same path is
// rejected. This makes the external secret write recoverable before the
// database idempotency transaction commits without ever returning plaintext.
func (reader *ClientReader) PutAPIKey(ctx context.Context, path, value string) (string, bool, error) {
	if reader == nil || reader.Client == nil || reader.Mount == "" || path == "" || len(value) < 8 || len(value) > 16<<10 || strings.ContainsAny(value, "\x00\r\n") {
		return "", false, ErrKeyUnavailable
	}
	if reader.TokenFile != "" {
		if err := reader.reloadToken(); err != nil {
			return "", false, err
		}
	}
	secret, err := reader.Client.KVv2(reader.Mount).Put(ctx, path, map[string]any{"api_key": value}, api.WithCheckAndSet(0))
	if err == nil && secret != nil && secret.VersionMetadata != nil && secret.VersionMetadata.Version > 0 {
		return strconv.Itoa(secret.VersionMetadata.Version), true, nil
	}
	existing, readErr := reader.Client.KVv2(reader.Mount).Get(ctx, path)
	if readErr != nil || existing == nil || existing.VersionMetadata == nil || existing.VersionMetadata.Destroyed || !existing.VersionMetadata.DeletionTime.IsZero() {
		return "", false, ErrKeyUnavailable
	}
	stored, ok := existing.Data["api_key"].(string)
	if !ok || len(stored) != len(value) || subtle.ConstantTimeCompare([]byte(stored), []byte(value)) != 1 {
		return "", false, ErrKeyUnavailable
	}
	return strconv.Itoa(existing.VersionMetadata.Version), false, nil
}

// DestroySecretVersion irreversibly removes the exact version referenced by a
// revoked BYOK credential. Database authorization is fail-closed independently
// of Vault, but successful deletion also eliminates the residual secret.
func (reader *ClientReader) DestroySecretVersion(ctx context.Context, path string, version int) error {
	if reader == nil || reader.Client == nil || reader.Mount == "" || path == "" || version < 1 {
		return ErrKeyUnavailable
	}
	if reader.TokenFile != "" {
		if err := reader.reloadToken(); err != nil {
			return err
		}
	}
	if err := reader.Client.KVv2(reader.Mount).Destroy(ctx, path, []int{version}); err != nil {
		return ErrKeyUnavailable
	}
	secret, err := reader.Client.KVv2(reader.Mount).GetVersion(ctx, path, version)
	if err == nil && secret != nil && secret.VersionMetadata != nil && !secret.VersionMetadata.Destroyed {
		return ErrKeyUnavailable
	}
	return nil
}

func (reader *ClientReader) reloadToken() error {
	file, err := os.Open(reader.TokenFile)
	if err != nil {
		return ErrKeyUnavailable
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, 8193))
	token := strings.TrimSpace(string(contents))
	if err != nil || len(contents) > 8192 || token == "" || strings.ContainsAny(token, "\x00\r\n") {
		return ErrKeyUnavailable
	}
	reader.Client.SetToken(token)
	return nil
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
