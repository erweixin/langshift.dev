package vaultkeys

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
)

// PurgeKey irreversibly removes a per-subject Vault Transit key. The Transit
// key must have deletion_allowed enabled by policy; this method enables it on
// the exact confined path, deletes it, and verifies that a subsequent read is
// absent before returning a non-secret receipt checksum.
func (reader *ClientReader) PurgeKey(ctx context.Context, ref string) (string, error) {
	if reader == nil || reader.Client == nil || reader.Mount == "" {
		return "", ErrKeyUnavailable
	}
	parsed, err := url.Parse(ref)
	if err != nil || parsed.Scheme != "vault" || parsed.Host != reader.Mount || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return "", ErrKeyUnavailable
	}
	key := strings.TrimPrefix(parsed.EscapedPath(), "/")
	decoded, err := url.PathUnescape(key)
	if err != nil || decoded == "" || decoded != key || strings.Contains(decoded, "..") || strings.ContainsAny(decoded, "\\\x00\r\n") || strings.HasPrefix(decoded, "/") || strings.HasSuffix(decoded, "/") || strings.Contains(decoded, "//") {
		return "", ErrKeyUnavailable
	}
	if reader.TokenFile != "" {
		if err = reader.reloadToken(); err != nil {
			return "", err
		}
	}
	path := reader.Mount + "/keys/" + decoded
	if _, err = reader.Client.Logical().WriteWithContext(ctx, path+"/config", map[string]any{"deletion_allowed": true}); err != nil {
		return "", ErrKeyUnavailable
	}
	if _, err = reader.Client.Logical().DeleteWithContext(ctx, path); err != nil {
		return "", ErrKeyUnavailable
	}
	secret, err := reader.Client.Logical().ReadWithContext(ctx, path)
	if err != nil || secret != nil {
		return "", ErrKeyUnavailable
	}
	digest := sha256.Sum256([]byte("lites-vault-transit-key-purge-v1\x00" + reader.Mount + "\x00" + decoded))
	return hex.EncodeToString(digest[:]), nil
}
