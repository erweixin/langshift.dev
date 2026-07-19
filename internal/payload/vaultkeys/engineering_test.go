package vaultkeys

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEngineeringDerivedProviderIsDeterministicAndTenantSeparated(t *testing.T) {
	seed := bytes.Repeat([]byte{0x7a}, 32)
	provider, err := newEngineeringDerivedProvider([]byte(base64.StdEncoding.EncodeToString(seed) + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	const firstTenant = "10000000-0000-4000-8000-000000000001"
	const secondTenant = "20000000-0000-4000-8000-000000000001"
	first, err := provider.Current(context.Background(), firstTenant)
	if err != nil {
		t.Fatal(err)
	}
	again, err := provider.ByID(context.Background(), firstTenant, engineeringDerivedKeyID)
	if err != nil || first.ID != engineeringDerivedKeyID || !bytes.Equal(first.Material, again.Material) {
		t.Fatalf("first=%#v again=%#v err=%v", first, again, err)
	}
	second, err := provider.Current(context.Background(), secondTenant)
	if err != nil || bytes.Equal(first.Material, second.Material) {
		t.Fatalf("tenant-separated key was not derived: err=%v", err)
	}
	if _, err = provider.ByID(context.Background(), firstTenant, "other-key"); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("historical engineering key id was accepted: %v", err)
	}
	if _, err = provider.Current(context.Background(), "../tenant"); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("invalid tenant was accepted: %v", err)
	}
}

func TestProviderForEnvironmentFailsClosedOutsideLocalEngineering(t *testing.T) {
	seedFile := filepath.Join(t.TempDir(), "payload-key-seed")
	if err := os.WriteFile(seedFile, []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x51}, 32))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider, err := ProviderForEnvironment("engineering-test", true, seedFile, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Current(context.Background(), "10000000-0000-4000-8000-000000000001"); err != nil {
		t.Fatal(err)
	}
	if _, err = ProviderForEnvironment("production", true, seedFile, nil, ""); !errors.Is(err, errEngineeringProviderRejected) {
		t.Fatalf("production selected derived provider: %v", err)
	}
	if _, err = ProviderForEnvironment("production", false, seedFile, memoryVault{}, "lites/payload-keys"); !errors.Is(err, errEngineeringProviderRejected) {
		t.Fatalf("production ignored engineering seed: %v", err)
	}
	vaultProvider, err := ProviderForEnvironment("production", false, "", memoryVault{}, "lites/payload-keys")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := vaultProvider.(Provider); !ok {
		t.Fatalf("production provider type=%T", vaultProvider)
	}
}

func TestEngineeringDerivedProviderRejectsInvalidSeed(t *testing.T) {
	for _, encoded := range [][]byte{nil, []byte("not-base64"), []byte(base64.StdEncoding.EncodeToString(make([]byte, 32)))} {
		if _, err := newEngineeringDerivedProvider(encoded); !errors.Is(err, errEngineeringProviderRejected) {
			t.Fatalf("seed %q err=%v", encoded, err)
		}
	}
}
