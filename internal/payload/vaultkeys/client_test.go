package vaultkeys

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClientConfigurationRejectsRemotePlaintextAndURLCredentials(t *testing.T) {
	if _, err := NewClientReader(ClientConfig{Address: "http://vault.internal:8200", Mount: "secret", AllowInsecureDevelopment: true}); err == nil {
		t.Fatal("expected remote plaintext Vault rejection")
	}
	if _, err := NewClientReader(ClientConfig{Address: "https://token@vault.internal:8200", Mount: "secret", TokenFile: "/run/secrets/token"}); err == nil {
		t.Fatal("expected URL credential rejection")
	}
}

func TestClientReloadsBoundedTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault-token")
	if err := os.WriteFile(path, []byte("first-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := NewClientReader(ClientConfig{Address: "http://127.0.0.1:8200", Mount: "secret", TokenFile: path, AllowInsecureDevelopment: true})
	if err != nil || reader.Client.Token() != "first-token" {
		t.Fatalf("token=%q err=%v", reader.Client.Token(), err)
	}
	if err = os.WriteFile(path, []byte("second-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = reader.reloadToken(); err != nil || reader.Client.Token() != "second-token" {
		t.Fatalf("token=%q err=%v", reader.Client.Token(), err)
	}
}
