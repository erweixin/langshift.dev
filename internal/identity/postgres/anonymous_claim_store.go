package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/identity/anonymousclaim"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

type AnonymousClaimStore struct {
	Pool           *pgxpool.Pool
	SystemTenantID string
	IdentityKey    []byte
	Payloads       payload.Store
	Appender       eventpostgres.Appender
	StoreEpoch     string
	Now            func() time.Time
}

func (store AnonymousClaimStore) Load(ctx context.Context, claimID string) (anonymousclaim.Saga, error) {
	if store.Pool == nil || store.SystemTenantID == "" || claimID == "" {
		return anonymousclaim.Saga{}, anonymousclaim.ErrInvariant
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadOnly})
	if err != nil {
		return anonymousclaim.Saga{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, store.SystemTenantID); err != nil {
		return anonymousclaim.Saga{}, err
	}
	var saga anonymousclaim.Saga
	var reservedAt *time.Time
	err = tx.QueryRow(ctx, `SELECT c.id::text,c.anonymous_subject_id::text,c.user_id::text,c.onboarding_session_id::text,c.source_route_revision_id::text,COALESCE(r.claim_set_hash,''),c.status,c.version,c.claim_key,COALESCE(c.target_tenant_id::text,''),COALESCE(c.target_user_id::text,''),COALESCE(c.target_mission_id::text,''),COALESCE(c.destination_commit_event_id::text,''),c.reserved_at,c.expires_at FROM identity.onboarding_claims c LEFT JOIN product.route_revisions r ON r.id=c.source_route_revision_id AND r.tenant_id=c.tenant_id WHERE c.id=$1 AND c.tenant_id=$2`, claimID, store.SystemTenantID).Scan(&saga.ID, &saga.AnonymousSubjectID, &saga.EphemeralUserID, &saga.OnboardingSessionID, &saga.SourceRouteRevisionID, &saga.ClaimSetHash, &saga.Status, &saga.Version, &saga.ClaimKey, &saga.TargetTenantID, &saga.TargetUserID, &saga.MissionID, &saga.DestinationCommitEventID, &reservedAt, &saga.ExpiresAt)
	if err != nil {
		return anonymousclaim.Saga{}, err
	}
	if reservedAt != nil {
		saga.ReservedAt = reservedAt.UTC()
	}
	rows, err := tx.Query(ctx, `SELECT id::text,surface,receipt_hash,erased_at,details FROM identity.anonymous_erasure_receipts WHERE claim_id=$1 AND tenant_id=$2 ORDER BY surface`, claimID, store.SystemTenantID)
	if err != nil {
		return anonymousclaim.Saga{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var receipt anonymousclaim.DeletionReceipt
		if err = rows.Scan(&receipt.ID, &receipt.Surface, &receipt.Hash, &receipt.ErasedAt, &receipt.Details); err != nil {
			return anonymousclaim.Saga{}, err
		}
		saga.DeletionReceipts = append(saga.DeletionReceipts, receipt)
	}
	if err = rows.Err(); err != nil {
		return anonymousclaim.Saga{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return anonymousclaim.Saga{}, err
	}
	return saga, nil
}

func (store AnonymousClaimStore) CompareAndSwap(ctx context.Context, previous, next anonymousclaim.Saga) error {
	if store.Pool == nil || store.SystemTenantID == "" || previous.ID == "" || previous.AnonymousSubjectID == "" || next.ID != previous.ID || next.Version != previous.Version+1 {
		return anonymousclaim.ErrInvariant
	}
	if err := anonymousclaim.ValidateSuccessor(previous, next); err != nil {
		return err
	}
	now := time.Now().UTC()
	if store.Now != nil {
		now = store.Now().UTC()
	}
	var reservationAppend *eventpostgres.Input
	if previous.Status == anonymousclaim.Available && next.Status == anonymousclaim.Reserved {
		prepared, prepareErr := store.prepareReservationAppend(ctx, previous, next, now)
		if prepareErr != nil {
			return prepareErr
		}
		reservationAppend = &prepared
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, store.SystemTenantID); err != nil {
		return err
	}
	if previous.Status == anonymousclaim.Available && next.Status == anonymousclaim.Reserved {
		tag, reserveErr := tx.Exec(ctx, `UPDATE identity.anonymous_subjects SET reserved_at=COALESCE(reserved_at,$1),version=version+1,updated_at=$2 WHERE id=$3 AND system_tenant_id=$4 AND deleted_at IS NULL AND expires_at>$1`, next.ReservedAt, now, previous.AnonymousSubjectID, store.SystemTenantID)
		if reserveErr != nil {
			return reserveErr
		}
		if tag.RowsAffected() != 1 {
			return anonymousclaim.ErrVersionConflict
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE identity.onboarding_claims SET version=$1,status=$2,claim_key=$3,target_tenant_id=NULLIF($4,'')::uuid,target_user_id=NULLIF($5,'')::uuid,target_mission_id=NULLIF($6,'')::uuid,destination_commit_event_id=NULLIF($7,'')::uuid,reserved_at=CASE WHEN $2='reserved' THEN COALESCE(reserved_at,$14) ELSE reserved_at END,destination_committed_at=CASE WHEN $2='destination_committed' THEN COALESCE(destination_committed_at,$8) ELSE destination_committed_at END,erasing_at=CASE WHEN $2='erasing' THEN COALESCE(erasing_at,$8) ELSE erasing_at END,claimed_at=CASE WHEN $2='claimed' THEN COALESCE(claimed_at,$8) ELSE claimed_at END,updated_at=$8 WHERE id=$9 AND tenant_id=$10 AND version=$11 AND status=$12 AND claim_key=$13 AND ($2<>'expired' OR expires_at<=$8)`, next.Version, next.Status, next.ClaimKey, next.TargetTenantID, next.TargetUserID, next.MissionID, next.DestinationCommitEventID, now, previous.ID, store.SystemTenantID, previous.Version, previous.Status, previous.ClaimKey, next.ReservedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return anonymousclaim.ErrVersionConflict
	}
	if reservationAppend != nil {
		if _, err = store.Appender.Append(ctx, tx, *reservationAppend); err != nil {
			return err
		}
	}
	if receipt, found := newReceipt(previous, next); found {
		if err = insertOrVerifyReceipt(ctx, tx, store.SystemTenantID, previous.ID, receipt); err != nil {
			return err
		}
	}
	if next.Status == anonymousclaim.Claimed {
		if _, err = tx.Exec(ctx, `UPDATE identity.anonymous_subjects SET deleted_at=COALESCE(deleted_at,$1),version=version+1,updated_at=$1 WHERE id=$2 AND system_tenant_id=$3`, now, previous.AnonymousSubjectID, store.SystemTenantID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (store AnonymousClaimStore) prepareReservationAppend(ctx context.Context, previous, next anonymousclaim.Saga, now time.Time) (eventpostgres.Input, error) {
	if len(store.IdentityKey) < 32 || store.Payloads == nil || store.StoreEpoch == "" || next.EphemeralUserID == "" || next.ClaimSetHash == "" {
		return eventpostgres.Input{}, anonymousclaim.ErrInvariant
	}
	derive := func(purpose string) (string, error) {
		return ids.DeterministicUUID(store.IdentityKey, "anonymous-claim-reserved:"+purpose, next.ClaimKey)
	}
	eventID, err := derive("event")
	if err != nil {
		return eventpostgres.Input{}, err
	}
	publishOutboxID, err := derive("publish-outbox")
	if err != nil {
		return eventpostgres.Input{}, err
	}
	publishCommandID, err := derive("publish-command")
	if err != nil {
		return eventpostgres.Input{}, err
	}
	workOutboxID, err := derive("work-outbox")
	if err != nil {
		return eventpostgres.Input{}, err
	}
	workCommandID, err := derive("work-command")
	if err != nil {
		return eventpostgres.Input{}, err
	}
	eventPayload, err := store.putJSON(ctx, payload.Descriptor{TenantID: store.SystemTenantID, ObjectID: eventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{
		"subject_id": next.ID, "subject_version": next.Version, "request_id": workCommandID, "claim_set_hash": next.ClaimSetHash, "claim_id": next.ID,
		"claim_key": next.ClaimKey, "anonymous_subject_id": next.AnonymousSubjectID, "expected_claim_version": previous.Version, "reserved_at": next.ReservedAt,
	})
	if err != nil {
		return eventpostgres.Input{}, err
	}
	workPayload, err := store.putJSON(ctx, payload.Descriptor{TenantID: store.SystemTenantID, ObjectID: workCommandID, Class: "anonymous-claim-command", ContentType: "application/json"}, map[string]any{"claim_id": next.ID})
	if err != nil {
		return eventpostgres.Input{}, err
	}
	actor, _ := json.Marshal(map[string]any{"kind": "user", "id": next.TargetUserID})
	return eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: store.SystemTenantID, UserID: next.EphemeralUserID, EventType: "AnonymousClaimReserved", SchemaVersion: 1, AggregateKind: "anonymous_claim", AggregateID: next.ID, AggregateVersion: next.Version, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: next.ID, PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{
		{ID: publishOutboxID, CommandID: publishCommandID, CommandType: "events.publish", PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash},
		{ID: workOutboxID, CommandID: workCommandID, CommandType: AnonymousClaimReconcileCommand, PayloadRef: workPayload.Ref, PayloadHash: workPayload.Hash},
	}}, nil
}

func (store AnonymousClaimStore) putJSON(ctx context.Context, descriptor payload.Descriptor, value any) (payload.Manifest, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return payload.Manifest{}, err
	}
	return store.Payloads.Put(ctx, descriptor, encoded)
}

func insertOrVerifyReceipt(ctx context.Context, tx pgx.Tx, tenantID, claimID string, receipt anonymousclaim.DeletionReceipt) error {
	tag, err := tx.Exec(ctx, `INSERT INTO identity.anonymous_erasure_receipts(id,tenant_id,claim_id,surface,receipt_hash,erased_at,details) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (claim_id,surface) DO NOTHING`, receipt.ID, tenantID, claimID, receipt.Surface, receipt.Hash, receipt.ErasedAt.UTC(), receipt.Details)
	if err != nil || tag.RowsAffected() == 1 {
		return err
	}
	var actual anonymousclaim.DeletionReceipt
	if err = tx.QueryRow(ctx, `SELECT id::text,surface,receipt_hash,erased_at,details FROM identity.anonymous_erasure_receipts WHERE tenant_id=$1 AND claim_id=$2 AND surface=$3`, tenantID, claimID, receipt.Surface).Scan(&actual.ID, &actual.Surface, &actual.Hash, &actual.ErasedAt, &actual.Details); err != nil {
		return err
	}
	if actual.ID != receipt.ID || actual.Surface != receipt.Surface || actual.Hash != receipt.Hash || !actual.ErasedAt.Equal(receipt.ErasedAt) || !equalJSON(actual.Details, receipt.Details) {
		return anonymousclaim.ErrInvariant
	}
	return nil
}

func equalJSON(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	return json.Unmarshal(left, &leftValue) == nil && json.Unmarshal(right, &rightValue) == nil && bytes.Equal(mustCanonicalJSON(leftValue), mustCanonicalJSON(rightValue))
}

func mustCanonicalJSON(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

func newReceipt(previous, next anonymousclaim.Saga) (anonymousclaim.DeletionReceipt, bool) {
	for _, receipt := range next.DeletionReceipts {
		if !anonymousclaim.HasDeletionReceipt(previous, receipt.Surface) {
			return receipt, true
		}
	}
	return anonymousclaim.DeletionReceipt{}, false
}

var _ anonymousclaim.Store = AnonymousClaimStore{}
