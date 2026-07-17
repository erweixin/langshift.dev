//go:build integration

package vaultkeys

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/vault/api"
)

func TestVaultKVv2VersionedKeyAndCryptoShred(t *testing.T) {
	address := os.Getenv("VAULT_TEST_ADDR")
	token := os.Getenv("VAULT_TEST_TOKEN")
	if address == "" || token == "" {
		t.Skip("VAULT_TEST_ADDR and VAULT_TEST_TOKEN are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	configuration := api.DefaultConfig()
	configuration.Address = address
	client, err := api.NewClient(configuration)
	if err != nil {
		t.Fatal(err)
	}
	client.SetToken(token)
	const tenant = "10000000-0000-4000-8000-000000000001"
	const prefix = "lites/payload-keys-integration"
	versionPath := prefix + "/" + tenant + "/versions/key-v1"
	if _, err = client.KVv2("secret").Put(ctx, versionPath, map[string]any{"key_id": "key-v1", "material": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x51}, 32))}); err != nil {
		t.Fatal(err)
	}
	if _, err = client.KVv2("secret").Put(ctx, prefix+"/"+tenant+"/current", map[string]any{"key_id": "key-v1"}); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(t.TempDir(), "vault-token")
	if err = os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := NewClientReader(ClientConfig{Address: address, Mount: "secret", TokenFile: tokenFile, AllowInsecureDevelopment: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = reader.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	provider := Provider{KV: reader, Prefix: prefix}
	key, err := provider.Current(ctx, tenant)
	if err != nil || key.ID != "key-v1" || len(key.Material) != 32 {
		t.Fatalf("key=%#v err=%v", key, err)
	}
	if err = client.KVv2("secret").Destroy(ctx, versionPath, []int{1}); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.ByID(ctx, tenant, "key-v1"); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("destroyed key remained readable: %v", err)
	}
}

func TestVaultTransitSubjectKeyPurgeIsIrreversible(t *testing.T) {
	address := os.Getenv("VAULT_TEST_ADDR")
	token := os.Getenv("VAULT_TEST_TOKEN")
	if address == "" || token == "" {
		t.Skip("VAULT_TEST_ADDR and VAULT_TEST_TOKEN are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	configuration := api.DefaultConfig()
	configuration.Address = address
	client, err := api.NewClient(configuration)
	if err != nil {
		t.Fatal(err)
	}
	client.SetToken(token)
	mounts, err := client.Sys().ListMountsWithContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := mounts["transit/"]; !exists {
		if err = client.Sys().MountWithContext(ctx, "transit", &api.MountInput{Type: "transit"}); err != nil {
			t.Fatal(err)
		}
	}
	keyName := "subject-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	if _, err = client.Logical().WriteWithContext(ctx, "transit/keys/"+keyName, map[string]any{"type": "aes256-gcm96"}); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(t.TempDir(), "vault-token")
	if err = os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := NewClientReader(ClientConfig{Address: address, Mount: "transit", TokenFile: tokenFile, AllowInsecureDevelopment: true})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := reader.PurgeKey(ctx, "vault://transit/"+keyName)
	if err != nil || len(receipt) != 64 {
		t.Fatalf("receipt=%s err=%v", receipt, err)
	}
	if secret, readErr := client.Logical().ReadWithContext(ctx, "transit/keys/"+keyName); readErr != nil || secret != nil {
		t.Fatalf("purged key secret=%#v err=%v", secret, readErr)
	}
}
