package sessionrequest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	runtimecontract "github.com/langshift/lites/internal/runtime"
)

func TestPolicySatisfiesPinnedRequirementsWithoutWeakening(t *testing.T) {
	empty := sha256.Sum256(nil)
	requirement := Requirements{PolicySnapshotID: "70000000-0000-4000-8000-000000000001", PolicySnapshotKey: "runtime-policy:untrusted:v1",
		TrustTier: "untrusted", IsolationKind: "firecracker", ImageDigest: "sha256:" + string64("a"),
		WorkspaceMode: runtimecontract.WorkspaceNone, NetworkMode: "none", NetworkPolicyHash: "sha256:" + hex.EncodeToString(empty[:]),
		SecretMode: "none", SecretScopeHash: "sha256:" + hex.EncodeToString(empty[:]), MinimumVCPU: 1,
		MinimumMemoryMiB: 256, MinimumDiskMiB: 1024, MinimumPids: 64, MaximumDuration: 30 * time.Second}
	policy := policyRow{ID: requirement.PolicySnapshotID, SnapshotKey: requirement.PolicySnapshotKey, PolicyHash: string64("b"),
		TrustTier: requirement.TrustTier, IsolationKind: requirement.IsolationKind, ImageDigest: requirement.ImageDigest,
		WorkspaceMode: string(requirement.WorkspaceMode), NetworkMode: requirement.NetworkMode, NetworkPolicyHash: requirement.NetworkPolicyHash,
		SecretMode: requirement.SecretMode, SecretScopeHash: requirement.SecretScopeHash, VCPU: 2, MemoryMiB: 512,
		DiskMiB: 2048, Pids: 128, MaximumSeconds: 60}
	if !policySatisfies(policy, requirement) {
		t.Fatal("stronger resource policy should satisfy exact isolation requirements")
	}
	tests := []func(*policyRow){
		func(value *policyRow) { value.TrustTier = "semi_trusted" },
		func(value *policyRow) { value.NetworkMode = "broker_only" },
		func(value *policyRow) { value.SecretScopeHash = "sha256:" + string64("c") },
		func(value *policyRow) { value.MemoryMiB = 128 },
	}
	for index, mutate := range tests {
		candidate := policy
		mutate(&candidate)
		if policySatisfies(candidate, requirement) {
			t.Fatalf("weakened/mismatched policy %d was accepted", index)
		}
	}
	requirement.ApprovalRequired = true
	if policySatisfies(policy, requirement) {
		t.Fatal("approval requirement was weakened")
	}
}

func TestValidCommandRequiresExactWorkspaceApprovalAndRequestBindings(t *testing.T) {
	command := validRequestCommand()
	if !validCommand(command) {
		t.Fatal("valid command rejected")
	}
	command.Requirements.WorkspaceMode = runtimecontract.WorkspaceReadWrite
	if validCommand(command) {
		t.Fatal("workspace authority without an immutable base was accepted")
	}
	command.WorkspaceID, command.BaseWorkspaceRevision = "workspace", "revision"
	if !validCommand(command) {
		t.Fatal("bound workspace rejected")
	}
	command.Requirements.ApprovalRequired = true
	if validCommand(command) {
		t.Fatal("approval-required request without approval was accepted")
	}
	command.ApprovalID, command.ApprovalVersion = "approval", 1
	if !validCommand(command) {
		t.Fatal("exact approval binding rejected")
	}
	command.ApprovalVersion = 0
	if validCommand(command) {
		t.Fatal("partial approval binding accepted")
	}
}

func TestStoreRejectsConfigurationBeforeCommand(t *testing.T) {
	_, err := (Store{}).Request(t.Context(), Command{})
	if !errors.Is(err, ErrConfiguration) {
		t.Fatalf("configuration error = %v", err)
	}
}

func TestDeterministicNonceAndCapabilityAreReplayStable(t *testing.T) {
	store := Store{NonceKey: bytes.Repeat([]byte{0x31}, 32)}
	nonce := store.nonce("80000000-0000-4000-8000-000000000001")
	if nonce == "" || nonce != store.nonce("80000000-0000-4000-8000-000000000001") || nonce == store.nonce("80000000-0000-4000-8000-000000000002") {
		t.Fatal("nonce derivation is not stable and session scoped")
	}
	public, private, err := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{0x42}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	requested := Requested{SessionID: "session", TenantID: "tenant", UserID: "user", RunID: "run", ToolCallID: "tool",
		CommandID: "command", AttemptID: "attempt", Fence: 2, PolicySnapshotKey: "runtime-policy:untrusted:v1", PolicyHash: string64("a"),
		WorkspaceMode: runtimecontract.WorkspaceNone, NetworkPolicyHash: "sha256:" + string64("b"), SecretScopeHash: "sha256:" + string64("c"),
		RequestHash: string64("d"), LeaseTokenHash: "MTExMTExMTExMTExMTExMTExMTExMTExMTExMTExMTE", Nonce: nonce,
		RequestedAt: now, ExecutionLeaseExpiresAt: now.Add(4 * time.Minute), ExecutionDeadline: now.Add(3 * time.Minute)}
	issuer := Issuer{Issuer: "tool-worker", Audience: "runtime-host", KeyID: "runtime-key-v1", PrivateKey: private, TTL: 5 * time.Minute, Now: func() time.Time { return now }}
	first, err := issuer.issue(requested)
	if err != nil {
		t.Fatal(err)
	}
	second, err := issuer.issue(requested)
	if err != nil || first.CapabilityToken != second.CapabilityToken {
		t.Fatal("capability replay changed signed authority")
	}
	claims, err := (runtimecontract.CapabilityVerifier{Issuer: "tool-worker", Audience: "runtime-host", Keys: map[string]ed25519.PublicKey{"runtime-key-v1": public}, MaximumTTL: 5 * time.Minute}).Verify(first.CapabilityToken, now.Add(time.Minute))
	if err != nil || claims.ExpiresAt != now.Add(3*time.Minute).Unix() || claims.Nonce != nonce {
		t.Fatalf("signed claims = %#v, %v", claims, err)
	}
	requested.ApprovalID, requested.ApprovalVersion = "approval", 7
	third, err := issuer.issue(requested)
	if err != nil || third.CapabilityToken == first.CapabilityToken {
		t.Fatal("approval binding did not affect signed capability")
	}
}

func TestIssuerRejectsExpiredDurableRequest(t *testing.T) {
	_, private, _ := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{0x43}, 64)))
	now := time.Now().UTC().Truncate(time.Second)
	issuer := Issuer{Issuer: "tool-worker", Audience: "runtime-host", KeyID: "runtime-key", PrivateKey: private, TTL: time.Minute}
	_, err := issuer.issue(Requested{TenantID: "tenant", UserID: "user", RunID: "run", ToolCallID: "tool", CommandID: "command", AttemptID: "attempt", Fence: 1,
		PolicySnapshotKey: "runtime-policy:v1", PolicyHash: string64("a"), WorkspaceMode: runtimecontract.WorkspaceNone,
		NetworkPolicyHash: "sha256:" + string64("b"), SecretScopeHash: "sha256:" + string64("c"), RequestHash: string64("d"),
		LeaseTokenHash: "MTExMTExMTExMTExMTExMTExMTExMTExMTExMTExMTE", Nonce: "MTExMTExMTExMTExMTExMQ",
		RequestedAt: now.Add(-2 * time.Minute), ExecutionLeaseExpiresAt: now.Add(-time.Minute), ExecutionDeadline: now.Add(-time.Minute)})
	if !errors.Is(err, ErrSigning) {
		t.Fatalf("expired durable request error = %v", err)
	}
}

func validRequestCommand() Command {
	empty := sha256.Sum256(nil)
	return Command{Claim: executionpostgres.ToolClaim{ToolCallID: "tool", RunID: "run", TenantID: "tenant", UserID: "user", StoreEpoch: "epoch",
		CommandID: "command", ConsumerName: "worker", RequestHash: "delivery", InboxID: "inbox", AttemptID: "attempt", Fence: 1,
		LeaseToken: "lease", LeaseExpiresAt: time.Now().Add(time.Minute), Binding: executionpostgres.ToolBinding{ToolName: "tool",
			DescriptorSnapshotID: "tool@1.0.0", RequestHash: string64("a")}}, Requirements: Requirements{
		PolicySnapshotID: "policy", PolicySnapshotKey: "runtime-policy:v1", TrustTier: "untrusted", IsolationKind: "firecracker",
		ImageDigest: "sha256:" + string64("b"), WorkspaceMode: runtimecontract.WorkspaceNone, NetworkMode: "none",
		NetworkPolicyHash: "sha256:" + hex.EncodeToString(empty[:]), SecretMode: "none", SecretScopeHash: "sha256:" + hex.EncodeToString(empty[:]),
		MinimumVCPU: 1, MinimumMemoryMiB: 128, MinimumDiskMiB: 64, MinimumPids: 32, MaximumDuration: 30 * time.Second},
		Payload: PayloadPointer{Ref: "encrypted://runtime/request", Hash: "payload"}, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: "correlation"}
}

func string64(value string) string {
	result := ""
	for len(result) < 64 {
		result += value
	}
	return result[:64]
}
