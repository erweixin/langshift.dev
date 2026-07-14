// Package session creates opaque session credentials. Only the keyed digest is
// persisted; the raw token exists only in the secure cookie response.
package session

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	CookieName = "__Host-lites_session"
	tokenBytes = 32
)

var ErrInvalidToken = errors.New("session token is invalid")

type Credential struct {
	Raw    string
	Digest [sha256.Size]byte
}

func New(pepper []byte) (Credential, error) { return NewFrom(rand.Reader, pepper) }

func NewFrom(random io.Reader, pepper []byte) (Credential, error) {
	if len(pepper) < 32 {
		return Credential{}, errors.New("session pepper must contain at least 32 bytes")
	}
	value := make([]byte, tokenBytes)
	if _, err := io.ReadFull(random, value); err != nil {
		return Credential{}, fmt.Errorf("read session entropy: %w", err)
	}
	raw := base64.RawURLEncoding.EncodeToString(value)
	digest, err := Digest(raw, pepper)
	if err != nil {
		return Credential{}, err
	}
	return Credential{Raw: raw, Digest: digest}, nil
}

func Digest(raw string, pepper []byte) ([sha256.Size]byte, error) {
	var empty [sha256.Size]byte
	if len(pepper) < 32 {
		return empty, errors.New("session pepper must contain at least 32 bytes")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) != tokenBytes {
		return empty, ErrInvalidToken
	}
	mac := hmac.New(sha256.New, pepper)
	_, _ = mac.Write(decoded)
	var digest [sha256.Size]byte
	copy(digest[:], mac.Sum(nil))
	return digest, nil
}

func ConstantTimeEqual(left, right [sha256.Size]byte) bool { return hmac.Equal(left[:], right[:]) }

func Cookie(raw string, expiresAt time.Time) (*http.Cookie, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) != tokenBytes {
		return nil, ErrInvalidToken
	}
	return &http.Cookie{Name: CookieName, Value: raw, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: expiresAt.UTC()}, nil
}

func ClearCookie() *http.Cookie {
	return &http.Cookie{Name: CookieName, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0).UTC()}
}
