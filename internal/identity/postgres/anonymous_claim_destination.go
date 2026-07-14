package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/identity/anonymousclaim"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

type claimRouteSnapshot struct {
	RouteID, MissionID, SourceRoleProfileID, TargetRoleProfileID  string
	ClaimSetHash, RoutePayload, GoalPayload                       string
	InputManifest                                                 json.RawMessage
	AgentProfileSnapshotID, OntologySnapshotID, ContentSnapshotID string
}

// AnonymousClaimDestination copies an immutable anonymous route snapshot into
// the authenticated tenant. Source reads and destination writes use separate
// transactions; claim_key and deterministic IDs reconcile unknown commits.
type AnonymousClaimDestination struct {
	Pool           *pgxpool.Pool
	SystemTenantID string
	IdentityKey    []byte
	Payloads       payload.Store
	Appender       eventpostgres.Appender
	StoreEpoch     string
	Now            func() time.Time
}

func (destination AnonymousClaimDestination) CommitDestination(ctx context.Context, saga anonymousclaim.Saga) (string, error) {
	if destination.Pool == nil || destination.SystemTenantID == "" || len(destination.IdentityKey) < 32 || destination.Payloads == nil || destination.StoreEpoch == "" || saga.ID == "" || saga.ClaimKey == "" || saga.TargetTenantID == "" || saga.TargetUserID == "" || saga.MissionID == "" || saga.Status != anonymousclaim.Reserved {
		return "", anonymousclaim.ErrInvariant
	}
	if eventID, found, err := destination.loadCommitted(ctx, saga); err != nil || found {
		return eventID, err
	}
	snapshot, err := destination.loadSource(ctx, saga)
	if err != nil {
		return "", err
	}
	routeID, err := ids.DeterministicUUID(destination.IdentityKey, "anonymous-claim-target-route", saga.ClaimKey)
	if err != nil {
		return "", err
	}
	importID, err := ids.DeterministicUUID(destination.IdentityKey, "anonymous-claim-mission-import", saga.ClaimKey)
	if err != nil {
		return "", err
	}
	eventID, err := ids.DeterministicUUID(destination.IdentityKey, "anonymous-claim-destination-event", saga.ClaimKey)
	if err != nil {
		return "", err
	}
	outboxID, err := ids.DeterministicUUID(destination.IdentityKey, "anonymous-claim-destination-outbox", saga.ClaimKey)
	if err != nil {
		return "", err
	}
	commandID, err := ids.DeterministicUUID(destination.IdentityKey, "anonymous-claim-destination-publish", saga.ClaimKey)
	if err != nil {
		return "", err
	}
	targetRoutePayload, err := destination.copyManifest(ctx, payload.Descriptor{TenantID: destination.SystemTenantID, ObjectID: snapshot.RouteID, Class: "route-revision", ContentType: "application/json"}, payload.Descriptor{TenantID: saga.TargetTenantID, ObjectID: routeID, Class: "route-revision", ContentType: "application/json"}, snapshot.RoutePayload, true)
	if err != nil {
		return "", err
	}
	targetGoalPayload, err := destination.copyManifest(ctx, payload.Descriptor{TenantID: destination.SystemTenantID, ObjectID: snapshot.MissionID, Class: "mission-goal", ContentType: "application/json"}, payload.Descriptor{TenantID: saga.TargetTenantID, ObjectID: saga.MissionID, Class: "mission-goal", ContentType: "application/json"}, snapshot.GoalPayload, false)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	if destination.Now != nil {
		now = destination.Now().UTC()
	}
	eventManifest, err := destination.putJSON(ctx, payload.Descriptor{TenantID: saga.TargetTenantID, ObjectID: eventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{
		"subject_id": saga.ID, "subject_version": saga.Version + 1, "claim_set_hash": snapshot.ClaimSetHash, "claim_id": saga.ID, "claim_key": saga.ClaimKey,
		"target_tenant_id": saga.TargetTenantID, "target_user_id": saga.TargetUserID, "mission_id": saga.MissionID, "route_revision_id": routeID, "commit_event_id": eventID,
	})
	if err != nil {
		return "", err
	}
	tx, err := destination.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, saga.TargetTenantID); err != nil {
		return "", err
	}
	var authorized bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM identity.users u JOIN identity.memberships m ON m.user_id=u.id AND m.tenant_id=$2 WHERE u.id=$1 AND u.status='active' AND u.email_verified_at IS NOT NULL AND m.status='active')`, saga.TargetUserID, saga.TargetTenantID).Scan(&authorized); err != nil || !authorized {
		if err != nil {
			return "", err
		}
		return "", anonymousclaim.ErrInvariant
	}
	if existingEvent, found, loadErr := loadMissionImportTx(ctx, tx, saga); loadErr != nil || found {
		return existingEvent, loadErr
	}
	missionTag, err := tx.Exec(ctx, `INSERT INTO product.missions(id,tenant_id,user_id,status,source_role_profile_id,target_role_profile_id,goal_payload_ref,route_version,claim_set_hash,current_route_revision_id) VALUES($1,$2,$3,'active',NULLIF($4,'')::uuid,$5,NULLIF($6,''),1,$7,$8) ON CONFLICT (id) DO NOTHING`, saga.MissionID, saga.TargetTenantID, saga.TargetUserID, snapshot.SourceRoleProfileID, snapshot.TargetRoleProfileID, targetGoalPayload, snapshot.ClaimSetHash, routeID)
	if err != nil {
		return "", err
	}
	if missionTag.RowsAffected() == 0 {
		var valid bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product.missions WHERE id=$1 AND tenant_id=$2 AND user_id=$3 AND claim_set_hash=$4 AND current_route_revision_id=$5)`, saga.MissionID, saga.TargetTenantID, saga.TargetUserID, snapshot.ClaimSetHash, routeID).Scan(&valid); err != nil || !valid {
			if err != nil {
				return "", err
			}
			return "", anonymousclaim.ErrInvariant
		}
	}
	routeTag, err := tx.Exec(ctx, `INSERT INTO product.route_revisions(id,tenant_id,user_id,mission_id,route_version,status,claim_set_hash,base_route_version,input_manifest,route_payload_ref,agent_profile_snapshot_id,ontology_snapshot_id,content_snapshot_id,accepted_at) VALUES($1,$2,$3,$4,1,'accepted',$5,0,$6,$7,$8,$9,$10,$11) ON CONFLICT (id) DO NOTHING`, routeID, saga.TargetTenantID, saga.TargetUserID, saga.MissionID, snapshot.ClaimSetHash, snapshot.InputManifest, targetRoutePayload, snapshot.AgentProfileSnapshotID, snapshot.OntologySnapshotID, snapshot.ContentSnapshotID, now)
	if err != nil {
		return "", err
	}
	if routeTag.RowsAffected() == 0 {
		var valid bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product.route_revisions WHERE id=$1 AND tenant_id=$2 AND user_id=$3 AND mission_id=$4 AND claim_set_hash=$5)`, routeID, saga.TargetTenantID, saga.TargetUserID, saga.MissionID, snapshot.ClaimSetHash).Scan(&valid); err != nil || !valid {
			if err != nil {
				return "", err
			}
			return "", anonymousclaim.ErrInvariant
		}
	}
	importTag, err := tx.Exec(ctx, `INSERT INTO product.mission_imports(id,tenant_id,user_id,claim_key,mission_id,source_route_revision_id,commit_event_id,imported_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (claim_key) DO NOTHING`, importID, saga.TargetTenantID, saga.TargetUserID, saga.ClaimKey, saga.MissionID, snapshot.RouteID, eventID, now)
	if err != nil {
		return "", err
	}
	if importTag.RowsAffected() == 0 {
		existingEvent, found, loadErr := loadMissionImportTx(ctx, tx, saga)
		if loadErr != nil || !found {
			if loadErr != nil {
				return "", loadErr
			}
			return "", anonymousclaim.ErrInvariant
		}
		return existingEvent, nil
	}
	if _, err = destination.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: saga.TargetTenantID, UserID: saga.TargetUserID, EventType: "AnonymousClaimDestinationCommitted", SchemaVersion: 1, AggregateKind: "anonymous_claim", AggregateID: saga.ID, AggregateVersion: saga.Version + 1, StoreEpoch: destination.StoreEpoch, OccurredAt: now, Actor: json.RawMessage(`{"kind":"system","id":"anonymous-claim-worker"}`), CorrelationID: saga.ID, PayloadRef: eventManifest.Ref, PayloadHash: eventManifest.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: commandID, CommandType: "events.publish", PayloadRef: eventManifest.Ref, PayloadHash: eventManifest.Hash}}}); err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return eventID, nil
}

func (destination AnonymousClaimDestination) loadSource(ctx context.Context, saga anonymousclaim.Saga) (claimRouteSnapshot, error) {
	tx, err := destination.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return claimRouteSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, destination.SystemTenantID); err != nil {
		return claimRouteSnapshot{}, err
	}
	var snapshot claimRouteSnapshot
	err = tx.QueryRow(ctx, `SELECT r.id::text,m.id::text,COALESCE(m.source_role_profile_id::text,''),m.target_role_profile_id::text,r.claim_set_hash,r.input_manifest,COALESCE(r.route_payload_ref,''),COALESCE(m.goal_payload_ref,''),r.agent_profile_snapshot_id,r.ontology_snapshot_id,r.content_snapshot_id FROM identity.onboarding_claims c JOIN product.route_revisions r ON r.id=c.source_route_revision_id AND r.tenant_id=c.tenant_id JOIN product.missions m ON m.id=r.mission_id AND m.tenant_id=r.tenant_id WHERE c.id=$1 AND c.tenant_id=$2 AND c.claim_key=$3 AND c.status='reserved'`, saga.ID, destination.SystemTenantID, saga.ClaimKey).Scan(&snapshot.RouteID, &snapshot.MissionID, &snapshot.SourceRoleProfileID, &snapshot.TargetRoleProfileID, &snapshot.ClaimSetHash, &snapshot.InputManifest, &snapshot.RoutePayload, &snapshot.GoalPayload, &snapshot.AgentProfileSnapshotID, &snapshot.OntologySnapshotID, &snapshot.ContentSnapshotID)
	if err != nil {
		return claimRouteSnapshot{}, err
	}
	if snapshot.RoutePayload == "" || snapshot.ClaimSetHash == "" || snapshot.TargetRoleProfileID == "" {
		return claimRouteSnapshot{}, anonymousclaim.ErrInvariant
	}
	if err = tx.Commit(ctx); err != nil {
		return claimRouteSnapshot{}, err
	}
	return snapshot, nil
}

func (destination AnonymousClaimDestination) loadCommitted(ctx context.Context, saga anonymousclaim.Saga) (string, bool, error) {
	tx, err := destination.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, saga.TargetTenantID); err != nil {
		return "", false, err
	}
	eventID, found, err := loadMissionImportTx(ctx, tx, saga)
	if err != nil || !found {
		return eventID, found, err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", false, err
	}
	return eventID, true, nil
}

func loadMissionImportTx(ctx context.Context, tx pgx.Tx, saga anonymousclaim.Saga) (string, bool, error) {
	var missionID, userID, eventID string
	err := tx.QueryRow(ctx, `SELECT mission_id::text,user_id::text,commit_event_id::text FROM product.mission_imports WHERE tenant_id=$1 AND claim_key=$2`, saga.TargetTenantID, saga.ClaimKey).Scan(&missionID, &userID, &eventID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if missionID != saga.MissionID || userID != saga.TargetUserID || eventID == "" {
		return "", false, anonymousclaim.ErrInvariant
	}
	return eventID, true, nil
}

func (destination AnonymousClaimDestination) copyManifest(ctx context.Context, sourceDescriptor, targetDescriptor payload.Descriptor, encoded string, required bool) (string, error) {
	if encoded == "" {
		if required {
			return "", anonymousclaim.ErrInvariant
		}
		return "", nil
	}
	var source payload.Manifest
	if err := json.Unmarshal([]byte(encoded), &source); err != nil || source.Ref == "" || source.Hash == "" {
		return "", anonymousclaim.ErrInvariant
	}
	plaintext, err := destination.Payloads.Get(ctx, sourceDescriptor, source)
	if err != nil {
		return "", err
	}
	target, err := destination.Payloads.Put(ctx, targetDescriptor, plaintext)
	if err != nil {
		return "", err
	}
	result, err := json.Marshal(target)
	return string(result), err
}

func (destination AnonymousClaimDestination) putJSON(ctx context.Context, descriptor payload.Descriptor, value any) (payload.Manifest, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return payload.Manifest{}, err
	}
	return destination.Payloads.Put(ctx, descriptor, encoded)
}

var _ anonymousclaim.DestinationWriter = AnonymousClaimDestination{}
