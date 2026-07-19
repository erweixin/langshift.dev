package postgres

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/idempotency"
	idempotencypostgres "github.com/langshift/lites/internal/idempotency/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	productapi "github.com/langshift/lites/internal/product/api"
)

const (
	claimCreateOperation = "claims.create.v2"
	claimReviseOperation = "claims.revise.v2"
	claimPageSize        = 50
	claimStatementClass  = "capability-claim-statement"
	claimReasonClass     = "capability-claim-reason"
	claimResponseClass   = "capability-claim-idempotency"
)

type CapabilityClaimService struct {
	Pool                                                        *pgxpool.Pool
	Appender                                                    eventpostgres.Appender
	Payloads                                                    payload.Store
	Catalog                                                     CapabilityProjector
	IDKey, CursorKey, IdempotencyKeyPepper, RequestDigestPepper []byte
	StoreEpoch                                                  string
	IdempotencyTTL                                              time.Duration
	Now                                                         func() time.Time
}

type claimCursor struct {
	TenantID   string    `json:"tenant_id"`
	UserID     string    `json:"user_id"`
	Recorded   time.Time `json:"recorded_at"`
	RevisionID string    `json:"revision_id"`
}

type storedClaimRevision struct {
	RevisionID, IdentityID, MissionID, CapabilityID string
	Status, Origin, VerificationLevel, StatementRef string
	ReasonRef                                       *string
	Version                                         uint64
	RecordedAt                                      time.Time
}

type staleRoute struct {
	ID, PreviousStatus, PreviousClaimSetHash string
	Version, RouteVersion                    uint64
}

func (service CapabilityClaimService) List(ctx context.Context, query productapi.CapabilityClaimListQuery) (productapi.CapabilityClaimListResult, error) {
	if !service.valid() || query.TenantID == "" || query.UserID == "" {
		return productapi.CapabilityClaimListResult{}, productapi.ErrValidation
	}
	var cursor *claimCursor
	if query.Cursor != "" {
		decoded, err := service.decodeCursor(query.Cursor)
		if err != nil || decoded.TenantID != query.TenantID || decoded.UserID != query.UserID {
			return productapi.CapabilityClaimListResult{}, productapi.ErrValidation
		}
		cursor = &decoded
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return productapi.CapabilityClaimListResult{}, service.mapError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, query.TenantID); err != nil {
		return productapi.CapabilityClaimListResult{}, service.mapError(err)
	}
	args := []any{query.TenantID, query.UserID, claimPageSize + 1}
	statement := `WITH latest AS (SELECT DISTINCT ON (claim_identity_id) id,claim_identity_id,version,mission_id,capability_id,status,origin,verification_level,statement_ref,reason_ref,recorded_at,updated_at FROM product.capability_claims WHERE tenant_id=$1 AND user_id=$2 ORDER BY claim_identity_id,claim_revision DESC,id DESC) SELECT id::text,claim_identity_id::text,version,mission_id::text,capability_id::text,status,origin,verification_level,statement_ref,reason_ref,recorded_at,updated_at,ARRAY(SELECT link.evidence_id::text FROM product.claim_evidence_links link WHERE link.tenant_id=$1 AND link.claim_id=latest.id ORDER BY link.evidence_id) FROM latest`
	if cursor != nil {
		statement += ` WHERE (recorded_at<$4 OR (recorded_at=$4 AND id<$5::uuid))`
		args = append(args, cursor.Recorded, cursor.RevisionID)
	}
	statement += ` ORDER BY recorded_at DESC,id DESC LIMIT $3`
	rows, err := tx.Query(ctx, statement, args...)
	if err != nil {
		return productapi.CapabilityClaimListResult{}, service.mapError(err)
	}
	defer rows.Close()
	items := make([]productapi.CapabilityClaimResource, 0, claimPageSize+1)
	for rows.Next() {
		var item productapi.CapabilityClaimResource
		var statementRef string
		var reasonRef *string
		if err = rows.Scan(&item.RevisionID, &item.ID, &item.Version, &item.MissionID, &item.CapabilityID, &item.Status, &item.Origin, &item.VerificationLevel, &statementRef, &reasonRef, &item.RecordedAt, &item.UpdatedAt, &item.EvidenceIDs); err != nil {
			return productapi.CapabilityClaimListResult{}, service.mapError(err)
		}
		if item.Statement, err = service.readText(ctx, query.TenantID, item.RevisionID, claimStatementClass, statementRef); err != nil {
			return productapi.CapabilityClaimListResult{}, service.mapError(err)
		}
		if reasonRef != nil {
			reason, readErr := service.readText(ctx, query.TenantID, item.RevisionID, claimReasonClass, *reasonRef)
			if readErr != nil {
				return productapi.CapabilityClaimListResult{}, service.mapError(readErr)
			}
			item.Reason = &reason
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return productapi.CapabilityClaimListResult{}, service.mapError(err)
	}
	var next *string
	if len(items) > claimPageSize {
		last := items[claimPageSize-1]
		encoded, encodeErr := service.encodeCursor(claimCursor{TenantID: query.TenantID, UserID: query.UserID, Recorded: last.RecordedAt, RevisionID: last.RevisionID})
		if encodeErr != nil {
			return productapi.CapabilityClaimListResult{}, service.mapError(encodeErr)
		}
		next = &encoded
		items = items[:claimPageSize]
	}
	if err = tx.Commit(ctx); err != nil {
		return productapi.CapabilityClaimListResult{}, service.mapError(err)
	}
	return productapi.CapabilityClaimListResult{Items: items, NextCursor: next}, nil
}

func (service CapabilityClaimService) Create(ctx context.Context, command productapi.CreateCapabilityClaimCommand) (productapi.CapabilityClaimMutationResult, error) {
	if !service.valid() || !validMissionMetadata(command.CommandMetadata) || command.MissionID == "" || command.CapabilityID == "" || command.Statement == "" || !validClaimOrigin(command.Origin) {
		return productapi.CapabilityClaimMutationResult{}, productapi.ErrValidation
	}
	canonical, _ := json.Marshal(command)
	input, descriptor, err := service.idempotencyInput(command.CommandMetadata, claimCreateOperation, canonical)
	if err != nil {
		return productapi.CapabilityClaimMutationResult{}, productapi.ErrDependencyUnavailable
	}
	if result, found, loadErr := service.loadResult(ctx, input, descriptor); loadErr != nil {
		return productapi.CapabilityClaimMutationResult{}, service.mapError(loadErr)
	} else if found {
		return result, nil
	}
	identityID, err := ids.DeterministicUUID(service.IDKey, "capability-claim-identity", input.RecordID)
	if err != nil {
		return productapi.CapabilityClaimMutationResult{}, service.mapError(err)
	}
	revisionID, _ := ids.DeterministicUUID(service.IDKey, "capability-claim-revision", input.RecordID)
	eventID, _ := ids.DeterministicUUID(service.IDKey, "capability-claim-event", input.RecordID)
	statementRef, err := service.storeText(ctx, command.TenantID, revisionID, claimStatementClass, command.Statement)
	if err != nil {
		return productapi.CapabilityClaimMutationResult{}, service.mapError(err)
	}
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if _, innerErr := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if innerErr := service.Catalog.EnsureCapabilities(ctx, tx, command.TenantID, command.CapabilityID); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		var missionVersion uint64
		var missionStatus string
		if innerErr := tx.QueryRow(ctx, `SELECT version,status FROM product.missions WHERE tenant_id=$1 AND user_id=$2 AND id=$3 FOR UPDATE`, command.TenantID, command.UserID, command.MissionID).Scan(&missionVersion, &missionStatus); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if missionStatus == "archived" {
			return idempotency.Response{}, ErrRouteConflict
		}
		if innerErr := validateClaimEvidence(ctx, tx, command.TenantID, command.UserID, command.MissionID, command.EvidenceIDs); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		now := service.now()
		if _, innerErr := tx.Exec(ctx, `INSERT INTO product.capability_claims(id,tenant_id,user_id,version,claim_identity_id,claim_revision,mission_id,capability_id,status,origin,verification_level,statement_ref,recorded_at,created_at,updated_at) VALUES($1,$2,$3,1,$4,1,$5,$6,'active',$7,'inferred',$8,$9,$9,$9)`, revisionID, command.TenantID, command.UserID, identityID, command.MissionID, command.CapabilityID, command.Origin, statementRef, now); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if innerErr := service.linkEvidence(ctx, tx, command.TenantID, input.RecordID, revisionID, command.EvidenceIDs, "supports", now); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		claimSetHash, innerErr := computeClaimSetHash(ctx, tx, command.TenantID, command.UserID, command.MissionID)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		staleRoutes, innerErr := service.staleRoutes(ctx, tx, command.TenantID, command.UserID, command.MissionID, claimSetHash, now)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if innerErr = service.updateMissionClaimHash(ctx, tx, command.TenantID, command.UserID, command.MissionID, missionVersion, claimSetHash, staleRoutes, now); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if innerErr = service.appendClaimEvent(ctx, tx, claimEventInput{RecordID: input.RecordID, EventID: eventID, TenantID: command.TenantID, UserID: command.UserID, SessionID: command.SessionID, RequestID: command.RequestID, IdentityID: identityID, RevisionID: revisionID, Version: 1, MissionID: command.MissionID, Status: "active", VerificationLevel: "inferred", ReasonCode: "claim_created", ClaimSetHash: claimSetHash, EvidenceIDs: command.EvidenceIDs, Now: now}); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if innerErr = service.appendStaleRouteEvents(ctx, tx, input.RecordID, command.CommandMetadata, command.MissionID, claimSetHash, staleRoutes, now); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		result := productapi.CapabilityClaimMutationResult{ID: identityID, RevisionID: revisionID, Version: 1, Status: "active", ClaimSetHash: claimSetHash, UpdatedAt: now, EventID: eventID}
		return service.storeResult(ctx, descriptor, result)
	})
	if err != nil {
		return productapi.CapabilityClaimMutationResult{}, service.mapError(err)
	}
	result, err := service.readResult(ctx, descriptor, response)
	result.Replayed = replayed
	return result, service.mapError(err)
}

func (service CapabilityClaimService) Revise(ctx context.Context, command productapi.ReviseCapabilityClaimCommand) (productapi.CapabilityClaimMutationResult, error) {
	if !service.valid() || !validMissionMetadata(command.CommandMetadata) || command.ClaimIdentityID == "" || command.ExpectedVersion < 1 || command.Reason == "" || !validClaimAction(command.Action) || command.Action == "correct" && (command.CapabilityID == "" || command.Statement == "") {
		return productapi.CapabilityClaimMutationResult{}, productapi.ErrValidation
	}
	canonical, _ := json.Marshal(command)
	input, descriptor, err := service.idempotencyInput(command.CommandMetadata, claimReviseOperation, canonical)
	if err != nil {
		return productapi.CapabilityClaimMutationResult{}, productapi.ErrDependencyUnavailable
	}
	if result, found, loadErr := service.loadResult(ctx, input, descriptor); loadErr != nil {
		return productapi.CapabilityClaimMutationResult{}, service.mapError(loadErr)
	} else if found {
		return result, nil
	}
	revisionID, err := ids.DeterministicUUID(service.IDKey, "capability-claim-revision", input.RecordID)
	if err != nil {
		return productapi.CapabilityClaimMutationResult{}, service.mapError(err)
	}
	eventID, _ := ids.DeterministicUUID(service.IDKey, "capability-claim-event", input.RecordID)
	reasonRef, err := service.storeText(ctx, command.TenantID, revisionID, claimReasonClass, command.Reason)
	if err != nil {
		return productapi.CapabilityClaimMutationResult{}, service.mapError(err)
	}
	correctedStatementRef := ""
	if command.Action == "correct" {
		correctedStatementRef, err = service.storeText(ctx, command.TenantID, revisionID, claimStatementClass, command.Statement)
		if err != nil {
			return productapi.CapabilityClaimMutationResult{}, service.mapError(err)
		}
	}
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if _, innerErr := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		missionID, innerErr := claimMissionID(ctx, tx, command.TenantID, command.UserID, command.ClaimIdentityID)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		var missionVersion uint64
		if innerErr = tx.QueryRow(ctx, `SELECT version FROM product.missions WHERE tenant_id=$1 AND user_id=$2 AND id=$3 FOR UPDATE`, command.TenantID, command.UserID, missionID).Scan(&missionVersion); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		current, innerErr := readLatestClaim(ctx, tx, command.TenantID, command.UserID, command.ClaimIdentityID)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if current.MissionID != missionID {
			return idempotency.Response{}, ErrRouteConflict
		}
		if current.Version != command.ExpectedVersion || !allowedClaimAction(current, command.Action) {
			return idempotency.Response{}, ErrRouteConflict
		}
		capabilityID, statementRef, origin := current.CapabilityID, current.StatementRef, current.Origin
		status, verification := revisedClaimState(current, command.Action)
		if command.Action == "correct" {
			if innerErr = service.Catalog.EnsureCapabilities(ctx, tx, command.TenantID, command.CapabilityID); innerErr != nil {
				return idempotency.Response{}, innerErr
			}
			capabilityID, statementRef, origin = command.CapabilityID, correctedStatementRef, "user_asserted"
		}
		if innerErr = validateClaimEvidence(ctx, tx, command.TenantID, command.UserID, current.MissionID, command.EvidenceIDs); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		now := service.now()
		nextVersion := current.Version + 1
		if _, innerErr = tx.Exec(ctx, `INSERT INTO product.capability_claims(id,tenant_id,user_id,version,claim_identity_id,claim_revision,mission_id,capability_id,status,origin,verification_level,statement_ref,supersedes_claim_id,reason_ref,recorded_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$15,$15)`, revisionID, command.TenantID, command.UserID, nextVersion, command.ClaimIdentityID, nextVersion, current.MissionID, capabilityID, status, origin, verification, statementRef, current.RevisionID, reasonRef, now); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		relation := "supports"
		if command.Action == "dispute" || command.Action == "reject" {
			relation = "contradicts"
		}
		if innerErr = service.linkEvidence(ctx, tx, command.TenantID, input.RecordID, revisionID, command.EvidenceIDs, relation, now); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		claimSetHash, innerErr := computeClaimSetHash(ctx, tx, command.TenantID, command.UserID, current.MissionID)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		staleRoutes, innerErr := service.staleRoutes(ctx, tx, command.TenantID, command.UserID, current.MissionID, claimSetHash, now)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if innerErr = service.updateMissionClaimHash(ctx, tx, command.TenantID, command.UserID, current.MissionID, missionVersion, claimSetHash, staleRoutes, now); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if innerErr = service.appendClaimEvent(ctx, tx, claimEventInput{RecordID: input.RecordID, EventID: eventID, TenantID: command.TenantID, UserID: command.UserID, SessionID: command.SessionID, RequestID: command.RequestID, IdentityID: command.ClaimIdentityID, RevisionID: revisionID, PreviousRevisionID: current.RevisionID, Version: nextVersion, MissionID: current.MissionID, PreviousStatus: current.Status, Status: status, VerificationLevel: verification, ReasonCode: command.Action, ClaimSetHash: claimSetHash, EvidenceIDs: command.EvidenceIDs, Now: now}); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if innerErr = service.appendStaleRouteEvents(ctx, tx, input.RecordID, command.CommandMetadata, current.MissionID, claimSetHash, staleRoutes, now); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		result := productapi.CapabilityClaimMutationResult{ID: command.ClaimIdentityID, RevisionID: revisionID, Version: nextVersion, Status: status, ClaimSetHash: claimSetHash, UpdatedAt: now, EventID: eventID}
		return service.storeResult(ctx, descriptor, result)
	})
	if err != nil {
		return productapi.CapabilityClaimMutationResult{}, service.mapError(err)
	}
	result, err := service.readResult(ctx, descriptor, response)
	result.Replayed = replayed
	return result, service.mapError(err)
}

type claimEventInput struct {
	RecordID, EventID, TenantID, UserID, SessionID, RequestID           string
	IdentityID, RevisionID, PreviousRevisionID, MissionID               string
	PreviousStatus, Status, VerificationLevel, ReasonCode, ClaimSetHash string
	Version                                                             uint64
	EvidenceIDs                                                         []string
	Now                                                                 time.Time
}

func (service CapabilityClaimService) appendClaimEvent(ctx context.Context, tx pgx.Tx, input claimEventInput) error {
	payloadBody := map[string]any{"subject_id": input.IdentityID, "subject_version": input.Version, "previous_state": nullableString(input.PreviousStatus), "new_state": input.Status, "reason_code": input.ReasonCode, "claim_set_hash": input.ClaimSetHash, "claim_id": input.RevisionID, "claim_identity_id": input.IdentityID, "claim_revision": input.Version, "previous_claim_id": nullableString(input.PreviousRevisionID), "status": input.Status, "verification_level": input.VerificationLevel, "evidence_ids": input.EvidenceIDs}
	eventPayload, err := service.putJSON(ctx, payload.Descriptor{TenantID: input.TenantID, ObjectID: input.EventID, Class: routeEventClass, ContentType: "application/json"}, payloadBody)
	if err != nil {
		return err
	}
	outboxID, _ := ids.DeterministicUUID(service.IDKey, "capability-claim-outbox", input.RecordID)
	publishID, _ := ids.DeterministicUUID(service.IDKey, "capability-claim-publish", input.RecordID)
	_, err = service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: input.EventID, TenantID: input.TenantID, UserID: input.UserID, EventType: "CapabilityClaimRevised", SchemaVersion: 1, AggregateKind: "capability_claim", AggregateID: input.IdentityID, AggregateVersion: input.Version, StoreEpoch: service.StoreEpoch, OccurredAt: input.Now, Actor: missionActor(input.UserID, input.SessionID), CorrelationID: input.RequestID, PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishID, CommandType: "events.publish", PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}}})
	return err
}

func (service CapabilityClaimService) staleRoutes(ctx context.Context, tx pgx.Tx, tenantID, userID, missionID, claimSetHash string, now time.Time) ([]staleRoute, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,version,status,claim_set_hash,route_version FROM product.route_revisions WHERE tenant_id=$1 AND user_id=$2 AND mission_id=$3 AND status IN ('generating','proposed','accepted') ORDER BY route_version,id FOR UPDATE`, tenantID, userID, missionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []staleRoute{}
	for rows.Next() {
		var route staleRoute
		if err = rows.Scan(&route.ID, &route.Version, &route.PreviousStatus, &route.PreviousClaimSetHash, &route.RouteVersion); err != nil {
			return nil, err
		}
		if route.PreviousClaimSetHash == claimSetHash {
			continue
		}
		result = append(result, route)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for _, route := range result {
		tag, updateErr := tx.Exec(ctx, `UPDATE product.route_revisions SET version=version+1,status='stale',stale_reason='claim_set_changed',updated_at=$1 WHERE tenant_id=$2 AND user_id=$3 AND id=$4 AND version=$5 AND status=$6`, now, tenantID, userID, route.ID, route.Version, route.PreviousStatus)
		if updateErr != nil {
			return nil, updateErr
		}
		if tag.RowsAffected() != 1 {
			return nil, ErrRouteConflict
		}
	}
	return result, nil
}

func (service CapabilityClaimService) updateMissionClaimHash(ctx context.Context, tx pgx.Tx, tenantID, userID, missionID string, missionVersion uint64, claimSetHash string, staleRoutes []staleRoute, now time.Time) error {
	maximumRouteVersion := uint64(0)
	for _, route := range staleRoutes {
		if route.RouteVersion > maximumRouteVersion {
			maximumRouteVersion = route.RouteVersion
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE product.missions SET version=version+1,claim_set_hash=$1,route_version=GREATEST(route_version,$2),current_route_revision_id=CASE WHEN current_route_revision_id IN (SELECT id FROM product.route_revisions WHERE tenant_id=$3 AND user_id=$4 AND mission_id=$5 AND status='stale') THEN NULL ELSE current_route_revision_id END,updated_at=$6 WHERE tenant_id=$3 AND user_id=$4 AND id=$5 AND version=$7`, claimSetHash, maximumRouteVersion, tenantID, userID, missionID, now, missionVersion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrRouteConflict
	}
	return nil
}

func (service CapabilityClaimService) appendStaleRouteEvents(ctx context.Context, tx pgx.Tx, recordID string, metadata productapi.CommandMetadata, missionID, claimSetHash string, routes []staleRoute, now time.Time) error {
	for index, route := range routes {
		suffix := recordID + "\x00" + route.ID
		eventID, err := ids.DeterministicUUID(service.IDKey, "claim-route-stale-event", suffix)
		if err != nil {
			return err
		}
		body := map[string]any{"subject_id": route.ID, "subject_version": route.Version + 1, "previous_state": route.PreviousStatus, "new_state": "stale", "reason_code": "claim_set_changed", "claim_set_hash": claimSetHash, "mission_id": missionID, "route_revision_id": route.ID, "route_version": route.RouteVersion, "expected_claim_set_hash": route.PreviousClaimSetHash, "current_claim_set_hash": claimSetHash}
		manifest, err := service.putJSON(ctx, payload.Descriptor{TenantID: metadata.TenantID, ObjectID: eventID, Class: routeEventClass, ContentType: "application/json"}, body)
		if err != nil {
			return err
		}
		outboxID, _ := ids.DeterministicUUID(service.IDKey, "claim-route-stale-outbox", suffix)
		publishID, _ := ids.DeterministicUUID(service.IDKey, "claim-route-stale-publish", suffix)
		if _, err = service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: metadata.TenantID, UserID: metadata.UserID, EventType: "RouteMarkedStale", SchemaVersion: 1, AggregateKind: "route_revision", AggregateID: route.ID, AggregateVersion: route.Version + 1, StoreEpoch: service.StoreEpoch, OccurredAt: now.Add(time.Duration(index) * time.Microsecond), Actor: missionActor(metadata.UserID, metadata.SessionID), CorrelationID: metadata.RequestID, PayloadRef: manifest.Ref, PayloadHash: manifest.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishID, CommandType: "events.publish", PayloadRef: manifest.Ref, PayloadHash: manifest.Hash}}}); err != nil {
			return err
		}
	}
	return nil
}

func computeClaimSetHash(ctx context.Context, tx pgx.Tx, tenantID, userID, missionID string) (string, error) {
	rows, err := tx.Query(ctx, `WITH latest AS (SELECT DISTINCT ON (claim_identity_id) id,claim_identity_id,version,capability_id,status,origin,verification_level,statement_ref FROM product.capability_claims WHERE tenant_id=$1 AND user_id=$2 AND mission_id=$3 ORDER BY claim_identity_id,claim_revision DESC,id DESC) SELECT id::text,claim_identity_id::text,version,capability_id::text,status,origin,verification_level,statement_ref,ARRAY(SELECT link.evidence_id::text FROM product.claim_evidence_links link WHERE link.tenant_id=$1 AND link.claim_id=latest.id ORDER BY link.evidence_id) FROM latest ORDER BY claim_identity_id`, tenantID, userID, missionID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type claimHashInput struct {
		RevisionID, IdentityID, CapabilityID, Status, Origin, VerificationLevel, StatementRef string
		Version                                                                               uint64
		EvidenceIDs                                                                           []string
	}
	inputs := []claimHashInput{}
	for rows.Next() {
		var input claimHashInput
		if err = rows.Scan(&input.RevisionID, &input.IdentityID, &input.Version, &input.CapabilityID, &input.Status, &input.Origin, &input.VerificationLevel, &input.StatementRef, &input.EvidenceIDs); err != nil {
			return "", err
		}
		inputs = append(inputs, input)
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(inputs)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validateClaimEvidence(ctx context.Context, tx pgx.Tx, tenantID, userID, missionID string, evidenceIDs []string) error {
	if len(evidenceIDs) == 0 {
		return nil
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM product.evidence WHERE tenant_id=$1 AND user_id=$2 AND mission_id=$3 AND id=ANY($4::uuid[])`, tenantID, userID, missionID, evidenceIDs).Scan(&count); err != nil {
		return err
	}
	if count != len(evidenceIDs) {
		return ErrRouteNotFound
	}
	return nil
}

func (service CapabilityClaimService) linkEvidence(ctx context.Context, tx pgx.Tx, tenantID, recordID, revisionID string, evidenceIDs []string, relation string, now time.Time) error {
	idsToLink := append([]string(nil), evidenceIDs...)
	sort.Strings(idsToLink)
	for _, evidenceID := range idsToLink {
		linkID, err := ids.DeterministicUUID(service.IDKey, "capability-claim-evidence-link", recordID+"\x00"+evidenceID+"\x00"+relation)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO product.claim_evidence_links(id,tenant_id,claim_id,evidence_id,relation,recorded_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$6,$6)`, linkID, tenantID, revisionID, evidenceID, relation, now); err != nil {
			return err
		}
	}
	return nil
}

func claimMissionID(ctx context.Context, tx pgx.Tx, tenantID, userID, identityID string) (string, error) {
	var missionID string
	err := tx.QueryRow(ctx, `SELECT mission_id::text FROM product.capability_claims WHERE tenant_id=$1 AND user_id=$2 AND claim_identity_id=$3 ORDER BY claim_revision DESC,id DESC LIMIT 1`, tenantID, userID, identityID).Scan(&missionID)
	return missionID, err
}

// readLatestClaim is called only after the owning Mission row has been locked.
// Capability claims are append-only and the product role intentionally has no
// UPDATE privilege, so a FOR UPDATE lock on this table would violate the
// immutable-table privilege boundary.
func readLatestClaim(ctx context.Context, tx pgx.Tx, tenantID, userID, identityID string) (storedClaimRevision, error) {
	var result storedClaimRevision
	err := tx.QueryRow(ctx, `SELECT id::text,claim_identity_id::text,version,mission_id::text,capability_id::text,status,origin,verification_level,statement_ref,reason_ref,recorded_at FROM product.capability_claims WHERE tenant_id=$1 AND user_id=$2 AND claim_identity_id=$3 ORDER BY claim_revision DESC,id DESC LIMIT 1`, tenantID, userID, identityID).Scan(&result.RevisionID, &result.IdentityID, &result.Version, &result.MissionID, &result.CapabilityID, &result.Status, &result.Origin, &result.VerificationLevel, &result.StatementRef, &result.ReasonRef, &result.RecordedAt)
	return result, err
}

func allowedClaimAction(current storedClaimRevision, action string) bool {
	if action == "correct" {
		return true
	}
	if current.Status != "active" && current.Status != "disputed" {
		return false
	}
	switch action {
	case "confirm":
		return current.Status != "active" || current.VerificationLevel != "user_confirmed"
	case "dispute":
		return current.Status == "active"
	case "supersede", "withdraw", "reject":
		return true
	default:
		return false
	}
}

func revisedClaimState(current storedClaimRevision, action string) (string, string) {
	switch action {
	case "confirm":
		return "active", "user_confirmed"
	case "correct":
		return "active", "inferred"
	case "dispute":
		return "disputed", current.VerificationLevel
	case "supersede":
		return "superseded", current.VerificationLevel
	case "withdraw":
		return "withdrawn", current.VerificationLevel
	case "reject":
		return "rejected", current.VerificationLevel
	default:
		return current.Status, current.VerificationLevel
	}
}

func validClaimOrigin(value string) bool {
	return value == "inferred" || value == "user_asserted" || value == "system_derived" || value == "reviewer_asserted"
}

func validClaimAction(value string) bool {
	return value == "confirm" || value == "correct" || value == "dispute" || value == "supersede" || value == "withdraw" || value == "reject"
}

func (service CapabilityClaimService) idempotencyInput(metadata productapi.CommandMetadata, operation string, canonical []byte) (idempotencypostgres.Input, payload.Descriptor, error) {
	digest, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return idempotencypostgres.Input{}, payload.Descriptor{}, err
	}
	recordID, err := ids.DeterministicUUID(service.IDKey, "product-idempotency:"+operation, metadata.TenantID+"\x00"+metadata.UserID+"\x00"+metadata.IdempotencyKey)
	if err != nil {
		return idempotencypostgres.Input{}, payload.Descriptor{}, err
	}
	return idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: metadata.TenantID, UserID: metadata.UserID, OperationID: operation}, RawKey: metadata.IdempotencyKey, RequestHash: digest, RequestID: metadata.RequestID}, payload.Descriptor{TenantID: metadata.TenantID, ObjectID: recordID, Class: claimResponseClass, ContentType: "application/json"}, nil
}

func (service CapabilityClaimService) loadResult(ctx context.Context, input idempotencypostgres.Input, descriptor payload.Descriptor) (productapi.CapabilityClaimMutationResult, bool, error) {
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	response, found, err := executor.LoadCompleted(ctx, input)
	if err != nil || !found {
		return productapi.CapabilityClaimMutationResult{}, found, err
	}
	result, err := service.readResult(ctx, descriptor, response)
	result.Replayed = err == nil
	return result, true, err
}

func (service CapabilityClaimService) storeResult(ctx context.Context, descriptor payload.Descriptor, result productapi.CapabilityClaimMutationResult) (idempotency.Response, error) {
	manifest, err := service.putJSON(ctx, descriptor, result)
	if err != nil {
		return idempotency.Response{}, err
	}
	return idempotency.Response{Status: http.StatusOK, ContentType: "application/json", PayloadRef: manifest.Ref, Hash: manifest.Hash, ResourceVersion: result.Version}, nil
}

func (service CapabilityClaimService) readResult(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (productapi.CapabilityClaimMutationResult, error) {
	encoded, err := service.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: response.PayloadRef, Hash: response.Hash})
	if err != nil {
		return productapi.CapabilityClaimMutationResult{}, err
	}
	var result productapi.CapabilityClaimMutationResult
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return result, payload.ErrIntegrity
	}
	return result, nil
}

func (service CapabilityClaimService) storeText(ctx context.Context, tenantID, objectID, class, value string) (string, error) {
	manifest, err := service.putJSON(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: objectID, Class: class, ContentType: "application/json"}, map[string]any{"schema_version": 1, "text": value})
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(struct {
		ObjectID string           `json:"object_id"`
		Manifest payload.Manifest `json:"manifest"`
	}{ObjectID: objectID, Manifest: manifest})
	return string(encoded), err
}

func (service CapabilityClaimService) readText(ctx context.Context, tenantID, _ string, class, reference string) (string, error) {
	var stored struct {
		ObjectID string           `json:"object_id"`
		Manifest payload.Manifest `json:"manifest"`
	}
	decoder := json.NewDecoder(bytes.NewBufferString(reference))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&stored) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || stored.ObjectID == "" || stored.Manifest.Ref == "" || stored.Manifest.Hash == "" {
		return "", payload.ErrIntegrity
	}
	encoded, err := service.Payloads.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: stored.ObjectID, Class: class, ContentType: "application/json"}, stored.Manifest)
	if err != nil {
		return "", err
	}
	var document struct {
		SchemaVersion int    `json:"schema_version"`
		Text          string `json:"text"`
	}
	decoder = json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&document) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || document.SchemaVersion != 1 || document.Text == "" {
		return "", payload.ErrIntegrity
	}
	return document.Text, nil
}

func (service CapabilityClaimService) putJSON(ctx context.Context, descriptor payload.Descriptor, value any) (payload.Manifest, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return payload.Manifest{}, err
	}
	return service.Payloads.Put(ctx, descriptor, encoded)
}

func (service CapabilityClaimService) encodeCursor(cursor claimCursor) (string, error) {
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, service.CursorKey)
	_, _ = mac.Write(encoded)
	return base64.RawURLEncoding.EncodeToString(append(encoded, mac.Sum(nil)...)), nil
}

func (service CapabilityClaimService) decodeCursor(value string) (claimCursor, error) {
	if len(value) > 2048 {
		return claimCursor{}, productapi.ErrValidation
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) <= sha256.Size {
		return claimCursor{}, productapi.ErrValidation
	}
	body, signature := decoded[:len(decoded)-sha256.Size], decoded[len(decoded)-sha256.Size:]
	mac := hmac.New(sha256.New, service.CursorKey)
	_, _ = mac.Write(body)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return claimCursor{}, productapi.ErrValidation
	}
	var cursor claimCursor
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cursor) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || cursor.TenantID == "" || cursor.UserID == "" || cursor.RevisionID == "" || cursor.Recorded.IsZero() {
		return claimCursor{}, productapi.ErrValidation
	}
	return cursor, nil
}

func (service CapabilityClaimService) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func (service CapabilityClaimService) valid() bool {
	return service.Pool != nil && service.Payloads != nil && service.Catalog != nil && len(service.IDKey) >= 32 && len(service.CursorKey) >= 32 && len(service.IdempotencyKeyPepper) >= 32 && len(service.RequestDigestPepper) >= 32 && service.StoreEpoch != "" && service.IdempotencyTTL > 0
}

func (service CapabilityClaimService) mapError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows), errors.Is(err, ErrRouteNotFound), errors.Is(err, ErrMissionNotFound):
		return productapi.ErrResourceNotFound
	case errors.Is(err, idempotency.ErrKeyConflict), errors.Is(err, idempotency.ErrInProgress):
		return productapi.ErrIdempotencyConflict
	case errors.Is(err, ErrRouteConflict), errors.Is(err, eventpostgres.ErrVersionConflict):
		return productapi.ErrStateConflict
	case errors.Is(err, productapi.ErrValidation):
		return productapi.ErrValidation
	default:
		return errors.Join(productapi.ErrDependencyUnavailable, err)
	}
}

var _ productapi.CapabilityClaimService = CapabilityClaimService{}
