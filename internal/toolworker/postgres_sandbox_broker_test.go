package toolworker

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestSandboxBrokerDerivesStablePurposeSeparatedAuthority(t *testing.T) {
	broker := PostgresSandboxBroker{ProvisionTokenDerivationKey: bytes.Repeat([]byte{0x31}, 32), MachineIdentityKey: bytes.Repeat([]byte{0x32}, 32)}
	lease := broker.provisionLease("session-1")
	decoded, err := base64.RawURLEncoding.DecodeString(lease)
	if err != nil || len(decoded) != 32 || lease != broker.provisionLease("session-1") || lease == broker.provisionLease("session-2") {
		t.Fatalf("invalid deterministic provision credential: %q %v", lease, err)
	}
	machine, cid := broker.machineIdentity("session-1", "runtime-host-1")
	secondMachine, secondCID := broker.machineIdentity("session-1", "runtime-host-1")
	if !strings.HasPrefix(machine, "lites-") || len(machine) > 63 || cid < 3 || machine != secondMachine || cid != secondCID {
		t.Fatalf("machine=%q cid=%d second=%q/%d", machine, cid, secondMachine, secondCID)
	}
}

func TestRuntimePolicyMustExactlyMatchSandboxDescriptor(t *testing.T) {
	registry, snapshot := workerRegistryWithTrust(t, "read_only", "30s", "untrusted")
	_ = registry
	binding := snapshot.Descriptor.RuntimePolicy
	policy := selectedRuntimePolicy{
		ID: "policy-1", SnapshotKey: binding.SnapshotKey, PolicyHash: strings.Repeat("a", 64), TrustTier: "untrusted",
		IsolationKind: binding.IsolationKind, NetworkMode: binding.NetworkMode, NetworkPolicyHash: binding.NetworkPolicyHash,
		SecretMode: binding.SecretMode, SecretScopeHash: binding.SecretScopeHash, WorkspaceMode: binding.WorkspaceMode,
		ImageDigest: "sha256:" + strings.Repeat("a", 64), KernelDigest: "sha256:" + strings.Repeat("b", 64), RootFSDigest: "sha256:" + strings.Repeat("c", 64),
		VCPU: 1, MemoryMiB: 128, DiskMiB: 64, Pids: binding.MinimumPids, MaximumSeconds: 30,
	}
	if !runtimePolicyMatchesDescriptor(policy, snapshot) {
		t.Fatal("exact runtime policy was rejected")
	}
	policy.NetworkPolicyHash = "sha256:" + strings.Repeat("d", 64)
	if runtimePolicyMatchesDescriptor(policy, snapshot) {
		t.Fatal("network authority substitution was accepted")
	}
	policy.NetworkPolicyHash = binding.NetworkPolicyHash
	policy.MaximumSeconds = int((29 * time.Second) / time.Second)
	if runtimePolicyMatchesDescriptor(policy, snapshot) {
		t.Fatal("weaker duration policy was accepted")
	}
}
