package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/platform/ids"
	"github.com/langshift/lites/internal/product/route"
)

var (
	ErrRouteConflict    = errors.New("route mutation conflicts with durable state")
	ErrRouteNotFound    = errors.New("route revision does not exist")
	ErrRouteResultStale = errors.New("route planner result is stale")
)

type RouteStore struct {
	Pool       *pgxpool.Pool
	Appender   eventpostgres.Appender
	IDKey      []byte
	StoreEpoch string
	Epochs     EpochAuthority
	Now        func() time.Time
}

type BeginRouteGenerationCommand struct {
	MutationID, RevisionID, TenantID, UserID, MissionID string
	ExpectedRouteVersion                                uint64
	ExpectedClaimSetHash, PlannerCommandID              string
	InputManifest                                       json.RawMessage
	AgentProfileSnapshotID, OntologySnapshotID          string
	ContentSnapshotID, CorrelationID                    string
	Actor                                               json.RawMessage
	RequestedEvent                                      PayloadPointer
}

type CompleteRouteGenerationCommand struct {
	MutationID, RevisionID, TenantID, UserID string
	ExpectedRevisionVersion                  uint64
	RoutePayload, CompletedEvent             PayloadPointer
	CorrelationID                            string
	Actor                                    json.RawMessage
}

type FailRouteGenerationCommand struct {
	MutationID, RevisionID, TenantID, UserID string
	ExpectedRevisionVersion                  uint64
	Reason, CorrelationID                    string
	Actor                                    json.RawMessage
	FailedEvent                              PayloadPointer
}

type AcceptRouteCommand struct {
	MutationID, RevisionID, TenantID, UserID string
	ExpectedRevisionVersion                  uint64
	ExpectedRouteVersion                     uint64
	ExpectedClaimSetHash, CorrelationID      string
	Actor                                    json.RawMessage
	AcceptedEvent                            PayloadPointer
	DailyTaskCommandID                       string
	DailyTaskCommand                         PayloadPointer
}

type RouteResult struct {
	RevisionID, MissionID, Status string
	RevisionVersion               uint64
	MissionVersion, RouteVersion  uint64
	CurrentRouteRevisionID        string
	PlannerCommandID              string
	EventID                       string
	Stale                         bool
}

func (store RouteStore) BeginGeneration(ctx context.Context, command BeginRouteGenerationCommand) (RouteResult, error) {
	if !store.validRoute() || !validBeginRouteCommand(command) {
		return RouteResult{}, ErrInvalidCommand
	}
	if err := store.requireRouteEpoch(ctx); err != nil {
		return RouteResult{}, err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RouteResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := store.BeginGenerationInTx(ctx, tx, command)
	if err != nil {
		return RouteResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RouteResult{}, err
	}
	return result, nil
}

func (store RouteStore) BeginGenerationInTx(ctx context.Context, tx pgx.Tx, command BeginRouteGenerationCommand) (RouteResult, error) {
	if !store.validRoute() || tx == nil || !validBeginRouteCommand(command) {
		return RouteResult{}, ErrInvalidCommand
	}
	state, err := store.lockRouteState(ctx, tx, command.TenantID, command.UserID, command.MissionID)
	if err != nil {
		return RouteResult{}, err
	}
	next, err := state.Begin(route.BeginCommand{RevisionID: command.RevisionID, ExpectedRouteVersion: command.ExpectedRouteVersion, ExpectedClaimSetHash: command.ExpectedClaimSetHash})
	if err != nil {
		return RouteResult{}, mapRouteError(err)
	}
	revision := next.Revisions[command.RevisionID]
	now := store.nowRoute()
	if _, err = tx.Exec(ctx, `INSERT INTO product.route_revisions(id,tenant_id,user_id,mission_id,route_version,status,claim_set_hash,base_route_version,input_manifest,agent_profile_snapshot_id,ontology_snapshot_id,content_snapshot_id,planner_command_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,'generating',$6,$7,$8,$9,$10,$11,$12,$13,$13)`, command.RevisionID, command.TenantID, command.UserID, command.MissionID, revision.RouteVersion, revision.ClaimSetHash, revision.BaseRouteVersion, command.InputManifest, command.AgentProfileSnapshotID, command.OntologySnapshotID, command.ContentSnapshotID, command.PlannerCommandID, now); err != nil {
		return RouteResult{}, err
	}
	identifiers, err := store.routeEventIDs("route-generation-requested", command.MutationID)
	if err != nil {
		return RouteResult{}, err
	}
	input := eventpostgres.Input{Event: eventpostgres.Event{ID: identifiers.event, TenantID: command.TenantID, UserID: command.UserID, EventType: "RouteGenerationRequested", SchemaVersion: 1, AggregateKind: "route_revision", AggregateID: command.RevisionID, AggregateVersion: revision.Version, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID, PayloadRef: command.RequestedEvent.Ref, PayloadHash: command.RequestedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: identifiers.publishOutbox, CommandID: identifiers.publishCommand, CommandType: "events.publish", PayloadRef: command.RequestedEvent.Ref, PayloadHash: command.RequestedEvent.Hash}, {ID: identifiers.domainOutbox, CommandID: command.PlannerCommandID, CommandType: "RoutePlanningRequested", TargetAggregateKind: "mission", TargetAggregateID: command.MissionID, PayloadRef: command.RequestedEvent.Ref, PayloadHash: command.RequestedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, input); err != nil {
		return RouteResult{}, err
	}
	return RouteResult{RevisionID: revision.ID, MissionID: revision.MissionID, Status: string(revision.Status), RevisionVersion: revision.Version, MissionVersion: state.Mission.Version, RouteVersion: state.Mission.RouteVersion, CurrentRouteRevisionID: state.Mission.CurrentRouteRevisionID, PlannerCommandID: command.PlannerCommandID, EventID: identifiers.event}, nil
}

func (store RouteStore) CompleteGeneration(ctx context.Context, command CompleteRouteGenerationCommand) (RouteResult, error) {
	if !store.validRoute() || !validCompleteRouteCommand(command) {
		return RouteResult{}, ErrInvalidCommand
	}
	if err := store.requireRouteEpoch(ctx); err != nil {
		return RouteResult{}, err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RouteResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := store.CompleteGenerationInTx(ctx, tx, command)
	if err != nil && !errors.Is(err, ErrRouteResultStale) {
		return RouteResult{}, err
	}
	resultErr := err
	if err = tx.Commit(ctx); err != nil {
		return RouteResult{}, err
	}
	return result, resultErr
}

func (store RouteStore) CompleteGenerationInTx(ctx context.Context, tx pgx.Tx, command CompleteRouteGenerationCommand) (RouteResult, error) {
	if !store.validRoute() || tx == nil || !validCompleteRouteCommand(command) {
		return RouteResult{}, ErrInvalidCommand
	}
	missionID, err := store.routeMissionID(ctx, tx, command.TenantID, command.UserID, command.RevisionID)
	if err != nil {
		return RouteResult{}, err
	}
	state, err := store.lockRouteState(ctx, tx, command.TenantID, command.UserID, missionID)
	if err != nil {
		return RouteResult{}, err
	}
	next, modelErr := state.Complete(route.CompleteCommand{RevisionID: command.RevisionID, ExpectedRevisionVersion: command.ExpectedRevisionVersion, PayloadRef: command.RoutePayload.Ref, PayloadHash: command.RoutePayload.Hash})
	if modelErr != nil && !errors.Is(modelErr, route.ErrResultStale) {
		return RouteResult{}, mapRouteError(modelErr)
	}
	before, after := state.Revisions[command.RevisionID], next.Revisions[command.RevisionID]
	now := store.nowRoute()
	tag, err := tx.Exec(ctx, `UPDATE product.route_revisions SET version=$1,status=$2,route_payload_ref=$3,route_payload_hash=$4,stale_reason=NULLIF($5,''),updated_at=$6 WHERE tenant_id=$7 AND user_id=$8 AND id=$9 AND version=$10 AND status=$11 AND route_payload_ref IS NULL AND route_payload_hash IS NULL`, after.Version, after.Status, after.PayloadRef, after.PayloadHash, after.StaleReason, now, command.TenantID, command.UserID, command.RevisionID, before.Version, before.Status)
	if err != nil || tag.RowsAffected() != 1 {
		return RouteResult{}, ErrRouteConflict
	}
	identifiers, err := store.routeEventIDs("route-generation-completed", command.MutationID)
	if err != nil {
		return RouteResult{}, err
	}
	eventType := "RouteRevisionProposed"
	if after.Status == route.Stale {
		eventType = "RouteRevisionStale"
	}
	input := eventpostgres.Input{Event: eventpostgres.Event{ID: identifiers.event, TenantID: command.TenantID, UserID: command.UserID, EventType: eventType, SchemaVersion: 1, AggregateKind: "route_revision", AggregateID: command.RevisionID, AggregateVersion: after.Version, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID, PayloadRef: command.CompletedEvent.Ref, PayloadHash: command.CompletedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: identifiers.publishOutbox, CommandID: identifiers.publishCommand, CommandType: "events.publish", PayloadRef: command.CompletedEvent.Ref, PayloadHash: command.CompletedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, input); err != nil {
		return RouteResult{}, err
	}
	result := RouteResult{RevisionID: after.ID, MissionID: missionID, Status: string(after.Status), RevisionVersion: after.Version, MissionVersion: state.Mission.Version, RouteVersion: state.Mission.RouteVersion, CurrentRouteRevisionID: state.Mission.CurrentRouteRevisionID, EventID: identifiers.event, Stale: after.Status == route.Stale}
	if result.Stale {
		return result, ErrRouteResultStale
	}
	return result, nil
}

func (store RouteStore) FailGeneration(ctx context.Context, command FailRouteGenerationCommand) (RouteResult, error) {
	if !store.validRoute() || !validFailRouteCommand(command) {
		return RouteResult{}, ErrInvalidCommand
	}
	if err := store.requireRouteEpoch(ctx); err != nil {
		return RouteResult{}, err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RouteResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := store.FailGenerationInTx(ctx, tx, command)
	if err != nil {
		return RouteResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RouteResult{}, err
	}
	return result, nil
}

func (store RouteStore) FailGenerationInTx(ctx context.Context, tx pgx.Tx, command FailRouteGenerationCommand) (RouteResult, error) {
	if !store.validRoute() || tx == nil || !validFailRouteCommand(command) {
		return RouteResult{}, ErrInvalidCommand
	}
	missionID, err := store.routeMissionID(ctx, tx, command.TenantID, command.UserID, command.RevisionID)
	if err != nil {
		return RouteResult{}, err
	}
	state, err := store.lockRouteState(ctx, tx, command.TenantID, command.UserID, missionID)
	if err != nil {
		return RouteResult{}, err
	}
	next, err := state.Fail(route.FailCommand{RevisionID: command.RevisionID, ExpectedRevisionVersion: command.ExpectedRevisionVersion, Reason: command.Reason})
	if err != nil {
		return RouteResult{}, mapRouteError(err)
	}
	before, after := state.Revisions[command.RevisionID], next.Revisions[command.RevisionID]
	now := store.nowRoute()
	tag, err := tx.Exec(ctx, `UPDATE product.route_revisions SET version=$1,status='failed',failure_reason=$2,updated_at=$3 WHERE tenant_id=$4 AND user_id=$5 AND id=$6 AND version=$7 AND status='generating' AND route_payload_ref IS NULL AND route_payload_hash IS NULL AND failure_reason IS NULL`, after.Version, after.FailureReason, now, command.TenantID, command.UserID, command.RevisionID, before.Version)
	if err != nil || tag.RowsAffected() != 1 {
		return RouteResult{}, ErrRouteConflict
	}
	identifiers, err := store.routeEventIDs("route-generation-failed", command.MutationID)
	if err != nil {
		return RouteResult{}, err
	}
	input := eventpostgres.Input{Event: eventpostgres.Event{ID: identifiers.event, TenantID: command.TenantID, UserID: command.UserID, EventType: "RouteRevisionFailed", SchemaVersion: 1, AggregateKind: "route_revision", AggregateID: command.RevisionID, AggregateVersion: after.Version, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID, PayloadRef: command.FailedEvent.Ref, PayloadHash: command.FailedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: identifiers.publishOutbox, CommandID: identifiers.publishCommand, CommandType: "events.publish", PayloadRef: command.FailedEvent.Ref, PayloadHash: command.FailedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, input); err != nil {
		return RouteResult{}, err
	}
	return RouteResult{RevisionID: after.ID, MissionID: missionID, Status: string(after.Status), RevisionVersion: after.Version, MissionVersion: state.Mission.Version, RouteVersion: state.Mission.RouteVersion, CurrentRouteRevisionID: state.Mission.CurrentRouteRevisionID, EventID: identifiers.event}, nil
}

func (store RouteStore) Accept(ctx context.Context, command AcceptRouteCommand) (RouteResult, error) {
	if !store.validRoute() || !validAcceptRouteCommand(command) {
		return RouteResult{}, ErrInvalidCommand
	}
	if err := store.requireRouteEpoch(ctx); err != nil {
		return RouteResult{}, err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RouteResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := store.AcceptInTx(ctx, tx, command)
	if err != nil {
		return RouteResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RouteResult{}, err
	}
	return result, nil
}

func (store RouteStore) AcceptInTx(ctx context.Context, tx pgx.Tx, command AcceptRouteCommand) (RouteResult, error) {
	if !store.validRoute() || tx == nil || !validAcceptRouteCommand(command) {
		return RouteResult{}, ErrInvalidCommand
	}
	missionID, err := store.routeMissionID(ctx, tx, command.TenantID, command.UserID, command.RevisionID)
	if err != nil {
		return RouteResult{}, err
	}
	state, err := store.lockRouteState(ctx, tx, command.TenantID, command.UserID, missionID)
	if err != nil {
		return RouteResult{}, err
	}
	next, err := state.Accept(route.AcceptCommand{RevisionID: command.RevisionID, ExpectedRevisionVersion: command.ExpectedRevisionVersion, ExpectedRouteVersion: command.ExpectedRouteVersion, ExpectedClaimSetHash: command.ExpectedClaimSetHash})
	if err != nil {
		return RouteResult{}, mapRouteError(err)
	}
	now := store.nowRoute()
	beforeCandidate, afterCandidate := state.Revisions[command.RevisionID], next.Revisions[command.RevisionID]
	tag, err := tx.Exec(ctx, `UPDATE product.route_revisions SET version=$1,status='accepted',accepted_at=$2,updated_at=$2 WHERE tenant_id=$3 AND user_id=$4 AND id=$5 AND version=$6 AND status='proposed'`, afterCandidate.Version, now, command.TenantID, command.UserID, command.RevisionID, beforeCandidate.Version)
	if err != nil || tag.RowsAffected() != 1 {
		return RouteResult{}, ErrRouteConflict
	}
	if previousID := state.Mission.CurrentRouteRevisionID; previousID != "" {
		beforePrevious, afterPrevious := state.Revisions[previousID], next.Revisions[previousID]
		tag, err = tx.Exec(ctx, `UPDATE product.route_revisions SET version=$1,status='superseded',updated_at=$2 WHERE tenant_id=$3 AND user_id=$4 AND id=$5 AND version=$6 AND status=$7`, afterPrevious.Version, now, command.TenantID, command.UserID, previousID, beforePrevious.Version, beforePrevious.Status)
		if err != nil || tag.RowsAffected() != 1 {
			return RouteResult{}, ErrRouteConflict
		}
	}
	tag, err = tx.Exec(ctx, `UPDATE product.missions SET version=$1,route_version=$2,current_route_revision_id=$3,updated_at=$4 WHERE tenant_id=$5 AND user_id=$6 AND id=$7 AND version=$8 AND route_version=$9 AND claim_set_hash=$10 AND current_route_revision_id IS NOT DISTINCT FROM NULLIF($11,'')::uuid`, next.Mission.Version, next.Mission.RouteVersion, command.RevisionID, now, command.TenantID, command.UserID, missionID, state.Mission.Version, state.Mission.RouteVersion, state.Mission.ClaimSetHash, state.Mission.CurrentRouteRevisionID)
	if err != nil || tag.RowsAffected() != 1 {
		return RouteResult{}, ErrRouteConflict
	}
	identifiers, err := store.routeEventIDs("route-accepted", command.MutationID)
	if err != nil {
		return RouteResult{}, err
	}
	commands := []eventpostgres.OutboxCommand{{ID: identifiers.publishOutbox, CommandID: identifiers.publishCommand, CommandType: "events.publish", PayloadRef: command.AcceptedEvent.Ref, PayloadHash: command.AcceptedEvent.Hash}}
	if command.DailyTaskCommandID != "" {
		commands = append(commands, eventpostgres.OutboxCommand{ID: identifiers.domainOutbox, CommandID: command.DailyTaskCommandID, CommandType: "GenerateDailyTask", TargetAggregateKind: "mission", TargetAggregateID: missionID, PayloadRef: command.DailyTaskCommand.Ref, PayloadHash: command.DailyTaskCommand.Hash})
	}
	input := eventpostgres.Input{Event: eventpostgres.Event{ID: identifiers.event, TenantID: command.TenantID, UserID: command.UserID, EventType: "RouteAccepted", SchemaVersion: 1, AggregateKind: "mission", AggregateID: missionID, AggregateVersion: next.Mission.Version, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID, PayloadRef: command.AcceptedEvent.Ref, PayloadHash: command.AcceptedEvent.Hash}, Commands: commands}
	if _, err = store.Appender.Append(ctx, tx, input); err != nil {
		return RouteResult{}, err
	}
	return RouteResult{RevisionID: afterCandidate.ID, MissionID: missionID, Status: string(afterCandidate.Status), RevisionVersion: afterCandidate.Version, MissionVersion: next.Mission.Version, RouteVersion: next.Mission.RouteVersion, CurrentRouteRevisionID: next.Mission.CurrentRouteRevisionID, EventID: identifiers.event}, nil
}

func (store RouteStore) lockRouteState(ctx context.Context, tx pgx.Tx, tenantID, userID, missionID string) (route.State, error) {
	if _, err := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return route.State{}, err
	}
	var missionState route.Mission
	var current *string
	err := tx.QueryRow(ctx, `SELECT id::text,version,route_version,claim_set_hash,current_route_revision_id::text FROM product.missions WHERE tenant_id=$1 AND user_id=$2 AND id=$3 FOR UPDATE`, tenantID, userID, missionID).Scan(&missionState.ID, &missionState.Version, &missionState.RouteVersion, &missionState.ClaimSetHash, &current)
	if errors.Is(err, pgx.ErrNoRows) {
		return route.State{}, ErrRouteNotFound
	}
	if err != nil {
		return route.State{}, err
	}
	if current != nil {
		missionState.CurrentRouteRevisionID = *current
	}
	state := route.State{Mission: missionState, Revisions: map[string]route.Revision{}}
	rows, err := tx.Query(ctx, `SELECT id::text,version,mission_id::text,route_version,status,claim_set_hash,base_route_version,COALESCE(route_payload_ref,''),COALESCE(route_payload_hash,''),COALESCE(stale_reason,''),COALESCE(failure_reason,'') FROM product.route_revisions WHERE tenant_id=$1 AND user_id=$2 AND mission_id=$3 ORDER BY id FOR UPDATE`, tenantID, userID, missionID)
	if err != nil {
		return route.State{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var revision route.Revision
		if err = rows.Scan(&revision.ID, &revision.Version, &revision.MissionID, &revision.RouteVersion, &revision.Status, &revision.ClaimSetHash, &revision.BaseRouteVersion, &revision.PayloadRef, &revision.PayloadHash, &revision.StaleReason, &revision.FailureReason); err != nil {
			return route.State{}, err
		}
		state.Revisions[revision.ID] = revision
	}
	if err = rows.Err(); err != nil {
		return route.State{}, err
	}
	return state, nil
}

func (store RouteStore) routeMissionID(ctx context.Context, tx pgx.Tx, tenantID, userID, revisionID string) (string, error) {
	if _, err := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return "", err
	}
	var missionID string
	if err := tx.QueryRow(ctx, `SELECT mission_id::text FROM product.route_revisions WHERE tenant_id=$1 AND user_id=$2 AND id=$3`, tenantID, userID, revisionID).Scan(&missionID); errors.Is(err, pgx.ErrNoRows) {
		return "", ErrRouteNotFound
	} else if err != nil {
		return "", err
	}
	return missionID, nil
}

type routeIdentifiers struct{ event, publishOutbox, publishCommand, domainOutbox string }

func (store RouteStore) routeEventIDs(domain, seed string) (routeIdentifiers, error) {
	values := make([]string, 4)
	for index, part := range []string{"event", "publish-outbox", "publish-command", "domain-outbox"} {
		value, err := ids.DeterministicUUID(store.IDKey, domain+":"+part, seed)
		if err != nil {
			return routeIdentifiers{}, err
		}
		values[index] = value
	}
	return routeIdentifiers{values[0], values[1], values[2], values[3]}, nil
}

func (store RouteStore) validRoute() bool {
	return store.Pool != nil && store.Epochs != nil && store.StoreEpoch != "" && len(store.IDKey) >= 32
}

func (store RouteStore) requireRouteEpoch(ctx context.Context) error {
	current, err := store.Epochs.CurrentStoreEpoch(ctx)
	if err != nil || current == "" {
		return ErrConfiguration
	}
	if current != store.StoreEpoch {
		return ErrStaleEpoch
	}
	return nil
}

func (store RouteStore) nowRoute() time.Time {
	now := time.Now().UTC()
	if store.Now != nil {
		now = store.Now().UTC()
	}
	return now.Truncate(time.Microsecond)
}

func validBeginRouteCommand(command BeginRouteGenerationCommand) bool {
	return command.MutationID != "" && command.RevisionID != "" && command.TenantID != "" && command.UserID != "" && command.MissionID != "" && command.ExpectedClaimSetHash != "" && command.PlannerCommandID != "" && validJSONObject(command.InputManifest) && command.AgentProfileSnapshotID != "" && command.OntologySnapshotID != "" && command.ContentSnapshotID != "" && command.CorrelationID != "" && validJSONObject(command.Actor) && validPointer(command.RequestedEvent)
}

func validCompleteRouteCommand(command CompleteRouteGenerationCommand) bool {
	return command.MutationID != "" && command.RevisionID != "" && command.TenantID != "" && command.UserID != "" && command.ExpectedRevisionVersion > 0 && validPointer(command.RoutePayload) && validPointer(command.CompletedEvent) && command.CorrelationID != "" && validJSONObject(command.Actor)
}

func validFailRouteCommand(command FailRouteGenerationCommand) bool {
	return command.MutationID != "" && command.RevisionID != "" && command.TenantID != "" && command.UserID != "" && command.ExpectedRevisionVersion > 0 && command.Reason != "" && len(command.Reason) <= 128 && command.CorrelationID != "" && validJSONObject(command.Actor) && validPointer(command.FailedEvent)
}

func validAcceptRouteCommand(command AcceptRouteCommand) bool {
	dailyValid := command.DailyTaskCommandID == "" && command.DailyTaskCommand.Ref == "" && command.DailyTaskCommand.Hash == "" || command.DailyTaskCommandID != "" && validPointer(command.DailyTaskCommand)
	return command.MutationID != "" && command.RevisionID != "" && command.TenantID != "" && command.UserID != "" && command.ExpectedRevisionVersion > 0 && command.ExpectedClaimSetHash != "" && command.CorrelationID != "" && validJSONObject(command.Actor) && validPointer(command.AcceptedEvent) && dailyValid
}

func mapRouteError(err error) error {
	switch {
	case errors.Is(err, route.ErrRevisionMissing):
		return ErrRouteNotFound
	case errors.Is(err, route.ErrResultStale):
		return ErrRouteResultStale
	case errors.Is(err, route.ErrConflict):
		return ErrRouteConflict
	case errors.Is(err, route.ErrInvalid):
		return ErrInvalidCommand
	default:
		return err
	}
}
