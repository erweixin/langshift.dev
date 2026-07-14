// Package opaque issues purpose-separated high-entropy bearer credentials.
// Only the keyed digest is persisted.
package opaque

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

const tokenBytes = 32

var ErrInvalid = errors.New("opaque credential is invalid")

type Credential struct {
	Raw    string
	Digest [sha256.Size]byte
}

type Manager struct {
	Purpose string
	Pepper  []byte
	Random  io.Reader
}

func (manager Manager) Issue() (Credential, error) {
	if err := manager.validate(); err != nil {
		return Credential{}, err
	}
	randomSource := manager.Random
	if randomSource == nil {
		randomSource = rand.Reader
	}
	value := make([]byte, tokenBytes)
	if _, err := io.ReadFull(randomSource, value); err != nil {
		return Credential{}, fmt.Errorf("read token entropy: %w", err)
	}
	raw := base64.RawURLEncoding.EncodeToString(value)
	digest, err := manager.Digest(raw)
	if err != nil {
		return Credential{}, err
	}
	return Credential{Raw: raw, Digest: digest}, nil
}

func (manager Manager) Digest(raw string) ([sha256.Size]byte, error) {
	var empty [sha256.Size]byte
	if err := manager.validate(); err != nil {
		return empty, err
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) != tokenBytes {
		return empty, ErrInvalid
	}
	mac := hmac.New(sha256.New, manager.Pepper)
	_, _ = mac.Write([]byte(manager.Purpose))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(decoded)
	var digest [sha256.Size]byte
	copy(digest[:], mac.Sum(nil))
	return digest, nil
}

func (manager Manager) validate() error {
	if manager.Purpose == "" || len(manager.Pepper) < 32 {
		return ErrInvalid
	}
	return nil
}
