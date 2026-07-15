// Package postgres owns the durable Runtime session and host-allocation CAS
// protocol. A signed capability is necessary but never sufficient: every
// claim is checked against the current database execution right in the same
// transaction that reserves host capacity.
package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	platformids "github.com/langshift/lites/internal/platform/ids"
	runtimecontract "github.com/langshift/lites/internal/runtime"
	"github.com/langshift/lites/internal/security/opaque"
)

var (
	ErrConfiguration     = errors.New("runtime store configuration is invalid")
	ErrInvalidCommand    = errors.New("runtime store command is invalid")
	ErrCapabilityBinding = errors.New("runtime capability does not match durable execution state")
	ErrSessionConflict   = errors.New("runtime session state conflicts with command")
	ErrHostPool          = errors.New("runtime host does not belong to the required trust pool")
	ErrApproval          = errors.New("runtime approval is absent, stale, or invalid")
	ErrStaleEpoch        = errors.New("runtime store epoch is stale")
)

type EpochAuthority interface {
	CurrentStoreEpoch(context.Context) (string, error)
}

type Store struct {
	Pool        *pgxpool.Pool
	Appender    eventpostgres.Appender
	Epochs      EpochAuthority
	StoreEpoch  string
	IDKey       []byte
	TokenPepper []byte
	Verifier    runtimecontract.CapabilityVerifier
	Now         func() time.Time
}

type PayloadPointer struct{ Ref, Hash string }

type RuntimePolicy struct {
	ID, SnapshotKey, PolicyHash, TrustTier, IsolationKind string
	NetworkPolicyHash, SecretScopeHash, WorkspaceMode     string
	ImageDigest, KernelDigest, RootFSDigest               string
	VCPUCount, MemoryMiB, DiskMiB, PidsMax                int
	MaximumDuration, IdleTimeout, KillGrace               time.Duration
	ApprovalRequired                                      bool
}

type ProvisionResult struct {
	TenantID, SessionID, AllocationID, ProvisionAttemptID string
	MachineID                                             string
	GuestCID                                              uint32
	Version                                               uint64
	Policy                                                RuntimePolicy
	Replayed                                              bool
}

func (store Store) IssueProvisionLease() (string, error) {
	credential, err := store.provisionTokens().Issue()
	return credential.Raw, err
}

func (store Store) valid() bool {
	return store.Pool != nil && store.Epochs != nil && store.StoreEpoch != "" && len(store.IDKey) >= 32 && len(store.TokenPepper) >= 32
}

func (store Store) requireEpoch(ctx context.Context) error {
	epoch, err := store.Epochs.CurrentStoreEpoch(ctx)
	if err != nil || epoch == "" {
		return ErrConfiguration
	}
	if epoch != store.StoreEpoch {
		return ErrStaleEpoch
	}
	return nil
}

func (store Store) now() time.Time {
	value := time.Now().UTC()
	if store.Now != nil {
		value = store.Now().UTC()
	}
	return value.Truncate(time.Microsecond)
}

func (store Store) provisionTokens() opaque.Manager {
	return opaque.Manager{Purpose: "runtime-provision-lease-v1", Pepper: store.TokenPepper}
}

func (store Store) deterministicID(scope, sessionID string, version uint64) (string, error) {
	return platformids.DeterministicUUID(store.IDKey, scope, sessionID+":"+strconv.FormatUint(version, 10))
}

func validPayload(pointer PayloadPointer) bool { return pointer.Ref != "" && pointer.Hash != "" }

func validActor(actor json.RawMessage) bool {
	var value map[string]any
	return json.Unmarshal(actor, &value) == nil && value != nil
}

func decodeLeaseHash(value string) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return nil, ErrCapabilityBinding
	}
	return decoded, nil
}
