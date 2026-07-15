// Package runtime defines immutable policy and capability contracts shared by
// the Runtime control plane and the Firecracker host manager.
package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

var ErrInvalidPolicy = errors.New("runtime policy snapshot is invalid")

type TrustTier string
type IsolationKind string
type NetworkMode string
type SecretMode string
type WorkspaceMode string

const (
	TrustTrusted     TrustTier = "trusted"
	TrustSemiTrusted TrustTier = "semi_trusted"
	TrustUntrusted   TrustTier = "untrusted"
	TrustPrivileged  TrustTier = "privileged"

	IsolationContainer   IsolationKind = "restricted_container"
	IsolationFirecracker IsolationKind = "firecracker"

	NetworkNone      NetworkMode = "none"
	NetworkBroker    NetworkMode = "broker_only"
	NetworkAllowlist NetworkMode = "allowlist_proxy"

	SecretNone       SecretMode = "none"
	SecretBroker     SecretMode = "broker_only"
	SecretShortLived SecretMode = "short_lived_injected"

	WorkspaceNone      WorkspaceMode = "none"
	WorkspaceReadOnly  WorkspaceMode = "read_only"
	WorkspaceReadWrite WorkspaceMode = "read_write"
)

var snapshotIDPattern = regexp.MustCompile(`^[a-z][a-z0-9._:-]{2,127}$`)

type PolicySnapshot struct {
	SchemaVersion     string        `json:"schema_version"`
	SnapshotID        string        `json:"snapshot_id"`
	TenantID          string        `json:"tenant_id"`
	Version           uint64        `json:"version"`
	TrustTier         TrustTier     `json:"trust_tier"`
	Isolation         IsolationKind `json:"isolation"`
	NetworkMode       NetworkMode   `json:"network_mode"`
	NetworkPolicyHash string        `json:"network_policy_hash"`
	SecretMode        SecretMode    `json:"secret_mode"`
	SecretScopeHash   string        `json:"secret_scope_hash"`
	WorkspaceMode     WorkspaceMode `json:"workspace_mode"`
	ImageDigest       string        `json:"image_digest"`
	KernelDigest      string        `json:"kernel_digest,omitempty"`
	RootFSDigest      string        `json:"rootfs_digest,omitempty"`
	VCPUCount         int           `json:"vcpu_count"`
	MemoryMiB         int           `json:"memory_mib"`
	DiskMiB           int           `json:"disk_mib"`
	PidsMax           int           `json:"pids_max"`
	MaximumDuration   time.Duration `json:"maximum_duration"`
	IdleTimeout       time.Duration `json:"idle_timeout"`
	KillGrace         time.Duration `json:"kill_grace"`
	ApprovalRequired  bool          `json:"approval_required"`
	CreatedAt         time.Time     `json:"created_at"`
}

func (policy PolicySnapshot) Validate() error {
	if policy.SchemaVersion != "1" || !snapshotIDPattern.MatchString(policy.SnapshotID) || policy.TenantID == "" || policy.Version == 0 || !validDigest(policy.ImageDigest) || !validDigest(policy.NetworkPolicyHash) || !validDigest(policy.SecretScopeHash) || policy.VCPUCount < 1 || policy.VCPUCount > 32 || policy.MemoryMiB < 128 || policy.MemoryMiB > 32768 || policy.MemoryMiB%2 != 0 || policy.DiskMiB < 64 || policy.DiskMiB > 262144 || policy.PidsMax < 1 || policy.PidsMax > 4096 || policy.MaximumDuration < time.Second || policy.MaximumDuration > time.Hour || policy.IdleTimeout < time.Second || policy.IdleTimeout > policy.MaximumDuration || policy.KillGrace < time.Second || policy.KillGrace > 30*time.Second || policy.CreatedAt.IsZero() || policy.CreatedAt.Location() != time.UTC {
		return ErrInvalidPolicy
	}
	if policy.WorkspaceMode != WorkspaceNone && policy.WorkspaceMode != WorkspaceReadOnly && policy.WorkspaceMode != WorkspaceReadWrite {
		return ErrInvalidPolicy
	}
	switch policy.TrustTier {
	case TrustTrusted:
		if policy.Isolation != IsolationContainer || policy.SecretMode == SecretShortLived && !policy.ApprovalRequired {
			return ErrInvalidPolicy
		}
	case TrustSemiTrusted:
		if policy.Isolation != IsolationFirecracker || !validDigest(policy.KernelDigest) || !validDigest(policy.RootFSDigest) || policy.SecretMode == SecretShortLived {
			return ErrInvalidPolicy
		}
	case TrustUntrusted:
		if policy.Isolation != IsolationFirecracker || !validDigest(policy.KernelDigest) || !validDigest(policy.RootFSDigest) || policy.NetworkMode == NetworkAllowlist || policy.SecretMode == SecretShortLived {
			return ErrInvalidPolicy
		}
	case TrustPrivileged:
		if policy.Isolation != IsolationFirecracker || !validDigest(policy.KernelDigest) || !validDigest(policy.RootFSDigest) || !policy.ApprovalRequired {
			return ErrInvalidPolicy
		}
	default:
		return ErrInvalidPolicy
	}
	if policy.NetworkMode != NetworkNone && policy.NetworkMode != NetworkBroker && policy.NetworkMode != NetworkAllowlist || policy.SecretMode != SecretNone && policy.SecretMode != SecretBroker && policy.SecretMode != SecretShortLived {
		return ErrInvalidPolicy
	}
	if (policy.NetworkMode == NetworkNone) != (policy.NetworkPolicyHash == emptyPolicyHash()) || (policy.SecretMode == SecretNone) != (policy.SecretScopeHash == emptyPolicyHash()) || policy.SecretMode == SecretShortLived && policy.NetworkMode == NetworkNone {
		return ErrInvalidPolicy
	}
	return nil
}

func (policy PolicySnapshot) Hash() (string, error) {
	if err := policy.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func emptyPolicyHash() string {
	digest := sha256.Sum256(nil)
	return "sha256:" + hex.EncodeToString(digest[:])
}
