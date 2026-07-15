package runtime

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func validPolicy() PolicySnapshot {
	digest := "sha256:" + strings.Repeat("a", 64)
	return PolicySnapshot{
		SchemaVersion: "1", SnapshotID: "runtime-policy:untrusted:v1", TenantID: "tenant-1", Version: 1,
		TrustTier: TrustUntrusted, Isolation: IsolationFirecracker,
		NetworkMode: NetworkNone, NetworkPolicyHash: emptyPolicyHash(), SecretMode: SecretNone, SecretScopeHash: emptyPolicyHash(), WorkspaceMode: WorkspaceReadWrite,
		ImageDigest: digest, KernelDigest: digest, RootFSDigest: digest,
		VCPUCount: 2, MemoryMiB: 512, DiskMiB: 4096, PidsMax: 256,
		MaximumDuration: 10 * time.Minute, IdleTimeout: time.Minute, KillGrace: 5 * time.Second,
		CreatedAt: time.Date(2026, 7, 15, 6, 0, 0, 0, time.UTC),
	}
}

func TestPolicyRejectsIsolationNetworkSecretAndResourceDowngrades(t *testing.T) {
	policy := validPolicy()
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	hash, err := policy.Hash()
	if err != nil || len(hash) != 64 {
		t.Fatalf("Hash() = %q, %v", hash, err)
	}
	tests := []struct {
		name string
		edit func(*PolicySnapshot)
	}{
		{name: "floating image", edit: func(value *PolicySnapshot) { value.ImageDigest = "runtime:latest" }},
		{name: "untrusted container", edit: func(value *PolicySnapshot) { value.Isolation = IsolationContainer }},
		{name: "untrusted network", edit: func(value *PolicySnapshot) {
			value.NetworkMode = NetworkAllowlist
			value.NetworkPolicyHash = value.ImageDigest
		}},
		{name: "untrusted secret", edit: func(value *PolicySnapshot) {
			value.SecretMode = SecretShortLived
			value.SecretScopeHash = value.ImageDigest
			value.NetworkMode = NetworkBroker
			value.NetworkPolicyHash = value.ImageDigest
		}},
		{name: "unbounded cpu", edit: func(value *PolicySnapshot) { value.VCPUCount = 33 }},
		{name: "unbounded duration", edit: func(value *PolicySnapshot) { value.MaximumDuration = 2 * time.Hour }},
		{name: "mutable timestamp", edit: func(value *PolicySnapshot) { value.CreatedAt = value.CreatedAt.In(time.FixedZone("other", 3600)) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validPolicy()
			test.edit(&value)
			if err := value.Validate(); !errors.Is(err, ErrInvalidPolicy) {
				t.Fatalf("Validate() = %v", err)
			}
		})
	}
}

func TestPrivilegedPolicyRequiresApprovalAndFirecracker(t *testing.T) {
	policy := validPolicy()
	policy.TrustTier = TrustPrivileged
	policy.NetworkMode = NetworkAllowlist
	policy.NetworkPolicyHash = policy.ImageDigest
	policy.SecretMode = SecretShortLived
	policy.SecretScopeHash = policy.ImageDigest
	policy.ApprovalRequired = true
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	policy.ApprovalRequired = false
	if err := policy.Validate(); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("unapproved privileged policy = %v", err)
	}
}

func TestSemiTrustedMayUseBoundedProxyButUntrustedMayOnlyAddApproval(t *testing.T) {
	semi := validPolicy()
	semi.TrustTier = TrustSemiTrusted
	semi.NetworkMode = NetworkAllowlist
	semi.NetworkPolicyHash = semi.ImageDigest
	if err := semi.Validate(); err != nil {
		t.Fatalf("semi-trusted proxy policy: %v", err)
	}
	untrusted := validPolicy()
	untrusted.ApprovalRequired = true
	if err := untrusted.Validate(); err != nil {
		t.Fatalf("additional untrusted approval: %v", err)
	}
	semi.NetworkPolicyHash = emptyPolicyHash()
	if err := semi.Validate(); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("empty allowlist policy hash: %v", err)
	}
}
