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
