package vaultkeys

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/hashicorp/vault/api"
)

type memoryVault map[string]*api.KVSecret

func (vault memoryVault) Get(_ context.Context, path string) (*api.KVSecret, error) {
	secret, ok := vault[path]
	if !ok {
		return nil, errors.New("missing")
	}
	return secret, nil
}

func TestProviderLoadsCurrentAndHistoricalTenantKeys(t *testing.T) {
	const tenant = "10000000-0000-4000-8000-000000000001"
	material := bytes.Repeat([]byte{0x42}, 32)
	vault := memoryVault{
		"lites/payload-keys/" + tenant + "/current":         {Data: map[string]any{"key_id": "key-v2"}},
		"lites/payload-keys/" + tenant + "/versions/key-v2": {Data: map[string]any{"key_id": "key-v2", "material": base64.StdEncoding.EncodeToString(material)}},
	}
	provider := Provider{KV: vault, Prefix: "lites/payload-keys"}
	key, err := provider.Current(context.Background(), tenant)
	if err != nil || key.ID != "key-v2" || !bytes.Equal(key.Material, material) {
		t.Fatalf("key=%#v err=%v", key, err)
	}
	key.Material[0] = 0
	again, err := provider.ByID(context.Background(), tenant, "key-v2")
	if err != nil || again.Material[0] != 0x42 {
		t.Fatal("provider returned aliased key material")
	}
}

func TestProviderFailsClosedForPathInjectionAmbiguousOrDestroyedKeys(t *testing.T) {
	const tenant = "10000000-0000-4000-8000-000000000001"
	vault := memoryVault{
		"lites/payload-keys/" + tenant + "/current":         {Data: map[string]any{"key_id": "key-v2", "unexpected": true}},
		"lites/payload-keys/" + tenant + "/versions/key-v2": {Data: map[string]any{"key_id": "key-v2", "material": base64.StdEncoding.EncodeToString(make([]byte, 32))}},
	}
	provider := Provider{KV: vault, Prefix: "lites/payload-keys"}
	for _, invalidTenant := range []string{"../secret", tenant + "/other", ""} {
		if _, err := provider.Current(context.Background(), invalidTenant); !errors.Is(err, ErrKeyUnavailable) {
			t.Fatalf("tenant=%q err=%v", invalidTenant, err)
		}
	}
	if _, err := provider.Current(context.Background(), tenant); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("expected ambiguous current pointer rejection, got %v", err)
	}
	vault["lites/payload-keys/"+tenant+"/current"] = &api.KVSecret{Data: map[string]any{"key_id": "key-v2"}}
	if _, err := provider.ByID(context.Background(), tenant, "key-v2"); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("expected destroyed/zero key rejection, got %v", err)
	}
	if _, err := provider.ByID(context.Background(), tenant, "../key"); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("expected key id traversal rejection, got %v", err)
	}
}
