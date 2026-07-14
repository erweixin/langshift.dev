// Package anonymoussession creates signed opaque browser handles for the
// restricted anonymous Route Preview principal. Handles never encode an
// internal subject or user identifier.
package anonymoussession

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	Version        = "LITES-A1"
	CookieName     = "__Host-lites_anonymous"
	CSRFCookieName = "__Host-lites_csrf"
	MaximumTTL     = 24 * time.Hour
	tokenBytes     = 32
	minimumKeySize = 32
)

var (
	ErrInvalidHandle   = errors.New("anonymous handle is invalid")
	ErrExpired         = errors.New("anonymous handle is expired")
	ErrUnauthenticated = errors.New("anonymous session is not authenticated")
	keyIDPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

type Credential struct {
	Raw       string
	Digest    [sha256.Size]byte
	ExpiresAt time.Time
}

type Signer struct {
	KeyID        string
	Key          []byte
	DigestPepper []byte
	Random       io.Reader
}

type Verifier struct {
	Keys         map[string][]byte
	DigestPepper []byte
}

type Principal struct {
	AnonymousSubjectID string
	UserID             string
	TenantID           string
	ExpiresAt          time.Time
}

type Resolver interface {
	Resolve(context.Context, string) (Principal, error)
}

func (signer Signer) New(now time.Time, ttl time.Duration) (Credential, error) {
	if !keyIDPattern.MatchString(signer.KeyID) || len(signer.Key) < minimumKeySize || len(signer.DigestPepper) < minimumKeySize || ttl <= 0 || ttl > MaximumTTL {
		return Credential{}, ErrInvalidHandle
	}
	randomSource := signer.Random
	if randomSource == nil {
		randomSource = rand.Reader
	}
	token := make([]byte, tokenBytes)
	if _, err := io.ReadFull(randomSource, token); err != nil {
		return Credential{}, fmt.Errorf("read anonymous handle entropy: %w", err)
	}
	opaque := base64.RawURLEncoding.EncodeToString(token)
	expiresAt := now.UTC().Add(ttl).Truncate(time.Second)
	prefix := strings.Join([]string{Version, signer.KeyID, opaque, strconv.FormatInt(expiresAt.Unix(), 10)}, ".")
	signature := sign(prefix, signer.Key)
	raw := prefix + "." + base64.RawURLEncoding.EncodeToString(signature)
	digest := digestOpaque(token, signer.DigestPepper)
	return Credential{Raw: raw, Digest: digest, ExpiresAt: expiresAt}, nil
}

func (verifier Verifier) Verify(raw string, now time.Time) (Credential, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 5 || parts[0] != Version || !keyIDPattern.MatchString(parts[1]) {
		return Credential{}, ErrInvalidHandle
	}
	key := verifier.Keys[parts[1]]
	if len(key) < minimumKeySize || len(verifier.DigestPepper) < minimumKeySize {
		return Credential{}, ErrInvalidHandle
	}
	opaque, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(opaque) != tokenBytes {
		return Credential{}, ErrInvalidHandle
	}
	expiresUnix, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || expiresUnix <= 0 {
		return Credential{}, ErrInvalidHandle
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[4])
	if err != nil || len(signature) != sha256.Size {
		return Credential{}, ErrInvalidHandle
	}
	prefix := strings.Join(parts[:4], ".")
	if !hmac.Equal(signature, sign(prefix, key)) {
		return Credential{}, ErrInvalidHandle
	}
	expiresAt := time.Unix(expiresUnix, 0).UTC()
	if !expiresAt.After(now.UTC()) {
		return Credential{}, ErrExpired
	}
	return Credential{Raw: raw, Digest: digestOpaque(opaque, verifier.DigestPepper), ExpiresAt: expiresAt}, nil
}

// CSRF derives an unguessable double-submit value bound to this exact handle.
func CSRF(raw string, key []byte) (string, error) {
	if raw == "" || len(key) < minimumKeySize {
		return "", ErrInvalidHandle
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("anonymous-csrf\x00"))
	_, _ = mac.Write([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func VerifyCSRF(rawHandle, candidate string, key []byte) bool {
	expected, err := CSRF(rawHandle, key)
	return err == nil && candidate != "" && hmac.Equal([]byte(expected), []byte(candidate))
}

func Cookie(raw string, expiresAt time.Time) (*http.Cookie, error) {
	parts := strings.Split(raw, ".")
	if len(raw) > 512 || len(parts) != 5 || parts[0] != Version || !keyIDPattern.MatchString(parts[1]) || expiresAt.IsZero() {
		return nil, ErrInvalidHandle
	}
	opaque, opaqueErr := base64.RawURLEncoding.DecodeString(parts[2])
	_, expiryErr := strconv.ParseInt(parts[3], 10, 64)
	signature, signatureErr := base64.RawURLEncoding.DecodeString(parts[4])
	if opaqueErr != nil || len(opaque) != tokenBytes || expiryErr != nil || signatureErr != nil || len(signature) != sha256.Size {
		return nil, ErrInvalidHandle
	}
	return &http.Cookie{Name: CookieName, Value: raw, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: expiresAt.UTC()}, nil
}

func ClearCookie() *http.Cookie {
	return &http.Cookie{Name: CookieName, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0).UTC()}
}

func CSRFCookie(rawHandle string, key []byte, expiresAt time.Time) (*http.Cookie, error) {
	value, err := CSRF(rawHandle, key)
	if err != nil || expiresAt.IsZero() {
		return nil, ErrInvalidHandle
	}
	return &http.Cookie{Name: CSRFCookieName, Value: value, Path: "/", Secure: true, HttpOnly: false, SameSite: http.SameSiteLaxMode, Expires: expiresAt.UTC()}, nil
}

func sign(value string, key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}

func digestOpaque(opaque, pepper []byte) [sha256.Size]byte {
	mac := hmac.New(sha256.New, pepper)
	_, _ = mac.Write([]byte("anonymous-handle\x00"))
	_, _ = mac.Write(opaque)
	var digest [sha256.Size]byte
	copy(digest[:], mac.Sum(nil))
	return digest
}
