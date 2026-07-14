package natsjs

import (
	"errors"
	"testing"
	"time"
)

func TestConnectionConfigRequiresAuthenticatedTLSInProduction(t *testing.T) {
	base := ConnectionConfig{URLs: []string{"tls://nats.internal:4222"}, Name: "publisher", ConnectTimeout: time.Second, ReconnectWait: time.Second}
	if err := validateConnectionConfig(base); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("expected authentication requirement, got %v", err)
	}
	base.CredentialsFile = "/run/secrets/nats.creds"
	if err := validateConnectionConfig(base); err != nil {
		t.Fatal(err)
	}
	base.URLs = []string{"nats://token@example.com:4222"}
	if err := validateConnectionConfig(base); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("expected URL credential rejection, got %v", err)
	}
}

func TestConnectionConfigAllowsExplicitLocalDevelopment(t *testing.T) {
	config := ConnectionConfig{URLs: []string{"nats://127.0.0.1:4222"}, Name: "integration-test", ConnectTimeout: time.Second, ReconnectWait: time.Second, AllowInsecureDevelopment: true}
	if err := validateConnectionConfig(config); err != nil {
		t.Fatal(err)
	}
}
