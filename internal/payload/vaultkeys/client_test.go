package vaultkeys

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestPurgeTransitKeyEnablesDeletionAndVerifiesAbsence(t *testing.T) {
	var configured, deleted bool
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPut && request.URL.Path == "/v1/transit/keys/memory/user/config":
			configured = true
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`{"data":{}}`))
		case request.Method == http.MethodDelete && request.URL.Path == "/v1/transit/keys/memory/user":
			if !configured {
				t.Fatal("delete occurred before deletion was enabled")
			}
			deleted = true
			response.WriteHeader(http.StatusNoContent)
		case request.Method == http.MethodGet && request.URL.Path == "/v1/transit/keys/memory/user" && deleted:
			response.WriteHeader(http.StatusNotFound)
			_, _ = response.Write([]byte(`{"errors":[]}`))
		default:
			t.Fatalf("unexpected Vault request %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	reader, err := NewClientReader(ClientConfig{Address: server.URL, Mount: "transit", AllowInsecureDevelopment: true})
	if err != nil {
		t.Fatal(err)
	}
	checksum, err := reader.PurgeKey(t.Context(), "vault://transit/memory/user")
	if err != nil || !configured || !deleted || len(checksum) != 64 {
		t.Fatalf("configured=%v deleted=%v checksum=%q err=%v", configured, deleted, checksum, err)
	}
	if _, err = reader.PurgeKey(t.Context(), "vault://transit/../secret"); err == nil {
		t.Fatal("path traversal key reference was accepted")
	}
}

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
