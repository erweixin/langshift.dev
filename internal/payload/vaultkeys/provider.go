// Package vaultkeys loads versioned per-tenant payload data keys from Vault KV v2.
package vaultkeys

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"

	"github.com/hashicorp/vault/api"
	"github.com/langshift/lites/internal/payload"
)

var ErrKeyUnavailable = errors.New("tenant payload key is unavailable")

var (
	tenantPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	keyIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
	prefixPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_/-]{0,255}$`)
)

type Reader interface {
	Get(context.Context, string) (*api.KVSecret, error)
}

type KVv2Reader struct{ KV *api.KVv2 }

func (reader KVv2Reader) Get(ctx context.Context, path string) (*api.KVSecret, error) {
	if reader.KV == nil {
		return nil, ErrKeyUnavailable
	}
	return reader.KV.Get(ctx, path)
}

type Provider struct {
	KV     Reader
	Prefix string
}

func (provider Provider) Current(ctx context.Context, tenantID string) (payload.Key, error) {
	if !provider.valid() || !tenantPattern.MatchString(tenantID) {
		return payload.Key{}, ErrKeyUnavailable
	}
	secret, err := provider.KV.Get(ctx, provider.path(tenantID, "current"))
	if err != nil || secret == nil || len(secret.Data) != 1 {
		return payload.Key{}, ErrKeyUnavailable
	}
	keyID, ok := secret.Data["key_id"].(string)
	if !ok || !keyIDPattern.MatchString(keyID) {
		return payload.Key{}, ErrKeyUnavailable
	}
	return provider.ByID(ctx, tenantID, keyID)
}

func (provider Provider) ByID(ctx context.Context, tenantID, keyID string) (payload.Key, error) {
	if !provider.valid() || !tenantPattern.MatchString(tenantID) || !keyIDPattern.MatchString(keyID) {
		return payload.Key{}, ErrKeyUnavailable
	}
	secret, err := provider.KV.Get(ctx, provider.path(tenantID, "versions/"+keyID))
	if err != nil || secret == nil || len(secret.Data) != 2 {
		return payload.Key{}, ErrKeyUnavailable
	}
	storedID, idOK := secret.Data["key_id"].(string)
	encoded, materialOK := secret.Data["material"].(string)
	if !idOK || !materialOK || subtle.ConstantTimeCompare([]byte(storedID), []byte(keyID)) != 1 {
		return payload.Key{}, ErrKeyUnavailable
	}
	material, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(material) != 32 || allZero(material) {
		return payload.Key{}, ErrKeyUnavailable
	}
	return payload.Key{ID: keyID, Material: append([]byte(nil), material...)}, nil
}

func (provider Provider) valid() bool {
	return provider.KV != nil && prefixPattern.MatchString(provider.Prefix) && !strings.Contains(provider.Prefix, "//") && !strings.HasSuffix(provider.Prefix, "/")
}

func (provider Provider) path(tenantID, suffix string) string {
	return provider.Prefix + "/" + tenantID + "/" + suffix
}

func allZero(value []byte) bool {
	var combined byte
	for _, item := range value {
		combined |= item
	}
	return combined == 0
}
