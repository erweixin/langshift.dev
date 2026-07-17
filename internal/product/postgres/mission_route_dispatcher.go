package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/payload"
	productapi "github.com/langshift/lites/internal/product/api"
)

const defaultMissionRouteConsumer = "product-route-worker"

var (
	ErrMissionRouteCommand  = errors.New("mission route command is invalid")
	ErrMissionRouteObsolete = errors.New("mission route command is obsolete")
)

// MissionRouteDispatcher turns the durable command emitted by MissionCreated
// into an immutable RouteRevision. Inbox completion, the revision, its domain
// event, and RoutePlanningRequested are committed in one database transaction.
type MissionRouteDispatcher struct {
	Routes       RouteService
	Inbox        eventpostgres.InboxStore
	ConsumerName string
}

type MissionRouteDispatchResult struct {
	Claimed, Completed, Replayed, Superseded bool
	Route                                    RouteResult
}

type missionRouteSnapshot struct {
	Manifest     routeInputManifest
	ManifestJSON json.RawMessage
}

type missionRouteCommandDocument struct {
	SchemaVersion        int    `json:"schema_version"`
	MissionID            string `json:"mission_id"`
	UserID               string `json:"user_id"`
	ExpectedRouteVersion uint64 `json:"expected_route_version"`
	ExpectedClaimSetHash string `json:"expected_claim_set_hash"`
	CorrelationID        string `json:"correlation_id"`
}

func (dispatcher MissionRouteDispatcher) Dispatch(ctx context.Context, command eventpostgres.DeliveredCommand) (MissionRouteDispatchResult, error) {
	if !dispatcher.Routes.valid() || command.CommandType != "GenerateMissionRoute" || command.AggregateKind != "mission" || command.AggregateID == "" || command.StoreEpoch != dispatcher.Routes.Store.StoreEpoch {
		return MissionRouteDispatchResult{}, ErrMissionRouteCommand
	}
	consumer := dispatcher.ConsumerName
	if consumer == "" {
		consumer = defaultMissionRouteConsumer
	}
	claim, err := dispatcher.Inbox.Claim(ctx, consumer, command)
	if err != nil {
		return MissionRouteDispatchResult{}, err
	}
	if claim.Completed {
		return MissionRouteDispatchResult{Completed: true, Replayed: true}, nil
	}
	result := MissionRouteDispatchResult{Claimed: true}
	result.Route, err = dispatcher.startDelivery(ctx, claim)
	if err != nil {
		if errors.Is(err, ErrMissionRouteObsolete) {
			if err = dispatcher.Inbox.Complete(ctx, claim); err != nil {
				return result, err
			}
			result.Completed = true
			result.Superseded = true
			return result, nil
		}
		if abandonErr := dispatcher.Inbox.Abandon(ctx, claim); abandonErr != nil && !errors.Is(abandonErr, eventpostgres.ErrDeliveryConflict) {
			return result, abandonErr
		}
		return result, err
	}
	result.Completed = true
	return result, nil
}

func (dispatcher MissionRouteDispatcher) startDelivery(ctx context.Context, claim eventpostgres.InboxClaim) (RouteResult, error) {
	service := dispatcher.Routes
	if claim.Completed || claim.Busy || claim.Command.CommandType != "GenerateMissionRoute" || claim.Command.AggregateKind != "mission" || claim.Command.StoreEpoch != service.Store.StoreEpoch {
		return RouteResult{}, ErrMissionRouteCommand
	}
	if err := service.Store.requireRouteEpoch(ctx); err != nil {
		return RouteResult{}, err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if service.Now != nil {
		now = service.Now().UTC().Truncate(time.Microsecond)
	}
	revisionID, plannerCommandID, err := service.generationIDs(claim.Command.CommandID)
	if err != nil {
		return RouteResult{}, err
	}
	document, err := dispatcher.loadCommand(ctx, claim.Command)
	if err != nil {
		return RouteResult{}, err
	}
	snapshot, err := dispatcher.snapshot(ctx, claim.Command, document)
	if err != nil {
		return RouteResult{}, err
	}
	manifestHash := sha256.Sum256(snapshot.ManifestJSON)
	eventPayload, err := service.putJSON(ctx, payload.Descriptor{TenantID: claim.Command.TenantID, ObjectID: revisionID, Class: routeEventClass, ContentType: "application/json"}, map[string]any{
		"subject_id": revisionID, "subject_version": 1, "request_id": document.CorrelationID,
		"claim_set_hash": document.ExpectedClaimSetHash, "mission_id": claim.Command.AggregateID,
		"route_revision_id": revisionID, "base_route_version": document.ExpectedRouteVersion,
		"input_manifest_hash":  hex.EncodeToString(manifestHash[:]),
		"profile_snapshot_id":  snapshot.Manifest.AgentProfileSnapshotID,
		"causation_command_id": claim.Command.CommandID,
	})
	if err != nil {
		return RouteResult{}, err
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RouteResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claim.Command.TenantID); err != nil {
		return RouteResult{}, err
	}
	if err = dispatcher.Inbox.LockClaimTx(ctx, tx, claim, now); err != nil {
		return RouteResult{}, err
	}
	var userID, claimSetHash string
	var routeVersion uint64
	if err = tx.QueryRow(ctx, `SELECT user_id::text,route_version,claim_set_hash FROM product.missions WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, claim.Command.TenantID, claim.Command.AggregateID).Scan(&userID, &routeVersion, &claimSetHash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RouteResult{}, ErrMissionRouteObsolete
		}
		return RouteResult{}, err
	}
	if userID != document.UserID || routeVersion != document.ExpectedRouteVersion || claimSetHash != document.ExpectedClaimSetHash {
		return RouteResult{}, ErrMissionRouteObsolete
	}
	generate := productapi.GenerateRouteCommand{
		CommandMetadata: productapi.CommandMetadata{
			RequestID:       document.CorrelationID,
			ClientRequestID: claim.Command.CommandID,
			IdempotencyKey:  claim.Command.CommandID,
			TenantID:        claim.Command.TenantID,
			UserID:          document.UserID,
			SessionID:       "system:mission-route-worker",
		},
		MissionID:            claim.Command.AggregateID,
		ExpectedRouteVersion: document.ExpectedRouteVersion,
		ExpectedClaimSetHash: document.ExpectedClaimSetHash,
	}
	manifest, err := service.resolveInputManifest(ctx, tx, generate)
	if err != nil {
		return RouteResult{}, err
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return RouteResult{}, err
	}
	if !bytes.Equal(manifestJSON, snapshot.ManifestJSON) {
		return RouteResult{}, ErrRouteConflict
	}
	actor, _ := json.Marshal(map[string]any{"kind": "system", "service": "product-worker"})
	result, err := service.Store.BeginGenerationInTx(ctx, tx, BeginRouteGenerationCommand{
		MutationID: claim.Command.CommandID, RevisionID: revisionID,
		TenantID: claim.Command.TenantID, UserID: document.UserID, MissionID: claim.Command.AggregateID,
		ExpectedRouteVersion: document.ExpectedRouteVersion, ExpectedClaimSetHash: document.ExpectedClaimSetHash,
		PlannerCommandID: plannerCommandID, InputManifest: manifestJSON,
		AgentProfileSnapshotID: manifest.AgentProfileSnapshotID,
		OntologySnapshotID:     manifest.OntologySnapshotID, ContentSnapshotID: manifest.ContentSnapshotID,
		CorrelationID: document.CorrelationID, Actor: actor,
		RequestedEvent: PayloadPointer{Ref: eventPayload.Ref, Hash: eventPayload.Hash},
	})
	if err != nil {
		return RouteResult{}, err
	}
	if err = dispatcher.Inbox.CompleteTx(ctx, tx, claim, now); err != nil {
		return RouteResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RouteResult{}, err
	}
	return result, nil
}

func (dispatcher MissionRouteDispatcher) loadCommand(ctx context.Context, command eventpostgres.DeliveredCommand) (missionRouteCommandDocument, error) {
	encoded, err := dispatcher.Routes.Payloads.Get(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: command.CommandID, Class: missionCommandClass, ContentType: "application/json"}, payload.Manifest{Ref: command.PayloadRef, Hash: command.PayloadHash})
	if err != nil {
		return missionRouteCommandDocument{}, err
	}
	var document missionRouteCommandDocument
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&document) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || document.SchemaVersion != 1 || document.MissionID != command.AggregateID || document.UserID == "" || !routeDigestPattern.MatchString(document.ExpectedClaimSetHash) || document.CorrelationID == "" {
		return missionRouteCommandDocument{}, ErrMissionRouteCommand
	}
	return document, nil
}

func (dispatcher MissionRouteDispatcher) snapshot(ctx context.Context, command eventpostgres.DeliveredCommand, document missionRouteCommandDocument) (missionRouteSnapshot, error) {
	service := dispatcher.Routes
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return missionRouteSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return missionRouteSnapshot{}, err
	}
	var userID, claimSetHash string
	var routeVersion uint64
	if err = tx.QueryRow(ctx, `SELECT user_id::text,route_version,claim_set_hash FROM product.missions WHERE tenant_id=$1 AND id=$2`, command.TenantID, command.AggregateID).Scan(&userID, &routeVersion, &claimSetHash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return missionRouteSnapshot{}, ErrMissionRouteObsolete
		}
		return missionRouteSnapshot{}, err
	}
	if userID != document.UserID || routeVersion != document.ExpectedRouteVersion || claimSetHash != document.ExpectedClaimSetHash {
		return missionRouteSnapshot{}, ErrMissionRouteObsolete
	}
	var result missionRouteSnapshot
	generate := productapi.GenerateRouteCommand{CommandMetadata: productapi.CommandMetadata{TenantID: command.TenantID, UserID: document.UserID}, MissionID: command.AggregateID, ExpectedRouteVersion: document.ExpectedRouteVersion, ExpectedClaimSetHash: document.ExpectedClaimSetHash}
	result.Manifest, err = service.resolveInputManifest(ctx, tx, generate)
	if err != nil {
		return missionRouteSnapshot{}, err
	}
	result.ManifestJSON, err = json.Marshal(result.Manifest)
	if err != nil {
		return missionRouteSnapshot{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return missionRouteSnapshot{}, err
	}
	return result, nil
}
