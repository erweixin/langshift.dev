package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

type OnboardingRouteReconciler struct {
	Pool       *pgxpool.Pool
	Appender   eventpostgres.Appender
	Payloads   payload.Store
	IDKey      []byte
	StoreEpoch string
	Now        func() time.Time
}

type OnboardingRouteReconcileResult struct{ Ready, Failed int }

type onboardingTerminalRoute struct {
	SessionID, UserID, MissionID, RevisionID, RouteStatus string
}

func (reconciler OnboardingRouteReconciler) ListTenantIDs(ctx context.Context, after string, limit int) ([]string, error) {
	if !reconciler.valid() || limit < 1 || limit > 5000 {
		return nil, ErrRoutePlannerCommand
	}
	rows, err := reconciler.Pool.Query(ctx, `SELECT tenant_id::text FROM identity.list_onboarding_route_reconciliation_tenants(NULLIF($1,'')::uuid,$2)`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]string, 0, limit)
	for rows.Next() {
		var tenantID string
		if err = rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		result = append(result, tenantID)
	}
	return result, rows.Err()
}

func (reconciler OnboardingRouteReconciler) ReconcileTenant(ctx context.Context, tenantID string, limit int) (OnboardingRouteReconcileResult, error) {
	if !reconciler.valid() || tenantID == "" || limit < 1 || limit > 500 {
		return OnboardingRouteReconcileResult{}, ErrRoutePlannerCommand
	}
	tx, err := reconciler.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return OnboardingRouteReconcileResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return OnboardingRouteReconcileResult{}, err
	}
	rows, err := tx.Query(ctx, `SELECT s.id::text,s.user_id::text,s.mission_id::text,s.route_revision_id::text,r.status FROM identity.onboarding_sessions s JOIN product.route_revisions r ON r.tenant_id=s.tenant_id AND r.id=s.route_revision_id WHERE s.tenant_id=$1 AND s.status='route_generating' AND r.status IN ('proposed','failed','stale') ORDER BY s.updated_at,s.id LIMIT $2`, tenantID, limit)
	if err != nil {
		return OnboardingRouteReconcileResult{}, err
	}
	items := []onboardingTerminalRoute{}
	for rows.Next() {
		var item onboardingTerminalRoute
		if err = rows.Scan(&item.SessionID, &item.UserID, &item.MissionID, &item.RevisionID, &item.RouteStatus); err != nil {
			rows.Close()
			return OnboardingRouteReconcileResult{}, err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return OnboardingRouteReconcileResult{}, err
	}
	rows.Close()
	if err = tx.Commit(ctx); err != nil {
		return OnboardingRouteReconcileResult{}, err
	}
	result := OnboardingRouteReconcileResult{}
	for _, item := range items {
		ready, finalizeErr := reconciler.finalize(ctx, tenantID, item)
		if finalizeErr != nil {
			return result, finalizeErr
		}
		if ready {
			result.Ready++
		} else {
			result.Failed++
		}
	}
	return result, nil
}

func (reconciler OnboardingRouteReconciler) finalize(ctx context.Context, tenantID string, item onboardingTerminalRoute) (bool, error) {
	now := reconciler.now()
	eventID, err := ids.DeterministicUUID(reconciler.IDKey, "onboarding-route-finalize:event", item.RevisionID)
	if err != nil {
		return false, err
	}
	outboxID, err := ids.DeterministicUUID(reconciler.IDKey, "onboarding-route-finalize:outbox", item.RevisionID)
	if err != nil {
		return false, err
	}
	commandID, err := ids.DeterministicUUID(reconciler.IDKey, "onboarding-route-finalize:command", item.RevisionID)
	if err != nil {
		return false, err
	}
	claimID, err := ids.DeterministicUUID(reconciler.IDKey, "onboarding-route-finalize:claim", item.RevisionID)
	if err != nil {
		return false, err
	}
	claimKey, err := ids.DeterministicUUID(reconciler.IDKey, "onboarding-route-finalize:claim-key", item.RevisionID)
	if err != nil {
		return false, err
	}
	tx, err := reconciler.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return false, err
	}
	var version uint64
	var status, routeStatus, routeRef, routeHash, claimSetHash string
	var anonymousSubjectID *string
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `SELECT s.version,s.status,s.anonymous_subject_id::text,s.expires_at,r.status,COALESCE(r.route_payload_ref,''),COALESCE(r.route_payload_hash,''),r.claim_set_hash FROM identity.onboarding_sessions s JOIN product.route_revisions r ON r.tenant_id=s.tenant_id AND r.id=s.route_revision_id WHERE s.tenant_id=$1 AND s.user_id=$2 AND s.id=$3 AND s.mission_id=$4 AND s.route_revision_id=$5 FOR UPDATE OF s,r`, tenantID, item.UserID, item.SessionID, item.MissionID, item.RevisionID).Scan(&version, &status, &anonymousSubjectID, &expiresAt, &routeStatus, &routeRef, &routeHash, &claimSetHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if status != "route_generating" {
		return status == "route_ready", tx.Commit(ctx)
	}
	ready := routeStatus == "proposed" && routeRef != "" && routeHash != ""
	nextStatus, eventType := "route_failed", "OnboardingSessionUpdated"
	if ready {
		nextStatus, eventType = "route_ready", "RoutePreviewGenerated"
	}
	eventBody := map[string]any{"subject_id": item.SessionID, "subject_version": version + 1, "previous_state": "route_generating", "new_state": nextStatus, "reason_code": "planner_" + routeStatus, "route_revision_id": item.RevisionID, "claim_set_hash": claimSetHash}
	if ready {
		eventBody["payload_ref"] = routeRef
	}
	encoded, _ := json.Marshal(eventBody)
	manifest, err := reconciler.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: eventID, Class: "event-payload", ContentType: "application/json"}, encoded)
	if err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `UPDATE identity.onboarding_sessions SET version=version+1,status=$1,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND version=$5 AND status='route_generating'`, nextStatus, now, tenantID, item.SessionID, version)
	if err != nil || tag.RowsAffected() != 1 {
		return false, ErrRouteConflict
	}
	if ready && anonymousSubjectID != nil {
		_, err = tx.Exec(ctx, `INSERT INTO identity.onboarding_claims(id,tenant_id,user_id,anonymous_subject_id,onboarding_session_id,claim_key,status,source_route_revision_id,expires_at) VALUES($1,$2,$3,$4,$5,$6,'available',$7,$8) ON CONFLICT (claim_key) DO NOTHING`, claimID, tenantID, item.UserID, *anonymousSubjectID, item.SessionID, claimKey, item.RevisionID, expiresAt)
		if err != nil {
			return false, err
		}
	}
	actor := json.RawMessage(`{"kind":"system","id":"product-worker"}`)
	_, err = reconciler.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: tenantID, UserID: item.UserID, EventType: eventType, SchemaVersion: 1, AggregateKind: "onboarding_session", AggregateID: item.SessionID, AggregateVersion: version + 1, StoreEpoch: reconciler.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: item.SessionID, PayloadRef: manifest.Ref, PayloadHash: manifest.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: commandID, CommandType: "events.publish", PayloadRef: manifest.Ref, PayloadHash: manifest.Hash}}})
	if err != nil {
		return false, err
	}
	return ready, tx.Commit(ctx)
}

func (reconciler OnboardingRouteReconciler) valid() bool {
	return reconciler.Pool != nil && reconciler.Payloads != nil && len(reconciler.IDKey) >= 32 && reconciler.StoreEpoch != ""
}
func (reconciler OnboardingRouteReconciler) now() time.Time {
	if reconciler.Now != nil {
		return reconciler.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}
