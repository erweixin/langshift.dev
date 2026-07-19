package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/behavior"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

const defaultRoutePlannerConsumer = "product-route-planner"

const routePlannerOutputContract = `Return exactly one JSON object and no markdown fences. Required closed shape: {"schema_version":1,"summary":"string","transferable_experience":[{"statement":"string","capability_ids":["string"],"evidence_ids":["string"],"confidence":"inferred|supported|verified"}],"gaps":[same assessment shape],"bridge":[{"id":"lowercase_id","title":"string","rationale":"string","from_capability_ids":["string"],"to_capability_ids":["string"]}],"stages":[{"id":"lowercase_id","title":"string","outcome":"string","capability_ids":["string"],"evidence_required":["string"]}],"first_task":{"title":"string","objective":"string","estimated_minutes":5..480,"difficulty":"easy|standard|stretch","capability_ids":["string"],"success_criteria":["string"]}}. Include 1..16 bridge items and 2..12 stages. Use only capability IDs from current active claim revisions or target_requirements, and only evidence IDs from current non-invalidated evidence revisions. supported and verified assessments must cite evidence linked to the current claim revision; verified also requires verified evidence and a demonstrated, applied, or reviewer_verified claim. Never present inferred capability as verified.`

var ErrRoutePlannerCommand = errors.New("route planner command is invalid")

type RoutePlannerService struct {
	Pool                 *pgxpool.Pool
	Routes               RouteStore
	Runs                 executionpostgres.RunStore
	Payloads             payload.Store
	IDKey                []byte
	RunTimeout           time.Duration
	RunMaxSteps          int
	RunMaxCostMicrounits int64
	RunMaxAttempts       int
	QueuePriority        int
	Now                  func() time.Time
}

type RoutePlannerDispatcher struct {
	Service      RoutePlannerService
	Inbox        eventpostgres.InboxStore
	ConsumerName string
}

type RoutePlannerStartResult struct {
	RevisionID, RunID, ConversationID string
	Replayed                          bool
}

type RoutePlannerDispatchResult struct {
	Claimed, Completed, Replayed bool
	Start                        RoutePlannerStartResult
}

type plannerRouteRecord struct {
	RevisionID, UserID, Status string
	Version                    uint64
	InputManifest              json.RawMessage
	PlannerRunID               *string
}

type plannerIdentifiers struct {
	conversationID, messageID, runID, startCommandID string
	correlationID                                    string
}

func (dispatcher RoutePlannerDispatcher) Dispatch(ctx context.Context, command eventpostgres.DeliveredCommand) (RoutePlannerDispatchResult, error) {
	if !dispatcher.Service.valid() || command.CommandType != "RoutePlanningRequested" || command.AggregateKind != "mission" || command.AggregateID == "" || command.StoreEpoch != dispatcher.Service.Routes.StoreEpoch {
		return RoutePlannerDispatchResult{}, ErrRoutePlannerCommand
	}
	consumer := dispatcher.ConsumerName
	if consumer == "" {
		consumer = defaultRoutePlannerConsumer
	}
	claim, err := dispatcher.Inbox.Claim(ctx, consumer, command)
	if err != nil {
		return RoutePlannerDispatchResult{}, err
	}
	if claim.Completed {
		return RoutePlannerDispatchResult{Completed: true, Replayed: true}, nil
	}
	result := RoutePlannerDispatchResult{Claimed: true}
	result.Start, err = dispatcher.Service.StartDelivery(ctx, dispatcher.Inbox, claim)
	if err != nil {
		if abandonErr := dispatcher.Inbox.Abandon(ctx, claim); abandonErr != nil && !errors.Is(abandonErr, eventpostgres.ErrDeliveryConflict) {
			return result, abandonErr
		}
		return result, err
	}
	result.Completed = true
	result.Replayed = result.Start.Replayed
	return result, nil
}

// StartDelivery commits inbox completion, planner Conversation/Message/Run,
// and the RouteRevision -> Run link atomically. Payload ciphertext may be
// staged before the transaction; it remains unreachable garbage if the
// durable commit fails and is safe for lifecycle cleanup.
func (service RoutePlannerService) StartDelivery(ctx context.Context, inbox eventpostgres.InboxStore, claim eventpostgres.InboxClaim) (RoutePlannerStartResult, error) {
	if !service.valid() || claim.Completed || claim.Busy || claim.Command.CommandType != "RoutePlanningRequested" || claim.Command.AggregateKind != "mission" || claim.Command.StoreEpoch != service.Routes.StoreEpoch {
		return RoutePlannerStartResult{}, ErrRoutePlannerCommand
	}
	if err := service.Routes.requireRouteEpoch(ctx); err != nil {
		return RoutePlannerStartResult{}, err
	}
	identifiers, err := service.identifiers(claim.Command.CommandID)
	if err != nil {
		return RoutePlannerStartResult{}, err
	}
	record, manifest, err := service.loadRoute(ctx, claim.Command)
	if err != nil {
		return RoutePlannerStartResult{}, err
	}
	prepared, err := service.prepareRunPayloads(ctx, claim.Command, record, manifest, identifiers)
	if err != nil {
		return RoutePlannerStartResult{}, err
	}
	now := service.now()
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RoutePlannerStartResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claim.Command.TenantID); err != nil {
		return RoutePlannerStartResult{}, err
	}
	if err = inbox.LockClaimTx(ctx, tx, claim, now); err != nil {
		return RoutePlannerStartResult{}, err
	}
	locked, lockedManifest, err := service.lockRoute(ctx, tx, claim.Command)
	if err != nil {
		return RoutePlannerStartResult{}, err
	}
	if !bytes.Equal(locked.InputManifest, record.InputManifest) || !sameRouteBehavior(lockedManifest.AgentProfile, manifest.AgentProfile) {
		return RoutePlannerStartResult{}, ErrRouteConflict
	}
	if locked.PlannerRunID != nil {
		if *locked.PlannerRunID != identifiers.runID {
			return RoutePlannerStartResult{}, ErrRouteConflict
		}
		if err = inbox.CompleteTx(ctx, tx, claim, now); err != nil {
			return RoutePlannerStartResult{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return RoutePlannerStartResult{}, err
		}
		return RoutePlannerStartResult{RevisionID: locked.RevisionID, RunID: identifiers.runID, ConversationID: identifiers.conversationID, Replayed: true}, nil
	}
	title := "Route planner"
	conversation, err := service.Runs.CreatePlannerConversationInTx(ctx, tx, executionpostgres.CreateConversationCommand{
		ConversationID: identifiers.conversationID, TenantID: claim.Command.TenantID, UserID: locked.UserID,
		MissionID: claim.Command.AggregateID, Title: &title, Mode: "project", CorrelationID: identifiers.correlationID,
		Actor: service.actor(), CreatedEvent: prepared.conversationEvent,
	})
	if err != nil {
		return RoutePlannerStartResult{}, err
	}
	budget := json.RawMessage(fmt.Sprintf(`{"max_steps":%d,"max_cost_microunits":%d}`, service.RunMaxSteps, service.RunMaxCostMicrounits))
	accepted, err := service.Runs.AcceptMessageRunInTx(ctx, tx, executionpostgres.AcceptMessageRunCommand{
		Run: executionpostgres.AcceptRunCommand{
			RunID: identifiers.runID, TenantID: claim.Command.TenantID, UserID: locked.UserID,
			ConversationID: identifiers.conversationID, CorrelationID: identifiers.correlationID, DueAt: now.Add(service.RunTimeout),
			BehaviorProfile: behavior.RoutePlanner, BehaviorEnvironment: manifest.AgentProfile.Environment,
			ExpectedProfileSnapshotID: manifest.AgentProfile.SnapshotID, ExpectedBehaviorChannelID: manifest.AgentProfile.ChannelID,
			ExpectedBehaviorSequence: manifest.AgentProfile.Sequence, PinnedBehaviorBinding: true,
			BudgetSnapshot: budget, Actor: service.actor(), AcceptedEvent: prepared.acceptedEvent,
			QueuedEvent: prepared.queuedEvent, StartCommand: prepared.startCommand, QueueClass: "background",
			ResourceClass: "llm", Priority: service.priority(), CostUnits: int64(service.RunMaxSteps), MaxAttempts: service.RunMaxAttempts,
		},
		MessageID: identifiers.messageID, ExpectedConversationVersion: conversation.Version, ExpectedConversationMode: "project", ExpectedConversationProfile: behavior.RoutePlanner,
		Message: prepared.message, ContentHash: prepared.messageContentHash, AppendedEvent: prepared.messageEvent, Actor: service.actor(),
	})
	if err != nil {
		return RoutePlannerStartResult{}, err
	}
	if accepted.RunID != identifiers.runID || accepted.ProfileSnapshotID != manifest.AgentProfile.SnapshotID || accepted.BehaviorChannelID != manifest.AgentProfile.ChannelID || accepted.BehaviorChannelSequence != manifest.AgentProfile.Sequence {
		return RoutePlannerStartResult{}, ErrRouteConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE product.route_revisions SET planner_run_id=$1,updated_at=$2 WHERE tenant_id=$3 AND user_id=$4 AND id=$5 AND planner_command_id=$6 AND planner_run_id IS NULL AND status='generating' AND version=$7`, identifiers.runID, now, claim.Command.TenantID, locked.UserID, locked.RevisionID, claim.Command.CommandID, locked.Version)
	if err != nil || tag.RowsAffected() != 1 {
		return RoutePlannerStartResult{}, ErrRouteConflict
	}
	if err = inbox.CompleteTx(ctx, tx, claim, now); err != nil {
		return RoutePlannerStartResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RoutePlannerStartResult{}, err
	}
	return RoutePlannerStartResult{RevisionID: locked.RevisionID, RunID: identifiers.runID, ConversationID: identifiers.conversationID}, nil
}

type plannerPayloads struct {
	conversationEvent, message, messageEvent executionpostgres.PayloadPointer
	acceptedEvent, queuedEvent, startCommand executionpostgres.PayloadPointer
	messageContentHash                       string
}

func (service RoutePlannerService) prepareRunPayloads(ctx context.Context, command eventpostgres.DeliveredCommand, record plannerRouteRecord, manifest routeInputManifest, identifiers plannerIdentifiers) (plannerPayloads, error) {
	promptText, err := service.routePlannerPromptText(ctx, command.TenantID, record, manifest)
	if err != nil {
		return plannerPayloads{}, err
	}
	prompt := map[string]any{
		"schema_version": 1, "role": "user", "content": []map[string]any{{"type": "text", "text": promptText}},
	}
	messageJSON, err := json.Marshal(prompt)
	if err != nil {
		return plannerPayloads{}, err
	}
	digest := sha256.Sum256(messageJSON)
	put := func(objectID, class string, value any) (executionpostgres.PayloadPointer, error) {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return executionpostgres.PayloadPointer{}, marshalErr
		}
		stored, putErr := service.Payloads.Put(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: objectID, Class: class, ContentType: "application/json"}, encoded)
		return executionpostgres.PayloadPointer{Ref: stored.Ref, Hash: stored.Hash}, putErr
	}
	putRaw := func(objectID, class string, encoded []byte) (executionpostgres.PayloadPointer, error) {
		stored, putErr := service.Payloads.Put(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: objectID, Class: class, ContentType: "application/json"}, encoded)
		return executionpostgres.PayloadPointer{Ref: stored.Ref, Hash: stored.Hash}, putErr
	}
	var result plannerPayloads
	if result.conversationEvent, err = put(identifiers.conversationID, "event-payload", map[string]any{"subject_id": identifiers.conversationID, "subject_version": 1, "mission_id": command.AggregateID, "purpose": "route_planner"}); err != nil {
		return plannerPayloads{}, err
	}
	if result.message, err = putRaw(identifiers.messageID, "run-message", messageJSON); err != nil {
		return plannerPayloads{}, err
	}
	result.messageContentHash = hex.EncodeToString(digest[:])
	if result.messageEvent, err = put(identifiers.messageID, "event-payload", map[string]any{"subject_id": identifiers.conversationID, "subject_version": 2, "message_id": identifiers.messageID, "route_revision_id": record.RevisionID}); err != nil {
		return plannerPayloads{}, err
	}
	behaviorBinding := map[string]any{"profile": manifest.AgentProfile.Profile, "environment": manifest.AgentProfile.Environment, "snapshot_id": manifest.AgentProfile.SnapshotID, "channel_id": manifest.AgentProfile.ChannelID, "sequence": manifest.AgentProfile.Sequence}
	if result.acceptedEvent, err = put(identifiers.runID, "event-payload", map[string]any{"subject_id": identifiers.runID, "subject_version": 1, "run_id": identifiers.runID, "route_revision_id": record.RevisionID, "behavior": behaviorBinding}); err != nil {
		return plannerPayloads{}, err
	}
	if result.queuedEvent, err = put(identifiers.startCommandID, "event-payload", map[string]any{"subject_id": identifiers.runID, "subject_version": 2, "run_id": identifiers.runID, "pending_command_id": identifiers.startCommandID}); err != nil {
		return plannerPayloads{}, err
	}
	result.startCommand, err = put(identifiers.startCommandID, "agent-run-command", map[string]any{"schema_version": 1, "run_id": identifiers.runID, "correlation_id": identifiers.correlationID})
	return result, err
}

func (service RoutePlannerService) routePlannerPromptText(ctx context.Context, tenantID string, record plannerRouteRecord, manifest routeInputManifest) (string, error) {
	text := "Generate a production route revision from this immutable input manifest. " + routePlannerOutputContract + "\nINPUT_MANIFEST=" + string(record.InputManifest)
	if manifest.Onboarding == nil {
		return text, nil
	}
	intake, err := service.Payloads.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: manifest.Onboarding.SessionID, Class: "onboarding-body", ContentType: "application/json"}, manifest.Onboarding.ExperiencePayload)
	if err != nil || !validJSONObject(intake) {
		return "", errors.Join(payload.ErrIntegrity, err)
	}
	return text + "\nONBOARDING_INTAKE=" + string(intake), nil
}

func (service RoutePlannerService) loadRoute(ctx context.Context, command eventpostgres.DeliveredCommand) (plannerRouteRecord, routeInputManifest, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return plannerRouteRecord{}, routeInputManifest{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return plannerRouteRecord{}, routeInputManifest{}, err
	}
	record, manifest, err := service.queryRoute(ctx, tx, command, false)
	if err != nil {
		return plannerRouteRecord{}, routeInputManifest{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return plannerRouteRecord{}, routeInputManifest{}, err
	}
	return record, manifest, nil
}

func (service RoutePlannerService) lockRoute(ctx context.Context, tx pgx.Tx, command eventpostgres.DeliveredCommand) (plannerRouteRecord, routeInputManifest, error) {
	return service.queryRoute(ctx, tx, command, true)
}

func (service RoutePlannerService) queryRoute(ctx context.Context, tx pgx.Tx, command eventpostgres.DeliveredCommand, lock bool) (plannerRouteRecord, routeInputManifest, error) {
	query := `SELECT id::text,user_id::text,version,status,input_manifest,planner_run_id::text FROM product.route_revisions WHERE tenant_id=$1 AND mission_id=$2 AND planner_command_id=$3`
	if lock {
		query += ` FOR UPDATE`
	}
	var record plannerRouteRecord
	err := tx.QueryRow(ctx, query, command.TenantID, command.AggregateID, command.CommandID).Scan(&record.RevisionID, &record.UserID, &record.Version, &record.Status, &record.InputManifest, &record.PlannerRunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return plannerRouteRecord{}, routeInputManifest{}, ErrRouteNotFound
	}
	if err != nil {
		return plannerRouteRecord{}, routeInputManifest{}, err
	}
	manifest, err := decodeRouteInputManifest(record.InputManifest)
	if err != nil || record.Status != "generating" || record.Version != 1 {
		return plannerRouteRecord{}, routeInputManifest{}, ErrRoutePlannerCommand
	}
	return record, manifest, nil
}

func decodeRouteInputManifest(encoded []byte) (routeInputManifest, error) {
	var manifest routeInputManifest
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || manifest.SchemaVersion != 2 && manifest.SchemaVersion != 3 && manifest.SchemaVersion != 4 || manifest.AgentProfile.Profile != string(behavior.RoutePlanner) || manifest.AgentProfile.Environment != "staging" && manifest.AgentProfile.Environment != "production" || manifest.AgentProfile.ChannelID == "" || manifest.AgentProfile.Sequence == 0 || manifest.AgentProfile.SnapshotID == "" || manifest.AgentProfileSnapshotID != manifest.AgentProfile.SnapshotID || manifest.Mission.ID == "" || manifest.Mission.ClaimSetHash == "" || manifest.OntologySnapshotID == "" || manifest.ContentSnapshotID == "" || manifest.Onboarding != nil && (manifest.Onboarding.SessionID == "" || manifest.Onboarding.Version < 1 || manifest.Onboarding.ExperiencePayload.Ref == "" || manifest.Onboarding.ExperiencePayload.Hash == "") {
		return routeInputManifest{}, ErrRoutePlannerCommand
	}
	return manifest, nil
}

func sameRouteBehavior(left, right routeBehaviorBinding) bool {
	return left.Profile == right.Profile && left.Environment == right.Environment && left.ChannelID == right.ChannelID && left.Sequence == right.Sequence && left.SnapshotID == right.SnapshotID && left.ActivatedAt.Equal(right.ActivatedAt)
}

func (service RoutePlannerService) identifiers(commandID string) (plannerIdentifiers, error) {
	domains := []string{"route-planner-conversation", "route-planner-message", "route-planner-run", "route-planner-correlation"}
	values := make([]string, len(domains))
	for index, domain := range domains {
		value, err := ids.DeterministicUUID(service.IDKey, domain, commandID)
		if err != nil {
			return plannerIdentifiers{}, err
		}
		values[index] = value
	}
	startCommand, err := executionpostgres.RunStartCommandID(service.Runs.IDKey, values[2])
	if err != nil {
		return plannerIdentifiers{}, err
	}
	return plannerIdentifiers{values[0], values[1], values[2], startCommand, values[3]}, nil
}

func (service RoutePlannerService) actor() json.RawMessage {
	return json.RawMessage(`{"kind":"service","name":"product-route-planner"}`)
}

func (service RoutePlannerService) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func (service RoutePlannerService) priority() int {
	if service.QueuePriority == 0 {
		return 100
	}
	return service.QueuePriority
}

func (service RoutePlannerService) valid() bool {
	return service.Pool != nil && service.Routes.Pool == service.Pool && service.Runs.Pool == service.Pool && service.Payloads != nil && len(service.IDKey) >= 32 && service.RunTimeout > 0 && service.RunMaxSteps > 0 && service.RunMaxCostMicrounits > 0 && service.RunMaxAttempts > 0 && service.priority() >= 0 && service.priority() <= 1000
}
