package postgres

import (
	"context"
	"crypto/rand"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/platform/ids"
	"github.com/langshift/lites/internal/security/opaque"
)

type DeliveredCommand struct {
	TenantID, StoreEpoch, CommandID, CommandType, AggregateKind, AggregateID, PayloadRef, PayloadHash string
	QueueGeneration, DispatchVersion                                                                  uint64
}

type InboxClaim struct {
	InboxID, ConsumerName, AttemptID string
	Command                          DeliveredCommand
	Fence                            uint64
	LeaseToken                       string
	LeaseExpiresAt                   time.Time
	Completed, Busy                  bool
}

type InboxStore struct {
	Pool     *pgxpool.Pool
	Epochs   EpochAuthority
	Tokens   opaque.Manager
	LeaseTTL time.Duration
	Random   io.Reader
	Now      func() time.Time
}

func (store InboxStore) Claim(ctx context.Context, consumerName string, command DeliveredCommand) (InboxClaim, error) {
	if err := store.validate(); err != nil || consumerName == "" || !validDeliveredCommand(command) {
		return InboxClaim{}, ErrDeliveryConfiguration
	}
	if err := requireCurrentEpoch(ctx, store.Epochs, command.StoreEpoch); err != nil {
		return InboxClaim{}, err
	}
	inboxID, err := ids.NewUUIDFrom(store.random())
	if err != nil {
		return InboxClaim{}, err
	}
	attemptID, err := ids.NewUUIDFrom(store.random())
	if err != nil {
		return InboxClaim{}, err
	}
	manager := store.Tokens
	manager.Random = store.random()
	credential, err := manager.Issue()
	if err != nil {
		return InboxClaim{}, err
	}
	now := store.now()
	expiresAt := now.Add(store.LeaseTTL)
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return InboxClaim{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return InboxClaim{}, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO agent.inbox (id,tenant_id,store_epoch,consumer_name,command_id,status,owner_attempt_id,fence,lease_token_hash,lease_expires_at,request_hash) VALUES ($1,$2,$3,$4,$5,'running',$6,1,$7,$8,$9) ON CONFLICT (tenant_id,consumer_name,command_id) DO NOTHING`, inboxID, command.TenantID, command.StoreEpoch, consumerName, command.CommandID, attemptID, credential.Digest[:], expiresAt, command.PayloadHash)
	if err != nil {
		return InboxClaim{}, err
	}
	if tag.RowsAffected() == 1 {
		if err = tx.Commit(ctx); err != nil {
			return InboxClaim{}, err
		}
		return InboxClaim{InboxID: inboxID, ConsumerName: consumerName, AttemptID: attemptID, Command: command, Fence: 1, LeaseToken: credential.Raw, LeaseExpiresAt: expiresAt}, nil
	}
	var actual InboxClaim
	actual.ConsumerName = consumerName
	actual.Command = command
	var status, actualEpoch, actualHash string
	err = tx.QueryRow(ctx, `SELECT id::text,store_epoch::text,status,owner_attempt_id::text,fence,lease_expires_at,request_hash FROM agent.inbox WHERE tenant_id=$1 AND consumer_name=$2 AND command_id=$3 FOR UPDATE`, command.TenantID, consumerName, command.CommandID).Scan(&actual.InboxID, &actualEpoch, &status, &actual.AttemptID, &actual.Fence, &actual.LeaseExpiresAt, &actualHash)
	if err != nil {
		return InboxClaim{}, err
	}
	if actualEpoch != command.StoreEpoch || actualHash != command.PayloadHash {
		return InboxClaim{}, ErrDeliveryConflict
	}
	if status == "completed" {
		actual.Completed = true
		if err = tx.Commit(ctx); err != nil {
			return InboxClaim{}, err
		}
		return actual, nil
	}
	if status == "running" && actual.LeaseExpiresAt.After(now) {
		actual.Busy = true
		if err = tx.Commit(ctx); err != nil {
			return InboxClaim{}, err
		}
		return actual, ErrDeliveryBusy
	}
	tag, err = tx.Exec(ctx, `UPDATE agent.inbox SET status='running',owner_attempt_id=$1,fence=fence+1,lease_token_hash=$2,lease_expires_at=$3,updated_at=$4 WHERE id=$5 AND tenant_id=$6 AND status IN ('running','abandoned') AND (status='abandoned' OR lease_expires_at<=$4)`, attemptID, credential.Digest[:], expiresAt, now, actual.InboxID, command.TenantID)
	if err != nil || tag.RowsAffected() != 1 {
		return InboxClaim{}, ErrDeliveryConflict
	}
	actual.AttemptID = attemptID
	actual.Fence++
	actual.LeaseToken = credential.Raw
	actual.LeaseExpiresAt = expiresAt
	if err = tx.Commit(ctx); err != nil {
		return InboxClaim{}, err
	}
	return actual, nil
}

func (store InboxStore) Heartbeat(ctx context.Context, claim InboxClaim) (InboxClaim, error) {
	if err := store.validateClaim(ctx, claim); err != nil {
		return InboxClaim{}, err
	}
	digest, err := store.Tokens.Digest(claim.LeaseToken)
	if err != nil {
		return InboxClaim{}, ErrDeliveryConflict
	}
	now := store.now()
	expiresAt := now.Add(store.LeaseTTL)
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return InboxClaim{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claim.Command.TenantID); err != nil {
		return InboxClaim{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.inbox SET lease_expires_at=$1,updated_at=$2 WHERE id=$3 AND tenant_id=$4 AND store_epoch=$5 AND consumer_name=$6 AND command_id=$7 AND status='running' AND owner_attempt_id=$8 AND fence=$9 AND lease_token_hash=$10 AND lease_expires_at>$2`, expiresAt, now, claim.InboxID, claim.Command.TenantID, claim.Command.StoreEpoch, claim.ConsumerName, claim.Command.CommandID, claim.AttemptID, claim.Fence, digest[:])
	if err != nil || tag.RowsAffected() != 1 {
		return InboxClaim{}, ErrDeliveryConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return InboxClaim{}, err
	}
	claim.LeaseExpiresAt = expiresAt
	return claim, nil
}

func (store InboxStore) Complete(ctx context.Context, claim InboxClaim) error {
	if err := store.validateClaim(ctx, claim); err != nil {
		return err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claim.Command.TenantID); err != nil {
		return err
	}
	if err = store.CompleteTx(ctx, tx, claim, store.now()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CompleteTx lets a business handler commit its terminal event/projection and
// inbox completion in one PostgreSQL transaction.
func (store InboxStore) CompleteTx(ctx context.Context, tx pgx.Tx, claim InboxClaim, now time.Time) error {
	if tx == nil || claim.Completed || claim.Busy {
		return ErrDeliveryConfiguration
	}
	if err := store.LockClaimTx(ctx, tx, claim, now); err != nil {
		return err
	}
	digest, err := store.Tokens.Digest(claim.LeaseToken)
	if err != nil {
		return ErrDeliveryConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.inbox SET status='completed',completed_at=$1,updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND store_epoch=$4 AND consumer_name=$5 AND command_id=$6 AND request_hash=$7 AND status='running' AND owner_attempt_id=$8 AND fence=$9 AND lease_token_hash=$10 AND lease_expires_at>$1`, now, claim.InboxID, claim.Command.TenantID, claim.Command.StoreEpoch, claim.ConsumerName, claim.Command.CommandID, claim.Command.PayloadHash, claim.AttemptID, claim.Fence, digest[:])
	if err != nil || tag.RowsAffected() != 1 {
		return ErrDeliveryConflict
	}
	return nil
}

// LockClaimTx enforces the global write lock order: inbox/effect dedupe rows
// are locked before business aggregates and event cursors.
func (store InboxStore) LockClaimTx(ctx context.Context, tx pgx.Tx, claim InboxClaim, now time.Time) error {
	if tx == nil || claim.Completed || claim.Busy {
		return ErrDeliveryConfiguration
	}
	if err := store.validateClaim(ctx, claim); err != nil {
		return err
	}
	digest, err := store.Tokens.Digest(claim.LeaseToken)
	if err != nil {
		return ErrDeliveryConflict
	}
	var id string
	err = tx.QueryRow(ctx, `SELECT id::text FROM agent.inbox WHERE id=$1 AND tenant_id=$2 AND store_epoch=$3 AND consumer_name=$4 AND command_id=$5 AND request_hash=$6 AND status='running' AND owner_attempt_id=$7 AND fence=$8 AND lease_token_hash=$9 AND lease_expires_at>$10 FOR UPDATE`, claim.InboxID, claim.Command.TenantID, claim.Command.StoreEpoch, claim.ConsumerName, claim.Command.CommandID, claim.Command.PayloadHash, claim.AttemptID, claim.Fence, digest[:], now).Scan(&id)
	if err != nil || id != claim.InboxID {
		return ErrDeliveryConflict
	}
	return nil
}

func (store InboxStore) Abandon(ctx context.Context, claim InboxClaim) error {
	if err := store.validateClaim(ctx, claim); err != nil {
		return err
	}
	digest, err := store.Tokens.Digest(claim.LeaseToken)
	if err != nil {
		return ErrDeliveryConflict
	}
	now := store.now()
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claim.Command.TenantID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.inbox SET status='abandoned',updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND store_epoch=$4 AND consumer_name=$5 AND command_id=$6 AND status='running' AND owner_attempt_id=$7 AND fence=$8 AND lease_token_hash=$9`, now, claim.InboxID, claim.Command.TenantID, claim.Command.StoreEpoch, claim.ConsumerName, claim.Command.CommandID, claim.AttemptID, claim.Fence, digest[:])
	if err != nil || tag.RowsAffected() != 1 {
		return ErrDeliveryConflict
	}
	return tx.Commit(ctx)
}

func (store InboxStore) validate() error {
	if store.Pool == nil || store.Epochs == nil || store.LeaseTTL <= 0 || store.Tokens.Purpose == "" || len(store.Tokens.Pepper) < 32 {
		return ErrDeliveryConfiguration
	}
	return nil
}

func (store InboxStore) validateClaim(ctx context.Context, claim InboxClaim) error {
	if err := store.validate(); err != nil || claim.InboxID == "" || claim.ConsumerName == "" || claim.AttemptID == "" || claim.Fence == 0 || claim.LeaseToken == "" || !validDeliveredCommand(claim.Command) {
		return ErrDeliveryConfiguration
	}
	return requireCurrentEpoch(ctx, store.Epochs, claim.Command.StoreEpoch)
}

func validDeliveredCommand(command DeliveredCommand) bool {
	return command.TenantID != "" && command.StoreEpoch != "" && command.CommandID != "" && command.CommandType != "" && command.AggregateKind != "" && command.AggregateID != "" && command.PayloadRef != "" && command.PayloadHash != ""
}

func (store InboxStore) now() time.Time {
	if store.Now != nil {
		return store.Now().UTC()
	}
	return time.Now().UTC()
}

func (store InboxStore) random() io.Reader {
	if store.Random != nil {
		return store.Random
	}
	return rand.Reader
}
