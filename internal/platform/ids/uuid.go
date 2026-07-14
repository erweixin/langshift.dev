// Package ids creates cryptographically random and keyed deterministic UUIDs.
package ids

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
)

var ErrInvalidKey = errors.New("deterministic id key is invalid")
var ErrInvalidRandom = errors.New("uuid random source is invalid")

func NewUUID() (string, error) { return NewUUIDFrom(rand.Reader) }

func NewUUIDFrom(random io.Reader) (string, error) {
	if random == nil {
		return "", ErrInvalidRandom
	}
	value := make([]byte, 16)
	if _, err := io.ReadFull(random, value); err != nil {
		return "", fmt.Errorf("read uuid entropy: %w", err)
	}
	return formatUUID(value), nil
}

func DeterministicUUID(key []byte, domain, value string) (string, error) {
	if len(key) < 32 || domain == "" || value == "" {
		return "", ErrInvalidKey
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(domain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(value))
	return formatUUID(mac.Sum(nil)[:16]), nil
}

func formatUUID(value []byte) string {
	copyValue := append([]byte(nil), value[:16]...)
	copyValue[6] = (copyValue[6] & 0x0f) | 0x40
	copyValue[8] = (copyValue[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", copyValue[0:4], copyValue[4:6], copyValue[6:8], copyValue[8:10], copyValue[10:16])
}
