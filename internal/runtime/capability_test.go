package runtime

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func validCapability(now time.Time) CapabilityClaims {
	hash := strings.Repeat("a", 64)
	return CapabilityClaims{
		Purpose: "runtime_session", Issuer: "event-service", Audience: "runtime-manager",
		TenantID: "tenant-1", UserID: "user-1", RunID: "run-1", ToolCallID: "tool-1", CommandID: "command-1", AttemptID: "attempt-1", Fence: 17,
		PolicySnapshotID: "runtime-policy:untrusted:v1", PolicyHash: hash,
		WorkspaceID: "workspace-1", BaseWorkspaceRevision: "revision-7", WorkspaceMode: WorkspaceReadWrite,
		NetworkPolicyHash: emptyPolicyHash(), SecretScopeHash: emptyPolicyHash(),
		LeaseTokenHash: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32)), RequestHash: hash,
		IssuedAt: now.Unix(), ExpiresAt: now.Add(2 * time.Minute).Unix(), Nonce: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x51}, 16)),
	}
}

func TestRuntimeCapabilityBindsEntireExecutionRightAndRotatingKeyWindow(t *testing.T) {
	now := time.Date(2026, 7, 15, 7, 0, 0, 0, time.UTC)
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x31}, ed25519.SeedSize))
	publicKey := privateKey.Public().(ed25519.PublicKey)
	claims := validCapability(now)
	token, err := SignCapability(claims, "runtime-key-v1", privateKey, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	verifier := CapabilityVerifier{
		Issuer: "event-service", Audience: "runtime-manager", Keys: map[string]ed25519.PublicKey{"runtime-key-v1": publicKey},
		KeyWindows: map[string]CapabilityKeyWindow{"runtime-key-v1": {NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}},
		MaximumTTL: 5 * time.Minute, ClockSkew: time.Second,
	}
	verified, err := verifier.Verify(token, now)
	if err != nil || verified.Fence != 17 || verified.PolicyHash != claims.PolicyHash || verified.LeaseTokenHash != claims.LeaseTokenHash || verified.RequestHash != claims.RequestHash || verified.WorkspaceID != claims.WorkspaceID {
		t.Fatalf("Verify() = %#v, %v", verified, err)
	}

	tampered := strings.Split(token, ".")
	tampered[1] = capabilityEncode([]byte(`{"purpose":"runtime_session"}`))
	if _, err = verifier.Verify(strings.Join(tampered, "."), now); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("tampered token = %v", err)
	}
	verifier.Audience = "workspace-service"
	if _, err = verifier.Verify(token, now); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("cross-audience token = %v", err)
	}
}

func TestRuntimeCapabilityRejectsExpiryAndScopeAmbiguity(t *testing.T) {
	now := time.Date(2026, 7, 15, 7, 0, 0, 0, time.UTC)
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x31}, ed25519.SeedSize))
	publicKey := privateKey.Public().(ed25519.PublicKey)
	claims := validCapability(now)
	token, err := SignCapability(claims, "runtime-key-v1", privateKey, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	verifier := CapabilityVerifier{Issuer: "event-service", Audience: "runtime-manager", Keys: map[string]ed25519.PublicKey{"runtime-key-v1": publicKey}, MaximumTTL: 5 * time.Minute}
	if _, err = verifier.Verify(token, time.Unix(claims.ExpiresAt, 0)); !errors.Is(err, ErrExpiredCapability) {
		t.Fatalf("exact expiry = %v", err)
	}

	tests := []func(*CapabilityClaims){
		func(value *CapabilityClaims) { value.Fence = 0 },
		func(value *CapabilityClaims) { value.WorkspaceMode = WorkspaceNone },
		func(value *CapabilityClaims) { value.LeaseTokenHash = "raw-secret" },
		func(value *CapabilityClaims) { value.ApprovalVersion = 2 },
		func(value *CapabilityClaims) { value.ExpiresAt = value.IssuedAt + 301 },
	}
	for index, edit := range tests {
		value := validCapability(now)
		edit(&value)
		if _, err = SignCapability(value, "runtime-key-v1", privateKey, 5*time.Minute); !errors.Is(err, ErrInvalidCapability) {
			t.Fatalf("case %d: %v", index, err)
		}
	}
	if _, err = SignCapability(validCapability(now), "runtime-key-v1", privateKey, time.Hour); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("oversized signing policy TTL = %v", err)
	}
}
