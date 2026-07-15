package postgres

import (
	"context"
	"crypto/rand"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/security/opaque"
)

const maximumOutboxBatch = 500
const maximumTenantPage = 5000

type PublishedCommand struct {
	OutboxID, TenantID, CommandID, CommandType, AggregateKind, AggregateID string
	StoreEpoch, PayloadRef, PayloadHash                                    string
	PublishAttempts                                                        int
	// QueueGeneration and DispatchVersion are zero while an outbox command
	// is being durably staged. The scheduler binds both values before the
	// command is admitted to an execution worker.
	QueueGeneration, DispatchVersion uint64
}

type OutboxClaim struct {
	PublishedCommand
	LeaseToken     string
	LeaseExpiresAt time.Time
}

func (command PublishedCommand) Delivered() DeliveredCommand {
	return DeliveredCommand{TenantID: command.TenantID, StoreEpoch: command.StoreEpoch, CommandID: command.CommandID, CommandType: command.CommandType, AggregateKind: command.AggregateKind, AggregateID: command.AggregateID, PayloadRef: command.PayloadRef, PayloadHash: command.PayloadHash, QueueGeneration: command.QueueGeneration, DispatchVersion: command.DispatchVersion}
}

type CommandBroker interface {
	Publish(context.Context, PublishedCommand) error
}

type OutboxStore struct {
	Pool       *pgxpool.Pool
	Epochs     EpochAuthority
	Tokens     opaque.Manager
	LeaseTTL   time.Duration
	RetryBase  time.Duration
	RetryLimit time.Duration
	Random     io.Reader
	Now        func() time.Time
}

type PublishBatchResult struct {
	Claimed, Published, Deferred int
}

// ListReadyTenantIDs calls a minimal SECURITY DEFINER capability that reveals
// only tenant IDs with publishable work in the current epoch. Payload rows are
// still read and mutated under transaction-local tenant RLS in ClaimBatch.
func (store OutboxStore) ListReadyTenantIDs(ctx context.Context, storeEpoch, after string, limit, shardIndex, shardCount int) ([]string, error) {
	if store.Pool == nil || storeEpoch == "" || limit < 1 || limit > maximumTenantPage || shardCount < 1 || shardIndex < 0 || shardIndex >= shardCount {
		return nil, ErrDeliveryConfiguration
	}
	rows, err := store.Pool.Query(ctx, `SELECT tenant_id FROM agent.list_ready_outbox_tenants($1::uuid,NULLIF($2,'')::uuid,$3,$4,$5)`, storeEpoch, after, limit, shardIndex, shardCount)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (store OutboxStore) ClaimBatch(ctx context.Context, tenantID, storeEpoch string, limit int) ([]OutboxClaim, error) {
	if err := store.validate(); err != nil || tenantID == "" || limit < 1 || limit > maximumOutboxBatch {
		return nil, ErrDeliveryConfiguration
	}
	if err := requireCurrentEpoch(ctx, store.Epochs, storeEpoch); err != nil {
		return nil, err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return nil, err
	}
	now := store.now()
	rows, err := tx.Query(ctx, `SELECT id::text,tenant_id::text,command_id::text,command_type,aggregate_kind,aggregate_id::text,store_epoch::text,payload_ref,payload_hash,publish_attempts FROM agent.outbox WHERE tenant_id=$1 AND store_epoch=$2 AND ((status='pending' AND available_at<=$3) OR (status='publishing' AND publisher_lease_expires_at<=$3)) ORDER BY available_at,id LIMIT $4 FOR UPDATE SKIP LOCKED`, tenantID, storeEpoch, now, limit)
	if err != nil {
		return nil, err
	}
	commands := make([]PublishedCommand, 0, limit)
	for rows.Next() {
		var command PublishedCommand
		if err = rows.Scan(&command.OutboxID, &command.TenantID, &command.CommandID, &command.CommandType, &command.AggregateKind, &command.AggregateID, &command.StoreEpoch, &command.PayloadRef, &command.PayloadHash, &command.PublishAttempts); err != nil {
			rows.Close()
			return nil, err
		}
		commands = append(commands, command)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	claims := make([]OutboxClaim, 0, len(commands))
	for _, command := range commands {
		manager := store.Tokens
		manager.Random = store.random()
		credential, issueErr := manager.Issue()
		if issueErr != nil {
			return nil, issueErr
		}
		expiresAt := now.Add(store.LeaseTTL)
		tag, updateErr := tx.Exec(ctx, `UPDATE agent.outbox SET status='publishing',publisher_lease_hash=$1,publisher_lease_expires_at=$2,publish_attempts=publish_attempts+1 WHERE id=$3 AND tenant_id=$4 AND store_epoch=$5 AND ((status='pending' AND available_at<=$6) OR (status='publishing' AND publisher_lease_expires_at<=$6))`, credential.Digest[:], expiresAt, command.OutboxID, tenantID, storeEpoch, now)
		if updateErr != nil || tag.RowsAffected() != 1 {
			return nil, ErrDeliveryConflict
		}
		command.PublishAttempts++
		claims = append(claims, OutboxClaim{PublishedCommand: command, LeaseToken: credential.Raw, LeaseExpiresAt: expiresAt})
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return claims, nil
}

func (store OutboxStore) MarkPublished(ctx context.Context, claim OutboxClaim) error {
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
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claim.TenantID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.outbox SET status='published',publisher_lease_hash=NULL,publisher_lease_expires_at=NULL,published_at=$1 WHERE id=$2 AND tenant_id=$3 AND command_id=$4 AND store_epoch=$5 AND status='publishing' AND publisher_lease_hash=$6 AND publisher_lease_expires_at>$1`, now, claim.OutboxID, claim.TenantID, claim.CommandID, claim.StoreEpoch, digest[:])
	if err != nil || tag.RowsAffected() != 1 {
		return ErrDeliveryConflict
	}
	return tx.Commit(ctx)
}

func (store OutboxStore) Defer(ctx context.Context, claim OutboxClaim) error {
	if err := store.validateClaim(ctx, claim); err != nil {
		return err
	}
	digest, err := store.Tokens.Digest(claim.LeaseToken)
	if err != nil {
		return ErrDeliveryConflict
	}
	now := store.now()
	availableAt := now.Add(store.backoff(claim.PublishAttempts))
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claim.TenantID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.outbox SET status='pending',publisher_lease_hash=NULL,publisher_lease_expires_at=NULL,available_at=$1 WHERE id=$2 AND tenant_id=$3 AND command_id=$4 AND store_epoch=$5 AND status='publishing' AND publisher_lease_hash=$6`, availableAt, claim.OutboxID, claim.TenantID, claim.CommandID, claim.StoreEpoch, digest[:])
	if err != nil || tag.RowsAffected() != 1 {
		return ErrDeliveryConflict
	}
	return tx.Commit(ctx)
}

func (store OutboxStore) PublishBatch(ctx context.Context, broker CommandBroker, tenantID, storeEpoch string, limit int) (PublishBatchResult, error) {
	if broker == nil {
		return PublishBatchResult{}, ErrDeliveryConfiguration
	}
	claims, err := store.ClaimBatch(ctx, tenantID, storeEpoch, limit)
	if err != nil {
		return PublishBatchResult{}, err
	}
	result := PublishBatchResult{Claimed: len(claims)}
	for _, claim := range claims {
		if err = broker.Publish(ctx, claim.PublishedCommand); err != nil {
			if deferErr := store.Defer(ctx, claim); deferErr != nil {
				return result, deferErr
			}
			result.Deferred++
			continue
		}
		if err = store.MarkPublished(ctx, claim); err != nil {
			return result, err
		}
		result.Published++
	}
	return result, nil
}

func (store OutboxStore) validate() error {
	if store.Pool == nil || store.Epochs == nil || store.LeaseTTL <= 0 || store.RetryBase <= 0 || store.RetryLimit < store.RetryBase || store.Tokens.Purpose == "" || len(store.Tokens.Pepper) < 32 {
		return ErrDeliveryConfiguration
	}
	return nil
}

func (store OutboxStore) validateClaim(ctx context.Context, claim OutboxClaim) error {
	if err := store.validate(); err != nil || claim.OutboxID == "" || claim.TenantID == "" || claim.CommandID == "" || claim.StoreEpoch == "" || claim.LeaseToken == "" {
		return ErrDeliveryConfiguration
	}
	return requireCurrentEpoch(ctx, store.Epochs, claim.StoreEpoch)
}

func (store OutboxStore) backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := store.RetryBase
	for index := 1; index < attempt && delay < store.RetryLimit; index++ {
		if delay > store.RetryLimit/2 {
			return store.RetryLimit
		}
		delay *= 2
	}
	if delay > store.RetryLimit {
		return store.RetryLimit
	}
	return delay
}

func (store OutboxStore) now() time.Time {
	if store.Now != nil {
		return store.Now().UTC()
	}
	return time.Now().UTC()
}

func (store OutboxStore) random() io.Reader {
	if store.Random != nil {
		return store.Random
	}
	return rand.Reader
}
