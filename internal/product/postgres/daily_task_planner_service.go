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

const defaultDailyTaskPlannerConsumer = "product-daily-task-planner"

const dailyTaskOutputContract = `Return exactly one JSON object and no markdown fences. Required closed shape: {"schema_version":1,"title":"string","objective":"string","why_this_task":"string","key_judgment":"string","estimated_minutes":5..480,"difficulty":"easier|standard|harder","practice_kind":"code|writing|design","explanation":[{"title":"string","content":"string"}],"example":"string","practice":{"instructions":"string","starter_content":"string","deterministic_checks":["string"],"success_criteria":["string"]},"capability_ids":["string"],"evidence_targets":["string"],"next_task_hint":"string"}. The estimated_minutes and difficulty must exactly match the frozen request. Capability IDs must come from the accepted route.`

var (
	ErrDailyTaskCommand  = errors.New("daily task planner command is invalid")
	ErrDailyTaskObsolete = errors.New("daily task planner command is obsolete")
)

type DailyTaskPlannerService struct {
	Pool                 *pgxpool.Pool
	Routes               RouteStore
	Runs                 executionpostgres.RunStore
	Payloads             payload.Store
	IDKey                []byte
	BehaviorEnvironment  string
	ContentSnapshotID    string
	RunTimeout           time.Duration
	RunMaxSteps          int
	RunMaxCostMicrounits int64
	RunMaxAttempts       int
	QueuePriority        int
	Now                  func() time.Time
}

type DailyTaskPlannerDispatcher struct {
	Service      DailyTaskPlannerService
	Inbox        eventpostgres.InboxStore
	ConsumerName string
}

type DailyTaskPlannerDispatchResult struct {
	Claimed, Completed, Replayed, Superseded bool
	GenerationID, RunID, ConversationID      string
}

type dailyTaskInputManifest struct {
	SchemaVersion     int                      `json:"schema_version"`
	Command           dailyTaskCommandDocument `json:"command"`
	MissionVersion    uint64                   `json:"mission_version"`
	RouteVersion      uint64                   `json:"route_version"`
	RouteRowVersion   uint64                   `json:"route_row_version"`
	RoutePayloadRef   string                   `json:"route_payload_ref"`
	RoutePayloadHash  string                   `json:"route_payload_hash"`
	EvidenceRevisions []routeEvidenceRevision  `json:"evidence_revisions"`
	AgentProfile      routeBehaviorBinding     `json:"agent_profile"`
	ContentSnapshotID string                   `json:"content_snapshot_id"`
}

type dailyTaskSnapshot struct {
	Manifest     dailyTaskInputManifest
	ManifestJSON json.RawMessage
	RouteJSON    json.RawMessage
}

type dailyTaskIdentifiers struct {
	generationID, conversationID, messageID, runID, startCommandID, correlationID string
}

func (dispatcher DailyTaskPlannerDispatcher) Dispatch(ctx context.Context, command eventpostgres.DeliveredCommand) (DailyTaskPlannerDispatchResult, error) {
	if !dispatcher.Service.valid() || command.CommandType != "GenerateDailyTask" || command.AggregateKind != "mission" || command.AggregateID == "" || command.StoreEpoch != dispatcher.Service.Routes.StoreEpoch {
		return DailyTaskPlannerDispatchResult{}, ErrDailyTaskCommand
	}
	consumer := dispatcher.ConsumerName
	if consumer == "" {
		consumer = defaultDailyTaskPlannerConsumer
	}
	claim, err := dispatcher.Inbox.Claim(ctx, consumer, command)
	if err != nil {
		return DailyTaskPlannerDispatchResult{}, err
	}
	if claim.Completed {
		return DailyTaskPlannerDispatchResult{Completed: true, Replayed: true}, nil
	}
	result := DailyTaskPlannerDispatchResult{Claimed: true}
	result.GenerationID, result.RunID, result.ConversationID, result.Superseded, err = dispatcher.Service.StartDelivery(ctx, dispatcher.Inbox, claim)
	if err != nil {
		if abandonErr := dispatcher.Inbox.Abandon(ctx, claim); abandonErr != nil && !errors.Is(abandonErr, eventpostgres.ErrDeliveryConflict) {
			return result, abandonErr
		}
		return result, err
	}
	result.Completed = true
	return result, nil
}

func (service DailyTaskPlannerService) StartDelivery(ctx context.Context, inbox eventpostgres.InboxStore, claim eventpostgres.InboxClaim) (string, string, string, bool, error) {
	if !service.valid() || claim.Completed || claim.Busy || claim.Command.CommandType != "GenerateDailyTask" || claim.Command.AggregateKind != "mission" || claim.Command.StoreEpoch != service.Routes.StoreEpoch {
		return "", "", "", false, ErrDailyTaskCommand
	}
	if err := service.Routes.requireRouteEpoch(ctx); err != nil {
		return "", "", "", false, err
	}
	document, err := service.loadCommand(ctx, claim.Command)
	if err != nil {
		return "", "", "", false, err
	}
	identifiers, err := service.identifiers(claim.Command.CommandID)
	if err != nil {
		return "", "", "", false, err
	}
	snapshot, obsolete, err := service.snapshot(ctx, claim.Command.TenantID, document)
	if err != nil {
		return "", "", "", false, err
	}
	if obsolete {
		if err = service.completeSuperseded(ctx, inbox, claim, identifiers, document, snapshot); err != nil {
			return "", "", "", false, err
		}
		return identifiers.generationID, "", "", true, nil
	}
	prepared, err := service.prepareRunPayloads(ctx, claim.Command, document, snapshot, identifiers)
	if err != nil {
		return "", "", "", false, err
	}
	now := service.now()
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return "", "", "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claim.Command.TenantID); err != nil {
		return "", "", "", false, err
	}
	if err = inbox.LockClaimTx(ctx, tx, claim, now); err != nil {
		return "", "", "", false, err
	}
	locked, lockedObsolete, err := service.snapshotInTx(ctx, tx, claim.Command.TenantID, document, true)
	if err != nil {
		return "", "", "", false, err
	}
	if lockedObsolete || !bytes.Equal(locked.ManifestJSON, snapshot.ManifestJSON) {
		return "", "", "", false, ErrDailyTaskObsolete
	}
	var existingGenerationID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM product.daily_task_generations WHERE tenant_id=$1 AND user_id=$2 AND scheduled_for=$3 AND status IN ('generating','succeeded') ORDER BY created_at,id LIMIT 1`, claim.Command.TenantID, document.UserID, document.ScheduledFor).Scan(&existingGenerationID)
	if err == nil {
		if err = inbox.CompleteTx(ctx, tx, claim, now); err != nil {
			return "", "", "", false, err
		}
		if err = tx.Commit(ctx); err != nil {
			return "", "", "", false, err
		}
		return existingGenerationID, "", "", false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", false, err
	}
	title := "Daily task planner"
	conversation, err := service.Runs.CreateDailyPlannerConversationInTx(ctx, tx, executionpostgres.CreateConversationCommand{
		ConversationID: identifiers.conversationID, TenantID: claim.Command.TenantID, UserID: document.UserID,
		MissionID: document.MissionID, RouteRevisionID: document.RouteRevisionID, FocusVersion: document.ExpectedFocusVersion,
		Title: &title, Mode: "task", CorrelationID: identifiers.correlationID, Actor: service.actor(), CreatedEvent: prepared.conversationEvent,
	})
	if err != nil {
		return "", "", "", false, err
	}
	budget := json.RawMessage(fmt.Sprintf(`{"max_steps":%d,"max_cost_microunits":%d}`, service.RunMaxSteps, service.RunMaxCostMicrounits))
	accepted, err := service.Runs.AcceptMessageRunInTx(ctx, tx, executionpostgres.AcceptMessageRunCommand{
		Run:       executionpostgres.AcceptRunCommand{RunID: identifiers.runID, TenantID: claim.Command.TenantID, UserID: document.UserID, ConversationID: identifiers.conversationID, CorrelationID: identifiers.correlationID, DueAt: now.Add(service.RunTimeout), BehaviorProfile: behavior.DailyPlanner, BehaviorEnvironment: snapshot.Manifest.AgentProfile.Environment, ExpectedProfileSnapshotID: snapshot.Manifest.AgentProfile.SnapshotID, ExpectedBehaviorChannelID: snapshot.Manifest.AgentProfile.ChannelID, ExpectedBehaviorSequence: snapshot.Manifest.AgentProfile.Sequence, PinnedBehaviorBinding: true, BudgetSnapshot: budget, Actor: service.actor(), AcceptedEvent: prepared.acceptedEvent, QueuedEvent: prepared.queuedEvent, StartCommand: prepared.startCommand, QueueClass: "background", ResourceClass: "llm", Priority: service.priority(), CostUnits: int64(service.RunMaxSteps), MaxAttempts: service.RunMaxAttempts},
		MessageID: identifiers.messageID, ExpectedConversationVersion: conversation.Version, ExpectedConversationMode: "task", ExpectedConversationProfile: behavior.DailyPlanner, Message: prepared.message, ContentHash: prepared.messageContentHash, AppendedEvent: prepared.messageEvent, Actor: service.actor(),
	})
	if err != nil {
		return "", "", "", false, err
	}
	if accepted.RunID != identifiers.runID || accepted.ProfileSnapshotID != snapshot.Manifest.AgentProfile.SnapshotID || accepted.BehaviorChannelID != snapshot.Manifest.AgentProfile.ChannelID || accepted.BehaviorChannelSequence != snapshot.Manifest.AgentProfile.Sequence {
		return "", "", "", false, ErrRouteConflict
	}
	manifestHash := sha256.Sum256(snapshot.ManifestJSON)
	_, err = tx.Exec(ctx, `INSERT INTO product.daily_task_generations(id,tenant_id,user_id,version,mission_id,route_revision_id,focus_version,scheduled_for,difficulty,available_minutes,status,input_manifest,input_manifest_hash,behavior_profile,behavior_environment,behavior_channel_id,behavior_channel_sequence,behavior_snapshot_id,behavior_activated_at,content_snapshot_id,planner_command_id,planner_run_id,created_at,updated_at) VALUES($1,$2,$3,1,$4,$5,$6,$7,$8,$9,'generating',$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$21)`, identifiers.generationID, claim.Command.TenantID, document.UserID, document.MissionID, document.RouteRevisionID, document.ExpectedFocusVersion, document.ScheduledFor, document.Difficulty, document.AvailableMinutes, snapshot.ManifestJSON, hex.EncodeToString(manifestHash[:]), behavior.DailyPlanner, snapshot.Manifest.AgentProfile.Environment, snapshot.Manifest.AgentProfile.ChannelID, snapshot.Manifest.AgentProfile.Sequence, snapshot.Manifest.AgentProfile.SnapshotID, snapshot.Manifest.AgentProfile.ActivatedAt, snapshot.Manifest.ContentSnapshotID, claim.Command.CommandID, identifiers.runID, now)
	if err != nil {
		return "", "", "", false, err
	}
	if err = inbox.CompleteTx(ctx, tx, claim, now); err != nil {
		return "", "", "", false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", "", "", false, err
	}
	return identifiers.generationID, identifiers.runID, identifiers.conversationID, false, nil
}

type dailyPlannerPayloads struct {
	conversationEvent, message, messageEvent executionpostgres.PayloadPointer
	acceptedEvent, queuedEvent, startCommand executionpostgres.PayloadPointer
	messageContentHash                       string
}

func (service DailyTaskPlannerService) prepareRunPayloads(ctx context.Context, command eventpostgres.DeliveredCommand, document dailyTaskCommandDocument, snapshot dailyTaskSnapshot, identifiers dailyTaskIdentifiers) (dailyPlannerPayloads, error) {
	prompt := map[string]any{"schema_version": 1, "role": "user", "content": []map[string]any{{"type": "text", "text": "Generate today's production practice task from the immutable manifest and accepted route. " + dailyTaskOutputContract + "\nINPUT_MANIFEST=" + string(snapshot.ManifestJSON) + "\nACCEPTED_ROUTE=" + string(snapshot.RouteJSON)}}}
	messageJSON, err := json.Marshal(prompt)
	if err != nil {
		return dailyPlannerPayloads{}, err
	}
	digest := sha256.Sum256(messageJSON)
	put := func(objectID, class string, value any) (executionpostgres.PayloadPointer, error) {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return executionpostgres.PayloadPointer{}, marshalErr
		}
		manifest, putErr := service.Payloads.Put(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: objectID, Class: class, ContentType: "application/json"}, encoded)
		return executionpostgres.PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, putErr
	}
	putRaw := func(objectID, class string, encoded []byte) (executionpostgres.PayloadPointer, error) {
		manifest, putErr := service.Payloads.Put(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: objectID, Class: class, ContentType: "application/json"}, encoded)
		return executionpostgres.PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, putErr
	}
	var result dailyPlannerPayloads
	if result.conversationEvent, err = put(identifiers.conversationID, "event-payload", map[string]any{"subject_id": identifiers.conversationID, "subject_version": 1, "mission_id": document.MissionID, "route_revision_id": document.RouteRevisionID, "focus_version": document.ExpectedFocusVersion, "purpose": "daily_planner"}); err != nil {
		return result, err
	}
	if result.message, err = putRaw(identifiers.messageID, "run-message", messageJSON); err != nil {
		return result, err
	}
	result.messageContentHash = hex.EncodeToString(digest[:])
	if result.messageEvent, err = put(identifiers.messageID, "event-payload", map[string]any{"subject_id": identifiers.conversationID, "subject_version": 2, "message_id": identifiers.messageID, "generation_id": identifiers.generationID}); err != nil {
		return result, err
	}
	binding := map[string]any{"profile": behavior.DailyPlanner, "environment": snapshot.Manifest.AgentProfile.Environment, "snapshot_id": snapshot.Manifest.AgentProfile.SnapshotID, "channel_id": snapshot.Manifest.AgentProfile.ChannelID, "sequence": snapshot.Manifest.AgentProfile.Sequence}
	if result.acceptedEvent, err = put(identifiers.runID, "event-payload", map[string]any{"subject_id": identifiers.runID, "subject_version": 1, "run_id": identifiers.runID, "generation_id": identifiers.generationID, "behavior": binding}); err != nil {
		return result, err
	}
	if result.queuedEvent, err = put(identifiers.startCommandID, "event-payload", map[string]any{"subject_id": identifiers.runID, "subject_version": 2, "run_id": identifiers.runID, "pending_command_id": identifiers.startCommandID}); err != nil {
		return result, err
	}
	result.startCommand, err = put(identifiers.startCommandID, "agent-run-command", map[string]any{"schema_version": 1, "run_id": identifiers.runID, "correlation_id": identifiers.correlationID})
	return result, err
}

func (service DailyTaskPlannerService) loadCommand(ctx context.Context, command eventpostgres.DeliveredCommand) (dailyTaskCommandDocument, error) {
	encoded, err := service.Payloads.Get(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: command.CommandID, Class: missionCommandClass, ContentType: "application/json"}, payload.Manifest{Ref: command.PayloadRef, Hash: command.PayloadHash})
	if err != nil {
		return dailyTaskCommandDocument{}, err
	}
	var document dailyTaskCommandDocument
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&document) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return dailyTaskCommandDocument{}, ErrDailyTaskCommand
	}
	_, dateErr := time.Parse("2006-01-02", document.ScheduledFor)
	if dateErr != nil || document.SchemaVersion != 1 || document.MissionID != command.AggregateID || document.UserID == "" || document.RouteRevisionID == "" || document.ExpectedRouteVersion == 0 || document.ExpectedFocusVersion == 0 || document.Difficulty != "easier" && document.Difficulty != "standard" && document.Difficulty != "harder" || document.AvailableMinutes < 5 || document.AvailableMinutes > 480 || document.CorrelationID == "" {
		return dailyTaskCommandDocument{}, ErrDailyTaskCommand
	}
	return document, nil
}

func (service DailyTaskPlannerService) snapshot(ctx context.Context, tenantID string, document dailyTaskCommandDocument) (dailyTaskSnapshot, bool, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return dailyTaskSnapshot{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return dailyTaskSnapshot{}, false, err
	}
	result, obsolete, err := service.snapshotInTx(ctx, tx, tenantID, document, false)
	if err != nil {
		return dailyTaskSnapshot{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return dailyTaskSnapshot{}, false, err
	}
	return result, obsolete, nil
}

func (service DailyTaskPlannerService) snapshotInTx(ctx context.Context, tx pgx.Tx, tenantID string, document dailyTaskCommandDocument, lock bool) (dailyTaskSnapshot, bool, error) {
	manifest := dailyTaskInputManifest{SchemaVersion: 2, Command: document, EvidenceRevisions: []routeEvidenceRevision{}, ContentSnapshotID: service.ContentSnapshotID}
	var missionStatus, currentRouteID, focusedMissionID string
	query := `SELECT m.version,m.status,m.route_version,COALESCE(m.current_route_revision_id::text,''),r.version,r.status,r.route_payload_ref,r.route_payload_hash FROM product.missions m JOIN product.route_revisions r ON r.tenant_id=m.tenant_id AND r.id=$4 WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.id=$3 AND r.mission_id=m.id`
	if lock {
		query += ` FOR UPDATE OF m,r`
	}
	var focusVersion uint64
	var routeStatus string
	err := tx.QueryRow(ctx, query, tenantID, document.UserID, document.MissionID, document.RouteRevisionID).Scan(&manifest.MissionVersion, &missionStatus, &manifest.RouteVersion, &currentRouteID, &manifest.RouteRowVersion, &routeStatus, &manifest.RoutePayloadRef, &manifest.RoutePayloadHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return dailyTaskSnapshot{}, true, nil
	}
	if err != nil {
		return dailyTaskSnapshot{}, false, err
	}
	focusQuery := `SELECT COALESCE(mission_id::text,''),focus_version FROM product.mission_focuses WHERE tenant_id=$1 AND user_id=$2`
	if lock {
		focusQuery += ` FOR UPDATE`
	}
	if err = tx.QueryRow(ctx, focusQuery, tenantID, document.UserID).Scan(&focusedMissionID, &focusVersion); errors.Is(err, pgx.ErrNoRows) {
		focusedMissionID, focusVersion = "", 0
	} else if err != nil {
		return dailyTaskSnapshot{}, false, err
	}
	manifest.AgentProfile.Profile = string(behavior.DailyPlanner)
	manifest.AgentProfile.Environment = service.BehaviorEnvironment
	err = tx.QueryRow(ctx, `SELECT channel_id::text,sequence,snapshot_id,activated_at FROM agent.behavior_channel_deployments WHERE tenant_id=$1 AND profile_name=$2 AND environment=$3 ORDER BY sequence DESC LIMIT 1`, tenantID, behavior.DailyPlanner, service.BehaviorEnvironment).Scan(&manifest.AgentProfile.ChannelID, &manifest.AgentProfile.Sequence, &manifest.AgentProfile.SnapshotID, &manifest.AgentProfile.ActivatedAt)
	if err != nil {
		return dailyTaskSnapshot{}, false, err
	}
	obsolete := missionStatus != "active" || manifest.RouteVersion != document.ExpectedRouteVersion || currentRouteID != document.RouteRevisionID || focusedMissionID != document.MissionID || focusVersion != document.ExpectedFocusVersion || routeStatus != "accepted"
	rows, err := tx.Query(ctx, `SELECT id::text,version,evidence_type,status,source_kind,source_id::text,payload_ref,content_hash,recorded_at,invalidated_at FROM product.evidence WHERE tenant_id=$1 AND user_id=$2 AND mission_id=$3 ORDER BY id`, tenantID, document.UserID, document.MissionID)
	if err != nil {
		return dailyTaskSnapshot{}, false, err
	}
	for rows.Next() {
		var evidence routeEvidenceRevision
		if err = rows.Scan(&evidence.ID, &evidence.RowVersion, &evidence.Type, &evidence.Status, &evidence.SourceKind, &evidence.SourceID, &evidence.PayloadRef, &evidence.ContentHash, &evidence.RecordedAt, &evidence.Invalidated); err != nil {
			rows.Close()
			return dailyTaskSnapshot{}, false, err
		}
		manifest.EvidenceRevisions = append(manifest.EvidenceRevisions, evidence)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return dailyTaskSnapshot{}, false, err
	}
	rows.Close()
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return dailyTaskSnapshot{}, false, err
	}
	result := dailyTaskSnapshot{Manifest: manifest, ManifestJSON: manifestJSON}
	if !obsolete && !lock {
		encoded, getErr := service.Payloads.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: document.RouteRevisionID, Class: routePayloadClass, ContentType: "application/json"}, payload.Manifest{Ref: manifest.RoutePayloadRef, Hash: manifest.RoutePayloadHash})
		if getErr != nil || !validJSONObject(encoded) {
			return dailyTaskSnapshot{}, false, errors.Join(getErr, payload.ErrIntegrity)
		}
		result.RouteJSON = encoded
	}
	return result, obsolete, nil
}

func (service DailyTaskPlannerService) completeSuperseded(ctx context.Context, inbox eventpostgres.InboxStore, claim eventpostgres.InboxClaim, identifiers dailyTaskIdentifiers, document dailyTaskCommandDocument, snapshot dailyTaskSnapshot) error {
	now := service.now()
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claim.Command.TenantID); err != nil {
		return err
	}
	if err = inbox.LockClaimTx(ctx, tx, claim, now); err != nil {
		return err
	}
	locked, obsolete, err := service.snapshotInTx(ctx, tx, claim.Command.TenantID, document, true)
	if err != nil {
		return err
	}
	if !obsolete {
		return ErrDailyTaskObsolete
	}
	if locked.Manifest.AgentProfile.ChannelID == "" {
		if err = inbox.CompleteTx(ctx, tx, claim, now); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	manifestHash := sha256.Sum256(locked.ManifestJSON)
	_, err = tx.Exec(ctx, `INSERT INTO product.daily_task_generations(id,tenant_id,user_id,version,mission_id,route_revision_id,focus_version,scheduled_for,difficulty,available_minutes,status,input_manifest,input_manifest_hash,behavior_profile,behavior_environment,behavior_channel_id,behavior_channel_sequence,behavior_snapshot_id,behavior_activated_at,content_snapshot_id,planner_command_id,failure_reason,created_at,updated_at,completed_at) VALUES($1,$2,$3,1,$4,$5,$6,$7,$8,$9,'superseded',$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,'focus_or_route_changed',$20,$20,$20) ON CONFLICT (tenant_id,planner_command_id) DO NOTHING`, identifiers.generationID, claim.Command.TenantID, document.UserID, document.MissionID, document.RouteRevisionID, document.ExpectedFocusVersion, document.ScheduledFor, document.Difficulty, document.AvailableMinutes, locked.ManifestJSON, hex.EncodeToString(manifestHash[:]), behavior.DailyPlanner, locked.Manifest.AgentProfile.Environment, locked.Manifest.AgentProfile.ChannelID, locked.Manifest.AgentProfile.Sequence, locked.Manifest.AgentProfile.SnapshotID, locked.Manifest.AgentProfile.ActivatedAt, locked.Manifest.ContentSnapshotID, claim.Command.CommandID, now)
	if err != nil {
		return err
	}
	if err = inbox.CompleteTx(ctx, tx, claim, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (service DailyTaskPlannerService) identifiers(commandID string) (dailyTaskIdentifiers, error) {
	domains := []string{"daily-task-generation", "daily-planner-conversation", "daily-planner-message", "daily-planner-run", "run-start-command", "daily-planner-correlation"}
	values := make([]string, len(domains))
	for index, domain := range domains {
		seed := commandID
		if domain == "run-start-command" {
			seed = values[3]
		}
		value, err := ids.DeterministicUUID(service.IDKey, domain, seed)
		if err != nil {
			return dailyTaskIdentifiers{}, err
		}
		values[index] = value
	}
	return dailyTaskIdentifiers{values[0], values[1], values[2], values[3], values[4], values[5]}, nil
}

func (service DailyTaskPlannerService) actor() json.RawMessage {
	return json.RawMessage(`{"kind":"service","name":"product-daily-task-planner"}`)
}

func (service DailyTaskPlannerService) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func (service DailyTaskPlannerService) priority() int {
	if service.QueuePriority == 0 {
		return 100
	}
	return service.QueuePriority
}

func (service DailyTaskPlannerService) valid() bool {
	return service.Pool != nil && service.Routes.Pool == service.Pool && service.Runs.Pool == service.Pool && service.Payloads != nil && len(service.IDKey) >= 32 && service.BehaviorEnvironment != "" && service.ContentSnapshotID != "" && service.RunTimeout > 0 && service.RunMaxSteps > 0 && service.RunMaxCostMicrounits > 0 && service.RunMaxAttempts > 0 && service.priority() >= 0 && service.priority() <= 1000
}
