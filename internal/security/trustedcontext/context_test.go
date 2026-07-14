package trustedcontext

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTrustedContextRoundTripAndBoundaries(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	claims := Claims{Issuer: "lites-gateway", Audience: "identity-service", SubjectID: "user-1", TenantID: "tenant-1", MembershipID: "membership-1", SessionID: "session-1", Roles: []string{"member"}, RequestID: "request-1", RequestMethod: "PATCH", RequestTarget: "/v1/account?view=full", CSRFVerified: true, IssuedAt: now.Unix(), ExpiresAt: now.Add(2 * time.Minute).Unix(), Nonce: "nonce-1"}
	token, err := Sign(claims, "gateway-2026-07", privateKey, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	verifier := Verifier{Issuer: "lites-gateway", Audience: "identity-service", Keys: map[string]ed25519.PublicKey{"gateway-2026-07": publicKey}, MaximumTTL: 5 * time.Minute, ClockSkew: 5 * time.Second}
	got, err := verifier.Verify(token, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.SubjectID != claims.SubjectID || got.TenantID != claims.TenantID {
		t.Fatalf("unexpected claims: %#v", got)
	}
	if _, err := verifier.VerifyRequest(token, now, "request-1", "PATCH", "/v1/account?view=full", true); err != nil {
		t.Fatalf("request binding: %v", err)
	}
	if _, err := verifier.VerifyRequest(token, now, "request-1", "GET", "/v1/account?view=full", false); !errors.Is(err, ErrInvalidClaims) {
		t.Fatalf("method replay error=%v", err)
	}

	parts := strings.Split(token, ".")
	parts[1] = parts[1][:len(parts[1])-1] + "A"
	if _, err := verifier.Verify(strings.Join(parts, "."), now); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("tamper error=%v", err)
	}
	if _, err := verifier.Verify(token, now.Add(3*time.Minute)); !errors.Is(err, ErrExpired) {
		t.Fatalf("expiry error=%v", err)
	}
	verifier.Audience = "event-service"
	if _, err := verifier.Verify(token, now); !errors.Is(err, ErrInvalidClaims) {
		t.Fatalf("audience error=%v", err)
	}
}

func TestTrustedContextRejectsExcessiveTTLAndDuplicateRoles(t *testing.T) {
	_, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	base := Claims{Issuer: "gateway", Audience: "identity", SubjectID: "u", TenantID: "t", MembershipID: "m", SessionID: "s", Roles: []string{"member"}, RequestID: "r", RequestMethod: "GET", RequestTarget: "/", IssuedAt: 100, ExpiresAt: 1000, Nonce: "n"}
	if _, err := Sign(base, "key", privateKey, 5*time.Minute); !errors.Is(err, ErrInvalidClaims) {
		t.Fatalf("ttl error=%v", err)
	}
	base.ExpiresAt = 200
	base.Roles = []string{"member", "member"}
	if _, err := Sign(base, "key", privateKey, 5*time.Minute); !errors.Is(err, ErrInvalidClaims) {
		t.Fatalf("roles error=%v", err)
	}
}
