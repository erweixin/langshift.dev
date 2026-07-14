package vaultkeys

import (
	"context"
	"io"
	"net"
	"net/url"
	"os"
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
		if address.Scheme == "http" && !loopback(address.Hostname()) {
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
