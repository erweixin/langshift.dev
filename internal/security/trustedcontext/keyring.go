package trustedcontext

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"time"
)

var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type publicKeyring struct {
	Version string            `json:"version"`
	Keys    []publicKeyRecord `json:"keys"`
}

type publicKeyRecord struct {
	ID        string    `json:"id"`
	PublicKey string    `json:"public_key"`
	NotBefore time.Time `json:"not_before"`
	NotAfter  time.Time `json:"not_after"`
}

// LoadPublicKeyring loads an externally managed gateway verification keyring.
// Key windows constrain token issuance, while retaining an old public key for
// at least MaximumTTL permits a safe overlap during rotation.
func LoadPublicKeyring(path string) (map[string]ed25519.PublicKey, map[string]KeyWindow, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, ErrUnknownKey
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil || len(encoded) > 64*1024 {
		return nil, nil, ErrMalformed
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var keyring publicKeyring
	if err = decoder.Decode(&keyring); err != nil {
		return nil, nil, ErrMalformed
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || keyring.Version != "1.0.0" || len(keyring.Keys) < 1 || len(keyring.Keys) > 32 {
		return nil, nil, ErrMalformed
	}
	keys := make(map[string]ed25519.PublicKey, len(keyring.Keys))
	windows := make(map[string]KeyWindow, len(keyring.Keys))
	for _, record := range keyring.Keys {
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(record.PublicKey)
		if decodeErr != nil || len(decoded) != ed25519.PublicKeySize || !keyIDPattern.MatchString(record.ID) || record.NotBefore.IsZero() || record.NotAfter.IsZero() || !record.NotAfter.After(record.NotBefore) {
			return nil, nil, ErrMalformed
		}
		if _, exists := keys[record.ID]; exists {
			return nil, nil, ErrMalformed
		}
		keys[record.ID] = ed25519.PublicKey(append([]byte(nil), decoded...))
		windows[record.ID] = KeyWindow{NotBefore: record.NotBefore.UTC(), NotAfter: record.NotAfter.UTC()}
	}
	return keys, windows, nil
}
