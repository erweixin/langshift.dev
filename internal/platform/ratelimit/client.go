package ratelimit

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"os"
	"strings"
	"time"

	valkey "github.com/valkey-io/valkey-go"
)

type ClientConfig struct {
	Addresses                []string
	Username                 string
	PasswordFile             string
	RootCAFile               string
	ClientCertificateFile    string
	ClientKeyFile            string
	TLSServerName            string
	AllowInsecureDevelopment bool
	AllowLocalCompose        bool
}

func NewClient(configuration ClientConfig) (valkey.Client, error) {
	option, err := clientOption(configuration)
	if err != nil {
		return nil, err
	}
	client, err := valkey.NewClient(option)
	if err != nil {
		return nil, ErrUnavailable
	}
	return client, nil
}

func Ready(ctx context.Context, client valkey.Client) error {
	if client == nil {
		return ErrConfiguration
	}
	value, err := client.Do(ctx, client.B().Ping().Build()).ToString()
	if err != nil || value != "PONG" {
		return ErrUnavailable
	}
	return nil
}

func clientOption(configuration ClientConfig) (valkey.ClientOption, error) {
	if len(configuration.Addresses) == 0 || len(configuration.Addresses) > 32 || (configuration.ClientCertificateFile == "") != (configuration.ClientKeyFile == "") || (configuration.Username != "" && configuration.PasswordFile == "") {
		return valkey.ClientOption{}, ErrConfiguration
	}
	for _, address := range configuration.Addresses {
		host, port, err := net.SplitHostPort(address)
		if err != nil || host == "" || port == "" {
			return valkey.ClientOption{}, ErrConfiguration
		}
		if configuration.AllowInsecureDevelopment && !isLoopback(host) && !(configuration.AllowLocalCompose && host == "valkey") {
			return valkey.ClientOption{}, ErrConfiguration
		}
	}
	if !configuration.AllowInsecureDevelopment && configuration.PasswordFile == "" && configuration.ClientCertificateFile == "" {
		return valkey.ClientOption{}, ErrConfiguration
	}
	tlsConfig, err := loadTLSConfig(configuration)
	if err != nil {
		return valkey.ClientOption{}, err
	}
	option := valkey.ClientOption{InitAddress: append([]string(nil), configuration.Addresses...), ClientName: "lites", TLSConfig: tlsConfig, DisableCache: true, Dialer: net.Dialer{Timeout: 3 * time.Second, KeepAlive: 5 * time.Second}, ConnWriteTimeout: 5 * time.Second, ShuffleInit: len(configuration.Addresses) > 1}
	if configuration.PasswordFile != "" {
		option.AuthCredentialsFn = func(valkey.AuthCredentialsContext) (valkey.AuthCredentials, error) {
			password, readErr := readCredential(configuration.PasswordFile)
			if readErr != nil {
				return valkey.AuthCredentials{}, readErr
			}
			return valkey.AuthCredentials{Username: configuration.Username, Password: password}, nil
		}
	}
	return option, nil
}

func loadTLSConfig(configuration ClientConfig) (*tls.Config, error) {
	if configuration.AllowInsecureDevelopment && configuration.RootCAFile == "" && configuration.ClientCertificateFile == "" && configuration.TLSServerName == "" {
		return nil, nil
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: configuration.TLSServerName}
	if configuration.RootCAFile != "" {
		contents, err := os.ReadFile(configuration.RootCAFile)
		if err != nil {
			return nil, ErrConfiguration
		}
		roots, err := x509.SystemCertPool()
		if err != nil || !roots.AppendCertsFromPEM(contents) {
			return nil, ErrConfiguration
		}
		tlsConfig.RootCAs = roots
	}
	if configuration.ClientCertificateFile != "" {
		certificate, err := tls.LoadX509KeyPair(configuration.ClientCertificateFile, configuration.ClientKeyFile)
		if err != nil {
			return nil, ErrConfiguration
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	return tlsConfig, nil
}

func readCredential(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", ErrUnavailable
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, 8193))
	value := strings.TrimSpace(string(contents))
	if err != nil || len(contents) > 8192 || value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", ErrUnavailable
	}
	return value, nil
}

func isLoopback(host string) bool {
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
