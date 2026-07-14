package anonymoussession

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSignedOpaqueHandleRoundTripAndTamperResistance(t *testing.T) {
	now := time.Unix(1_800_000_000, 123).UTC()
	key := bytes.Repeat([]byte{0x51}, 32)
	pepper := bytes.Repeat([]byte{0x52}, 32)
	signer := Signer{KeyID: "anonymous-2026-07", Key: key, DigestPepper: pepper, Random: bytes.NewReader(bytes.Repeat([]byte{0x53}, tokenBytes))}
	credential, err := signer.New(now, MaximumTTL)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(credential.Raw, "subject") || strings.Contains(credential.Raw, "user") {
		t.Fatal("handle exposed an internal principal")
	}
	verified, err := (Verifier{Keys: map[string][]byte{"anonymous-2026-07": key}, DigestPepper: pepper}).Verify(credential.Raw, now)
	if err != nil || verified.Digest != credential.Digest || !verified.ExpiresAt.Equal(credential.ExpiresAt) {
		t.Fatalf("verified=%#v error=%v", verified, err)
	}
	parts := strings.Split(credential.Raw, ".")
	parts[2] = strings.Repeat("A", len(parts[2]))
	if _, err = (Verifier{Keys: map[string][]byte{"anonymous-2026-07": key}, DigestPepper: pepper}).Verify(strings.Join(parts, "."), now); !errors.Is(err, ErrInvalidHandle) {
		t.Fatalf("tamper error=%v", err)
	}
	if _, err = (Verifier{Keys: map[string][]byte{"anonymous-2026-07": key}, DigestPepper: pepper}).Verify(credential.Raw, credential.ExpiresAt); !errors.Is(err, ErrExpired) {
		t.Fatalf("expiry error=%v", err)
	}
}

func TestAnonymousCookieAndBoundCSRFProperties(t *testing.T) {
	key := bytes.Repeat([]byte{0x61}, 32)
	pepper := bytes.Repeat([]byte{0x62}, 32)
	now := time.Unix(1_800_100_000, 0).UTC()
	credential, err := (Signer{KeyID: "cookie-key", Key: key, DigestPepper: pepper, Random: bytes.NewReader(bytes.Repeat([]byte{0x63}, tokenBytes))}).New(now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cookie, err := Cookie(credential.Raw, credential.ExpiresAt)
	if err != nil || cookie.Name != CookieName || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Fatalf("cookie=%#v error=%v", cookie, err)
	}
	csrf, err := CSRF(credential.Raw, key)
	if err != nil || !VerifyCSRF(credential.Raw, csrf, key) || VerifyCSRF("other.handle", csrf, key) {
		t.Fatalf("csrf=%q error=%v", csrf, err)
	}
	csrfCookie, err := CSRFCookie(credential.Raw, key, credential.ExpiresAt)
	if err != nil || csrfCookie.Name != CSRFCookieName || !csrfCookie.Secure || csrfCookie.HttpOnly || csrfCookie.SameSite != http.SameSiteLaxMode || csrfCookie.Value != csrf {
		t.Fatalf("csrf cookie=%#v error=%v", csrfCookie, err)
	}
}

func TestAnonymousHandleRejectsWeakKeysAndExcessiveTTL(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	signer := Signer{KeyID: "key", Key: bytes.Repeat([]byte{1}, 32), DigestPepper: bytes.Repeat([]byte{2}, 32)}
	if _, err := signer.New(now, MaximumTTL+time.Second); !errors.Is(err, ErrInvalidHandle) {
		t.Fatalf("ttl error=%v", err)
	}
	signer.Key = []byte("weak")
	if _, err := signer.New(now, time.Hour); !errors.Is(err, ErrInvalidHandle) {
		t.Fatalf("key error=%v", err)
	}
}
