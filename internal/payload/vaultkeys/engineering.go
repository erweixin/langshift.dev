package vaultkeys

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"os"
	"strings"

	"github.com/langshift/lites/internal/payload"
)

const engineeringDerivedKeyID = "engineering-derived-v1"

var errEngineeringProviderRejected = errors.New("engineering payload key provider is unavailable")

// EngineeringDerivedProvider is a deterministic, tenant-separated payload-key
// adapter for the ephemeral macOS Compose stack. It is deliberately selected
// only through ProviderForEnvironment; production payload keys remain Vault KV
// records with independent versions and rotation histories.
type EngineeringDerivedProvider struct {
	seed [32]byte
}

func newEngineeringDerivedProvider(encodedSeed []byte) (EngineeringDerivedProvider, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encodedSeed)))
	if err != nil || len(decoded) != sha256.Size || allZero(decoded) {
		return EngineeringDerivedProvider{}, errEngineeringProviderRejected
	}
	var seed [32]byte
	copy(seed[:], decoded)
	return EngineeringDerivedProvider{seed: seed}, nil
}

func (provider EngineeringDerivedProvider) Current(_ context.Context, tenantID string) (payload.Key, error) {
	if !tenantPattern.MatchString(tenantID) || allZero(provider.seed[:]) {
		return payload.Key{}, ErrKeyUnavailable
	}
	mac := hmac.New(sha256.New, provider.seed[:])
	_, _ = mac.Write([]byte("lites-macos-payload-key-v1\x00"))
	_, _ = mac.Write([]byte(tenantID))
	return payload.Key{ID: engineeringDerivedKeyID, Material: mac.Sum(nil)}, nil
}

func (provider EngineeringDerivedProvider) ByID(ctx context.Context, tenantID, keyID string) (payload.Key, error) {
	if subtleKeyIDMatch(keyID, engineeringDerivedKeyID) == 0 {
		return payload.Key{}, ErrKeyUnavailable
	}
	return provider.Current(ctx, tenantID)
}

// ProviderForEnvironment fails closed unless the derived-key adapter is
// explicitly enabled for engineering-test. A seed path in any other runtime is
// rejected rather than silently changing production key semantics.
func ProviderForEnvironment(environment string, localCompose bool, seedFile string, kv Reader, prefix string) (payload.KeyProvider, error) {
	if !localCompose {
		if seedFile != "" {
			return nil, errEngineeringProviderRejected
		}
		provider := Provider{KV: kv, Prefix: prefix}
		if !provider.valid() {
			return nil, ErrKeyUnavailable
		}
		return provider, nil
	}
	if environment != "engineering-test" || seedFile == "" {
		return nil, errEngineeringProviderRejected
	}
	info, err := os.Lstat(seedFile)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 128 {
		return nil, errEngineeringProviderRejected
	}
	encoded, err := os.ReadFile(seedFile)
	if err != nil {
		return nil, errEngineeringProviderRejected
	}
	provider, err := newEngineeringDerivedProvider(encoded)
	if err != nil {
		return nil, err
	}
	return provider, nil
}

func subtleKeyIDMatch(left, right string) int {
	if len(left) != len(right) {
		return 0
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right))
}
