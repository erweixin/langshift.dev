package vaultsecrets

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hashicorp/vault/api"
)

type versionedReaderStub struct {
	secret         *api.KVSecret
	err            error
	path           string
	version, calls int
}

func (reader *versionedReaderStub) GetVersion(_ context.Context, path string, version int) (*api.KVSecret, error) {
	reader.path, reader.version = path, version
	reader.calls++
	return reader.secret, reader.err
}

func TestSourceReturnsOnlyExactLiveVaultVersion(t *testing.T) {
	reader := &versionedReaderStub{secret: &api.KVSecret{Data: map[string]any{"api_key": "provider-secret"}, VersionMetadata: &api.KVVersionMetadata{Version: 7}}}
	source := Source{KV: reader, Prefix: "lites/byok", Field: "api_key"}
	secret, err := source.Resolve(t.Context(), "lites/byok/tenant-id/credential-id", "7")
	if err != nil || string(secret) != "provider-secret" || reader.path != "lites/byok/tenant-id/credential-id" || reader.version != 7 || reader.calls != 1 {
		t.Fatalf("secret=%q reader=%#v err=%v", secret, reader, err)
	}
	clear(secret)
	reader.secret.VersionMetadata.Version = 8
	if _, err = source.Resolve(t.Context(), "lites/byok/tenant-id/credential-id", "7"); !errors.Is(err, ErrSecretUnavailable) {
		t.Fatalf("version substitution accepted: %v", err)
	}
	reader.secret.VersionMetadata = &api.KVVersionMetadata{Version: 7, Destroyed: true}
	if _, err = source.Resolve(t.Context(), "lites/byok/tenant-id/credential-id", "7"); !errors.Is(err, ErrSecretUnavailable) {
		t.Fatalf("destroyed secret accepted: %v", err)
	}
	reader.secret.VersionMetadata = &api.KVVersionMetadata{Version: 7, DeletionTime: time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC)}
	if _, err = source.Resolve(t.Context(), "lites/byok/tenant-id/credential-id", "7"); !errors.Is(err, ErrSecretUnavailable) {
		t.Fatalf("deleted secret accepted: %v", err)
	}
}

func TestSourceRejectsPathVersionAndPayloadSubstitution(t *testing.T) {
	reader := &versionedReaderStub{secret: &api.KVSecret{Data: map[string]any{"api_key": "provider-secret"}, VersionMetadata: &api.KVVersionMetadata{Version: 1}}}
	source := Source{KV: reader, Prefix: "lites/byok", Field: "api_key"}
	for _, secretRef := range []string{"other/key", "lites/byok", "lites/byok/../admin", "lites/byok//key", "lites/byok/key\\other", "lites/byok/key\nother"} {
		if _, err := source.Resolve(t.Context(), secretRef, "1"); !errors.Is(err, ErrSecretUnavailable) {
			t.Fatalf("secret ref %q accepted: %v", secretRef, err)
		}
	}
	for _, version := range []string{"", "0", "-1", "latest", "1.0", "999999999999999999999"} {
		if _, err := source.Resolve(t.Context(), "lites/byok/tenant/key", version); !errors.Is(err, ErrSecretUnavailable) {
			t.Fatalf("secret version %q accepted: %v", version, err)
		}
	}
	reader.secret = &api.KVSecret{Data: map[string]any{"api_key": "short"}, VersionMetadata: &api.KVVersionMetadata{Version: 1}}
	if _, err := source.Resolve(t.Context(), "lites/byok/tenant/key", "1"); !errors.Is(err, ErrSecretUnavailable) {
		t.Fatalf("short secret accepted: %v", err)
	}
	reader.secret = &api.KVSecret{Data: map[string]any{"api_key": "provider\nsecret"}, VersionMetadata: &api.KVVersionMetadata{Version: 1}}
	if _, err := source.Resolve(t.Context(), "lites/byok/tenant/key", "1"); !errors.Is(err, ErrSecretUnavailable) {
		t.Fatalf("header injection secret accepted: %v", err)
	}
}

func TestSourceMasksVaultFailure(t *testing.T) {
	reader := &versionedReaderStub{err: errors.New("vault namespace detail")}
	source := Source{KV: reader, Prefix: "lites/byok", Field: "api_key"}
	_, err := source.Resolve(t.Context(), "lites/byok/tenant/key", "1")
	if !errors.Is(err, ErrSecretUnavailable) || errors.Is(err, reader.err) {
		t.Fatalf("vault error escaped boundary: %v", err)
	}
}

func TestMultiSourceRoutesOnlyExplicitNonOverlappingNamespace(t *testing.T) {
	managed := &versionedReaderStub{secret: &api.KVSecret{Data: map[string]any{"api_key": "managed-secret"}, VersionMetadata: &api.KVVersionMetadata{Version: 2}}}
	byok := &versionedReaderStub{secret: &api.KVSecret{Data: map[string]any{"api_key": "customer-secret"}, VersionMetadata: &api.KVVersionMetadata{Version: 2}}}
	source := MultiSource{Sources: []Source{
		{KV: managed, Prefix: "lites/providers", Field: "api_key"},
		{KV: byok, Prefix: "lites/byok", Field: "api_key"},
	}}
	secret, err := source.Resolve(t.Context(), "lites/byok/tenant/credential", "2")
	if err != nil || string(secret) != "customer-secret" || managed.calls != 0 || byok.calls != 1 {
		t.Fatalf("secret=%q managed_calls=%d byok_calls=%d err=%v", secret, managed.calls, byok.calls, err)
	}
	if _, err = source.Resolve(t.Context(), "lites/admin/credential", "2"); !errors.Is(err, ErrSecretUnavailable) {
		t.Fatalf("unbound namespace accepted: %v", err)
	}
	ambiguous := MultiSource{Sources: []Source{
		{KV: managed, Prefix: "lites", Field: "api_key"},
		{KV: byok, Prefix: "lites/byok", Field: "api_key"},
	}}
	if _, err = ambiguous.Resolve(t.Context(), "lites/byok/tenant/credential", "2"); !errors.Is(err, ErrSecretUnavailable) {
		t.Fatalf("ambiguous namespace accepted: %v", err)
	}
}
