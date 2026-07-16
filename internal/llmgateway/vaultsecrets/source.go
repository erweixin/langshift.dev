// Package vaultsecrets resolves exact-version BYOK credentials from Vault KV v2.
package vaultsecrets

import (
	"context"
	"errors"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/hashicorp/vault/api"
)

var ErrSecretUnavailable = errors.New("provider secret is unavailable")

type VersionedReader interface {
	GetVersion(context.Context, string, int) (*api.KVSecret, error)
}

type Source struct {
	KV     VersionedReader
	Prefix string
	Field  string
}

// MultiSource routes an immutable secret reference to exactly one configured
// namespace. Ambiguous or unbound references fail closed before Vault is read.
type MultiSource struct {
	Sources []Source
}

func (multi MultiSource) Resolve(ctx context.Context, secretRef, secretVersion string) ([]byte, error) {
	if ctx == nil || len(multi.Sources) == 0 || len(multi.Sources) > 16 {
		return nil, ErrSecretUnavailable
	}
	var selected *Source
	for index := range multi.Sources {
		source := &multi.Sources[index]
		if !validPrefix(source.Prefix) || source.KV == nil || source.Field != "api_key" {
			return nil, ErrSecretUnavailable
		}
		if !validSecretRef(source.Prefix, secretRef) {
			continue
		}
		if selected != nil {
			return nil, ErrSecretUnavailable
		}
		selected = source
	}
	if selected == nil {
		return nil, ErrSecretUnavailable
	}
	return selected.Resolve(ctx, secretRef, secretVersion)
}

func (source Source) Resolve(ctx context.Context, secretRef, secretVersion string) ([]byte, error) {
	if ctx == nil || source.KV == nil || !validPrefix(source.Prefix) || source.Field != "api_key" || !validSecretRef(source.Prefix, secretRef) {
		return nil, ErrSecretUnavailable
	}
	version64, err := strconv.ParseUint(secretVersion, 10, 31)
	if err != nil || version64 < 1 {
		return nil, ErrSecretUnavailable
	}
	secret, err := source.KV.GetVersion(ctx, secretRef, int(version64))
	if err != nil || secret == nil || secret.VersionMetadata == nil || secret.VersionMetadata.Version != int(version64) || secret.VersionMetadata.Destroyed || !secret.VersionMetadata.DeletionTime.IsZero() {
		return nil, ErrSecretUnavailable
	}
	value, ok := secret.Data[source.Field].(string)
	if !ok || len(value) < 8 || len(value) > 16<<10 || strings.ContainsAny(value, "\x00\r\n") {
		return nil, ErrSecretUnavailable
	}
	return []byte(value), nil
}

var prefixPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_/-]{0,127}$`)

func validPrefix(prefix string) bool {
	return prefixPattern.MatchString(prefix) && path.Clean(prefix) == prefix && !strings.Contains(prefix, "//") && !strings.HasSuffix(prefix, "/")
}

func validSecretRef(prefix, secretRef string) bool {
	return len(secretRef) > len(prefix)+1 && len(secretRef) <= 512 && strings.HasPrefix(secretRef, prefix+"/") && path.Clean(secretRef) == secretRef && !strings.Contains(secretRef, "//") && !strings.ContainsAny(secretRef, "\\\x00\r\n")
}
