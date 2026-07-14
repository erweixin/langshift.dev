package ratelimit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClientOptionRequiresAuthenticatedTLSInProduction(t *testing.T) {
	if _, err := clientOption(ClientConfig{Addresses: []string{"cache.internal:6379"}}); err == nil {
		t.Fatal("unauthenticated production Valkey configuration accepted")
	}
	if _, err := clientOption(ClientConfig{Addresses: []string{"cache.internal:6379"}, PasswordFile: "/missing"}); err != nil {
		t.Fatalf("file-backed authenticated production configuration rejected before connection: %v", err)
	}
}

func TestClientOptionAllowsOnlyLoopbackWithoutTLSInDevelopment(t *testing.T) {
	if option, err := clientOption(ClientConfig{Addresses: []string{"127.0.0.1:6379"}, AllowInsecureDevelopment: true}); err != nil || option.TLSConfig != nil {
		t.Fatalf("loopback development configuration rejected: option=%#v err=%v", option, err)
	}
	if _, err := clientOption(ClientConfig{Addresses: []string{"192.0.2.1:6379"}, AllowInsecureDevelopment: true}); err == nil {
		t.Fatal("remote insecure Valkey endpoint accepted")
	}
}

func TestReadCredentialRejectsAmbiguousSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte("secret\nsecond"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredential(path); err == nil {
		t.Fatal("multiline credential accepted")
	}
}
