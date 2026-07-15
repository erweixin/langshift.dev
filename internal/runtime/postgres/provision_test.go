package postgres

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	runtimecontract "github.com/langshift/lites/internal/runtime"
)

func provisionFixture(t *testing.T) (runtimecontract.CapabilityClaims, sessionRow, RuntimePolicy, time.Time) {
	t.Helper()
	now := time.Date(2026, 7, 15, 12, 0, 10, 0, time.UTC)
	issued := now.Add(-10 * time.Second)
	hash := strings.Repeat("a", 64)
	empty := "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	nonce := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x51}, 16))
	nonceHash, err := runtimecontract.CapabilityNonceDigest(nonce)
	if err != nil {
		t.Fatal(err)
	}
	lease := bytes.Repeat([]byte{0x42}, 32)
	claims := runtimecontract.CapabilityClaims{
		Purpose: "runtime_session", Issuer: "event-service", Audience: "runtime-manager",
		TenantID: "tenant-1", UserID: "user-1", RunID: "run-1", ToolCallID: "tool-1", CommandID: "command-1", AttemptID: "attempt-1", Fence: 7,
		PolicySnapshotID: "runtime-policy:untrusted:v1", PolicyHash: hash, WorkspaceMode: runtimecontract.WorkspaceNone,
		NetworkPolicyHash: empty, SecretScopeHash: empty, LeaseTokenHash: base64.RawURLEncoding.EncodeToString(lease), RequestHash: hash,
		IssuedAt: issued.Unix(), ExpiresAt: now.Add(time.Minute).Unix(), Nonce: nonce,
	}
	session := sessionRow{
		ID: "session-1", TenantID: claims.TenantID, UserID: claims.UserID, RunID: claims.RunID, ToolCallID: claims.ToolCallID,
		Version: 1, Status: "requested", PolicySnapshotKey: claims.PolicySnapshotID, PolicyHash: hash, WorkspaceMode: "none",
		NetworkPolicyHash: empty, SecretScopeHash: empty, RequestHash: hash, CapabilityNonceHash: nonceHash[:],
		CommandID: claims.CommandID, ExecutionAttemptID: claims.AttemptID, ExecutionFence: claims.Fence, ExecutionLeaseHash: lease,
		ExecutionLeaseExpiresAt: now.Add(2 * time.Minute), RequestedAt: issued, ExecutionDeadline: now.Add(5 * time.Minute),
	}
	policy := RuntimePolicy{SnapshotKey: claims.PolicySnapshotID, PolicyHash: hash, TrustTier: "untrusted", IsolationKind: "firecracker", NetworkPolicyHash: empty, SecretScopeHash: empty, WorkspaceMode: "none"}
	return claims, session, policy, now
}

func TestCapabilityBindingChecksEveryDurableExecutionDimension(t *testing.T) {
	claims, session, policy, now := provisionFixture(t)
	if err := validateCapabilityBinding(claims, session, policy, now); err != nil {
		t.Fatal(err)
	}
	tests := []func(*runtimecontract.CapabilityClaims, *sessionRow, *RuntimePolicy){
		func(value *runtimecontract.CapabilityClaims, _ *sessionRow, _ *RuntimePolicy) {
			value.TenantID = "tenant-2"
		},
		func(value *runtimecontract.CapabilityClaims, _ *sessionRow, _ *RuntimePolicy) {
			value.UserID = "user-2"
		},
		func(value *runtimecontract.CapabilityClaims, _ *sessionRow, _ *RuntimePolicy) { value.RunID = "run-2" },
		func(value *runtimecontract.CapabilityClaims, _ *sessionRow, _ *RuntimePolicy) {
			value.ToolCallID = "tool-2"
		},
		func(value *runtimecontract.CapabilityClaims, _ *sessionRow, _ *RuntimePolicy) {
			value.CommandID = "command-2"
		},
		func(value *runtimecontract.CapabilityClaims, _ *sessionRow, _ *RuntimePolicy) {
			value.AttemptID = "attempt-2"
		},
		func(value *runtimecontract.CapabilityClaims, _ *sessionRow, _ *RuntimePolicy) { value.Fence++ },
		func(value *runtimecontract.CapabilityClaims, _ *sessionRow, _ *RuntimePolicy) {
			value.PolicyHash = strings.Repeat("b", 64)
		},
		func(value *runtimecontract.CapabilityClaims, _ *sessionRow, _ *RuntimePolicy) {
			value.NetworkPolicyHash = "sha256:" + strings.Repeat("b", 64)
		},
		func(value *runtimecontract.CapabilityClaims, _ *sessionRow, _ *RuntimePolicy) {
			value.SecretScopeHash = "sha256:" + strings.Repeat("b", 64)
		},
		func(value *runtimecontract.CapabilityClaims, _ *sessionRow, _ *RuntimePolicy) {
			value.RequestHash = strings.Repeat("b", 64)
		},
		func(value *runtimecontract.CapabilityClaims, _ *sessionRow, _ *RuntimePolicy) {
			value.LeaseTokenHash = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x43}, 32))
		},
		func(value *runtimecontract.CapabilityClaims, _ *sessionRow, _ *RuntimePolicy) {
			value.Nonce = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x52}, 16))
		},
		func(value *runtimecontract.CapabilityClaims, _ *sessionRow, _ *RuntimePolicy) { value.ExpiresAt += 600 },
		func(value *runtimecontract.CapabilityClaims, _ *sessionRow, _ *RuntimePolicy) { value.IssuedAt -= 60 },
		func(_ *runtimecontract.CapabilityClaims, _ *sessionRow, value *RuntimePolicy) {
			value.PolicyHash = strings.Repeat("b", 64)
		},
	}
	for index, edit := range tests {
		candidateClaims, candidateSession, candidatePolicy := claims, session, policy
		edit(&candidateClaims, &candidateSession, &candidatePolicy)
		if err := validateCapabilityBinding(candidateClaims, candidateSession, candidatePolicy, now); !errors.Is(err, ErrCapabilityBinding) {
			t.Fatalf("case %d: %v", index, err)
		}
	}
}

func TestExactProvisionReplayRequiresSameMachineCIDAttemptLeaseAndExpiry(t *testing.T) {
	_, session, _, now := provisionFixture(t)
	session.Status, session.Version = "provisioning", 2
	session.HostID = sql.NullString{String: "runtime-host-1", Valid: true}
	session.MachineID = sql.NullString{String: "runtime-machine-1", Valid: true}
	session.GuestCID = sql.NullInt64{Int64: 42, Valid: true}
	session.ProvisionAttemptID = sql.NullString{String: "attempt-provision-1", Valid: true}
	session.ProvisionFence = 1
	session.ProvisionLeaseHash = bytes.Repeat([]byte{0x61}, 32)
	session.ProvisionLeaseExpiresAt = sql.NullTime{Time: now.Add(time.Minute), Valid: true}
	command := ProvisionCommand{HostID: "runtime-host-1", MachineID: "runtime-machine-1", GuestCID: 42}
	if !exactProvisionReplay(session, command, "attempt-provision-1", session.ProvisionLeaseHash, session.ProvisionLeaseExpiresAt.Time) {
		t.Fatal("exact replay rejected")
	}
	command.GuestCID++
	if exactProvisionReplay(session, command, "attempt-provision-1", session.ProvisionLeaseHash, session.ProvisionLeaseExpiresAt.Time) {
		t.Fatal("cross-CID replay accepted")
	}
}

func TestTrustTierMapsOnlyToDedicatedFirecrackerPools(t *testing.T) {
	if requiredPool("untrusted") != "runtime-untrusted" || requiredPool("semi_trusted") != "runtime-semi-trusted" || requiredPool("privileged") != "runtime-privileged" || requiredPool("trusted") != "" {
		t.Fatal("unsafe trust-tier pool mapping")
	}
}
