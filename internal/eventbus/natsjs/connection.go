package natsjs

import (
	"context"
	"crypto/tls"
	"net/url"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type ConnectionConfig struct {
	URLs                     []string
	Name                     string
	CredentialsFile          string
	RootCAFile               string
	ClientCertificateFile    string
	ClientKeyFile            string
	ConnectTimeout           time.Duration
	ReconnectWait            time.Duration
	AllowInsecureDevelopment bool
	OnDisconnect             func(error)
	OnReconnect              func(string)
	OnClosed                 func(error)
}

func Connect(config ConnectionConfig) (*nats.Conn, jetstream.JetStream, error) {
	if err := validateConnectionConfig(config); err != nil {
		return nil, nil, err
	}
	options := []nats.Option{
		nats.Name(config.Name),
		nats.Timeout(config.ConnectTimeout),
		nats.ReconnectWait(config.ReconnectWait),
		nats.MaxReconnects(-1),
		nats.PingInterval(20 * time.Second),
		nats.MaxPingsOutstanding(3),
		nats.DrainTimeout(15 * time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if config.OnDisconnect != nil {
				config.OnDisconnect(err)
			}
		}),
		nats.ReconnectHandler(func(connection *nats.Conn) {
			if config.OnReconnect != nil {
				config.OnReconnect(connection.ConnectedUrl())
			}
		}),
		nats.ClosedHandler(func(connection *nats.Conn) {
			if config.OnClosed != nil {
				config.OnClosed(connection.LastError())
			}
		}),
	}
	if config.CredentialsFile != "" {
		options = append(options, nats.UserCredentials(config.CredentialsFile))
	}
	if !config.AllowInsecureDevelopment {
		options = append(options, nats.Secure(&tls.Config{MinVersion: tls.VersionTLS13}))
	}
	if config.RootCAFile != "" {
		options = append(options, nats.RootCAs(config.RootCAFile))
	}
	if config.ClientCertificateFile != "" {
		options = append(options, nats.ClientCert(config.ClientCertificateFile, config.ClientKeyFile))
	}
	connection, err := nats.Connect(strings.Join(config.URLs, ","), options...)
	if err != nil {
		return nil, nil, err
	}
	js, err := jetstream.New(connection)
	if err != nil {
		connection.Close()
		return nil, nil, err
	}
	return connection, js, nil
}

func Ready(ctx context.Context, connection *nats.Conn, js jetstream.JetStream) error {
	if connection == nil || js == nil || !connection.IsConnected() {
		return ErrConfiguration
	}
	_, err := js.AccountInfo(ctx)
	return err
}

func validateConnectionConfig(config ConnectionConfig) error {
	if len(config.URLs) == 0 || config.Name == "" || config.ConnectTimeout <= 0 || config.ReconnectWait <= 0 || (config.ClientCertificateFile == "") != (config.ClientKeyFile == "") {
		return ErrConfiguration
	}
	if !config.AllowInsecureDevelopment && config.CredentialsFile == "" && config.ClientCertificateFile == "" {
		return ErrConfiguration
	}
	for _, rawURL := range config.URLs {
		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Host == "" || parsed.User != nil {
			return ErrConfiguration
		}
		if config.AllowInsecureDevelopment {
			if parsed.Scheme != "nats" && parsed.Scheme != "tls" {
				return ErrConfiguration
			}
		} else if parsed.Scheme != "tls" {
			return ErrConfiguration
		}
	}
	return nil
}
