package sessionrequest

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	runtimecontract "github.com/langshift/lites/internal/runtime"
)

var ErrSigning = errors.New("runtime capability issuer configuration is invalid")

type Issuer struct {
	Store                   Store
	Issuer, Audience, KeyID string
	PrivateKey              ed25519.PrivateKey
	TTL                     time.Duration
	Now                     func() time.Time
}

type Issued struct {
	Requested
	CapabilityToken string
}

func (issuer Issuer) Request(ctx context.Context, command Command) (Issued, error) {
	if !issuer.valid() {
		return Issued{}, ErrSigning
	}
	requested, err := issuer.Store.Request(ctx, command)
	if err != nil {
		return Issued{}, err
	}
	return issuer.issue(requested)
}

func (issuer Issuer) issue(requested Requested) (Issued, error) {
	expiresAt := minimumTime(requested.RequestedAt.Add(issuer.TTL), requested.ExecutionLeaseExpiresAt, requested.ExecutionDeadline).Unix()
	if !issuer.valid() || expiresAt <= issuer.now().Unix() {
		return Issued{}, ErrSigning
	}
	claims := runtimecontract.CapabilityClaims{Purpose: "runtime_session", Issuer: issuer.Issuer, Audience: issuer.Audience,
		TenantID: requested.TenantID, UserID: requested.UserID, RunID: requested.RunID, ToolCallID: requested.ToolCallID,
		CommandID: requested.CommandID, AttemptID: requested.AttemptID, Fence: requested.Fence,
		PolicySnapshotID: requested.PolicySnapshotKey, PolicyHash: requested.PolicyHash,
		WorkspaceID: requested.WorkspaceID, BaseWorkspaceRevision: requested.BaseWorkspaceRevision, WorkspaceMode: requested.WorkspaceMode,
		NetworkPolicyHash: requested.NetworkPolicyHash, SecretScopeHash: requested.SecretScopeHash,
		LeaseTokenHash: requested.LeaseTokenHash, RequestHash: requested.RequestHash,
		ApprovalID: requested.ApprovalID, ApprovalVersion: requested.ApprovalVersion,
		IssuedAt: requested.RequestedAt.Unix(), ExpiresAt: expiresAt, Nonce: requested.Nonce}
	token, err := runtimecontract.SignCapability(claims, issuer.KeyID, issuer.PrivateKey, 5*time.Minute)
	if err != nil {
		return Issued{}, errors.Join(ErrSigning, err)
	}
	return Issued{Requested: requested, CapabilityToken: token}, nil
}

func (issuer Issuer) valid() bool {
	return issuer.Issuer != "" && issuer.Audience != "" && issuer.KeyID != "" && len(issuer.PrivateKey) == ed25519.PrivateKeySize &&
		issuer.TTL >= time.Second && issuer.TTL <= 5*time.Minute
}

func (issuer Issuer) now() time.Time {
	if issuer.Now != nil {
		return issuer.Now().UTC()
	}
	return time.Now().UTC()
}
