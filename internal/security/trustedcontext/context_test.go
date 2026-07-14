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
	claims := Claims{PrincipalKind: AuthenticatedUser, Issuer: "lites-gateway", Audience: "identity-service", SubjectID: "user-1", TenantID: "tenant-1", MembershipID: "membership-1", SessionID: "session-1", Roles: []string{"member"}, RequestID: "request-1", RequestMethod: "PATCH", RequestTarget: "/v1/account?view=full", ClientIPHash: "YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE", UserAgentHash: "YmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmI", CSRFVerified: true, IssuedAt: now.Unix(), ExpiresAt: now.Add(2 * time.Minute).Unix(), Nonce: "nonce-1"}
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
	base := Claims{PrincipalKind: AuthenticatedUser, Issuer: "gateway", Audience: "identity", SubjectID: "u", TenantID: "t", MembershipID: "m", SessionID: "s", Roles: []string{"member"}, RequestID: "r", RequestMethod: "GET", RequestTarget: "/", ClientIPHash: "YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE", UserAgentHash: "YmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmI", IssuedAt: 100, ExpiresAt: 1000, Nonce: "n"}
	if _, err := Sign(base, "key", privateKey, 5*time.Minute); !errors.Is(err, ErrInvalidClaims) {
		t.Fatalf("ttl error=%v", err)
	}
	base.ExpiresAt = 200
	base.Roles = []string{"member", "member"}
	if _, err := Sign(base, "key", privateKey, 5*time.Minute); !errors.Is(err, ErrInvalidClaims) {
		t.Fatalf("roles error=%v", err)
	}
}

func TestPublicContextCannotSmuggleIdentity(t *testing.T) {
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Unix(1_800_000_000, 0)
	base := Claims{PrincipalKind: PublicRequest, Issuer: "gateway", Audience: "identity", RequestID: "request-public", RequestMethod: "POST", RequestTarget: "/v1/auth/login", ClientIPHash: "YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE", UserAgentHash: "YmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmI", CSRFVerified: true, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Minute).Unix(), Nonce: "nonce-public"}
	token, err := Sign(base, "key", privateKey, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	verifier := Verifier{Issuer: "gateway", Audience: "identity", Keys: map[string]ed25519.PublicKey{"key": publicKey}, MaximumTTL: 5 * time.Minute}
	claims, err := verifier.VerifyRequest(token, now, base.RequestID, base.RequestMethod, base.RequestTarget, true)
	if err != nil || claims.PrincipalKind != PublicRequest {
		t.Fatalf("claims=%#v err=%v", claims, err)
	}
	base.SubjectID = "attacker"
	base.Roles = []string{"owner"}
	if _, err = Sign(base, "key", privateKey, 5*time.Minute); !errors.Is(err, ErrInvalidClaims) {
		t.Fatalf("public identity smuggling error=%v", err)
	}
}

func TestAnonymousContextRequiresIsolatedEphemeralPrincipal(t *testing.T) {
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Unix(1_800_000_000, 0)
	claims := Claims{PrincipalKind: AnonymousUser, Issuer: "gateway", Audience: "identity", SubjectID: "ephemeral-user", TenantID: "anonymous-system", AnonymousSubjectID: "anonymous-subject", Roles: []string{"anonymous_preview"}, RequestID: "request", RequestMethod: "PATCH", RequestTarget: "/v1/onboarding-sessions/route", ClientIPHash: "YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE", UserAgentHash: "YmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmI", CSRFVerified: true, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Minute).Unix(), Nonce: "nonce"}
	token, err := Sign(claims, "key", privateKey, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := (Verifier{Issuer: "gateway", Audience: "identity", Keys: map[string]ed25519.PublicKey{"key": publicKey}, MaximumTTL: 5 * time.Minute}).Verify(token, now)
	if err != nil || verified.PrincipalKind != AnonymousUser || verified.SubjectID != "ephemeral-user" || verified.AnonymousSubjectID != "anonymous-subject" {
		t.Fatalf("claims=%#v error=%v", verified, err)
	}
	claims.MembershipID = "smuggled-membership"
	if _, err = Sign(claims, "key", privateKey, 5*time.Minute); !errors.Is(err, ErrInvalidClaims) {
		t.Fatalf("membership smuggling error=%v", err)
	}
}
