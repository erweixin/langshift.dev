// Package idempotency defines the tenant-principal-operation replay contract.
// Raw keys and raw response bodies are never persisted.
package idempotency

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
)

var (
	ErrInvalidKey  = errors.New("idempotency key is invalid")
	ErrKeyConflict = errors.New("idempotency key was used for a different request")
	ErrInProgress  = errors.New("idempotent operation is still in progress")
)

type Scope struct{ TenantID, UserID, OperationID string }

type Response struct {
	Status          int
	ContentType     string
	PayloadRef      string
	Hash            string
	ResourceVersion uint64
}

func KeyDigest(raw string, pepper []byte) ([sha256.Size]byte, error) {
	var empty [sha256.Size]byte
	if len(raw) < 16 || len(raw) > 200 || len(pepper) < 32 {
		return empty, ErrInvalidKey
	}
	mac := hmac.New(sha256.New, pepper)
	_, _ = mac.Write([]byte(raw))
	var digest [sha256.Size]byte
	copy(digest[:], mac.Sum(nil))
	return digest, nil
}

func RequestHash(canonical []byte) string { sum := sha256.Sum256(canonical); return stringHex(sum[:]) }

func stringHex(value []byte) string {
	const alphabet = "0123456789abcdef"
	encoded := make([]byte, len(value)*2)
	for index, item := range value {
		encoded[index*2] = alphabet[item>>4]
		encoded[index*2+1] = alphabet[item&15]
	}
	return string(encoded)
}
