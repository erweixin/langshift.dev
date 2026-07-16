package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/behavior"
	executionapi "github.com/langshift/lites/internal/execution/api"
	"github.com/langshift/lites/internal/idempotency"
	idempotencypostgres "github.com/langshift/lites/internal/idempotency/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

const (
	createConversationOperation = "conversations.create"
	createMessageOperation      = "messages.create"
	controlResponseClass        = "agent-control-idempotency"
	controlEventClass           = "event-payload"
	controlCommandClass         = "command-payload"
	controlMessageClass         = "conversation-message"
)

// ControlService is the public Agent Control Plane application service. It
// stages immutable encrypted payloads, then commits the domain mutation and
// its replay response manifest under one durable idempotency transaction.
type ControlService struct {
	Pool                 *pgxpool.Pool
	Store                RunStore
	Payloads             payload.Store
	IDKey                []byte
	IdempotencyKeyPepper []byte
	RequestDigestPepper  []byte
	IdempotencyTTL       time.Duration
	RunTimeout           time.Duration
	RunMaxSteps          int
	RunMaxCostMicrounits int64
	RunMaxAttempts       int
	Now                  func() time.Time
}

type controlPrepared struct {
	input      idempotencypostgres.Input
	descriptor payload.Descriptor
	recordID   string
}

type storedMessageResponse struct {
	RunID               string    `json:"run_id"`
	Status              string    `json:"status"`
	AcceptedAt          time.Time `json:"accepted_at"`
	ConversationVersion uint64    `json:"conversation_version"`
}

func (service ControlService) CreateConversation(ctx context.Context, command executionapi.CreateConversationCommand) (executionapi.ConversationResult, error) {
	if !service.valid() {
		return executionapi.ConversationResult{}, executionapi.ErrDependencyUnavailable
	}
	canonical, err := json.Marshal(struct {
		RequestID string  `json:"request_id"`
		MissionID string  `json:"mission_id"`
		Title     *string `json:"title"`
		Mode      string  `json:"mode"`
	}{command.ClientRequestID, command.MissionID, command.Title, command.Mode})
	if err != nil {
		return executionapi.ConversationResult{}, executionapi.ErrValidation
	}
	prepared, err := service.prepare(command.TenantID, command.UserID, createConversationOperation, command.IdempotencyKey, command.RequestID, canonical)
	if err != nil {
		return executionapi.ConversationResult{}, service.mapError(err)
	}
	executor := service.executor()
	if response, found, loadErr := executor.LoadCompleted(ctx, prepared.input); loadErr != nil {
		return executionapi.ConversationResult{}, service.mapError(loadErr)
	} else if found {
		return readControlResponse[executionapi.ConversationResult](ctx, service.Payloads, prepared.descriptor, response)
	}
	conversationID, err := ids.DeterministicUUID(service.IDKey, "conversation", prepared.recordID)
	if err != nil {
		return executionapi.ConversationResult{}, executionapi.ErrDependencyUnavailable
	}
	correlationID, _ := ids.DeterministicUUID(service.IDKey, "control-correlation", prepared.recordID)
	localStore := service.storeAt(service.now())
	identifiers, err := localStore.conversationIdentifiers(conversationID)
	if err != nil {
		return executionapi.ConversationResult{}, executionapi.ErrDependencyUnavailable
	}
	actor := controlActor(command.UserID, command.SessionID)
	now := localStore.Now()
	eventPayload, err := service.putJSON(ctx, command.TenantID, identifiers.event, controlEventClass, map[string]any{
		"subject_id": conversationID, "subject_version": 1,
		"conversation_id": conversationID, "conversation_version": 1, "mission_id": command.MissionID,
		"mode": command.Mode, "title": command.Title, "status": "active", "created_at": now,
	})
	if err != nil {
		return executionapi.ConversationResult{}, executionapi.ErrDependencyUnavailable
	}
	result := executionapi.ConversationResult{ID: conversationID, Version: 1, Status: "active", UpdatedAt: now}
	responseManifest, err := service.putJSON(ctx, command.TenantID, prepared.recordID, controlResponseClass, result)
	if err != nil {
		return executionapi.ConversationResult{}, executionapi.ErrDependencyUnavailable
	}
	response, _, err := executor.Execute(ctx, prepared.input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		stored, storeErr := localStore.CreateConversationInTx(ctx, tx, CreateConversationCommand{
			ConversationID: conversationID, TenantID: command.TenantID, UserID: command.UserID, MissionID: command.MissionID,
			Title: command.Title, Mode: command.Mode, CorrelationID: correlationID, Actor: actor,
			CreatedEvent: eventPayload,
		})
		if storeErr != nil {
			return idempotency.Response{}, storeErr
		}
		if stored.ID != result.ID || stored.Version != result.Version || stored.Status != result.Status || !stored.UpdatedAt.Equal(result.UpdatedAt) {
			return idempotency.Response{}, ErrRunConflict
		}
		return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: responseManifest.Ref, Hash: responseManifest.Hash, ResourceVersion: result.Version}, nil
	})
	if err != nil {
		return executionapi.ConversationResult{}, service.mapError(err)
	}
	return readControlResponse[executionapi.ConversationResult](ctx, service.Payloads, prepared.descriptor, response)
}

func (service ControlService) CreateMessage(ctx context.Context, command executionapi.CreateMessageCommand) (executionapi.MessageResult, error) {
	if !service.valid() {
		return executionapi.MessageResult{}, executionapi.ErrDependencyUnavailable
	}
	canonical, err := json.Marshal(struct {
		RequestID                   string `json:"request_id"`
		ConversationID              string `json:"conversation_id"`
		Content                     string `json:"content"`
		Mode                        string `json:"mode"`
		ExpectedConversationVersion uint64 `json:"expected_conversation_version"`
	}{command.ClientRequestID, command.ConversationID, command.Content, command.Mode, command.ExpectedConversationVersion})
	if err != nil {
		return executionapi.MessageResult{}, executionapi.ErrValidation
	}
	prepared, err := service.prepare(command.TenantID, command.UserID, createMessageOperation, command.IdempotencyKey, command.RequestID, canonical)
	if err != nil {
		return executionapi.MessageResult{}, service.mapError(err)
	}
	executor := service.executor()
	if response, found, loadErr := executor.LoadCompleted(ctx, prepared.input); loadErr != nil {
		return executionapi.MessageResult{}, service.mapError(loadErr)
	} else if found {
		return readMessageResponse(ctx, service.Payloads, prepared.descriptor, response)
	}
	runID, err := ids.DeterministicUUID(service.IDKey, "conversation-run", prepared.recordID)
	if err != nil {
		return executionapi.MessageResult{}, executionapi.ErrDependencyUnavailable
	}
	messageID, _ := ids.DeterministicUUID(service.IDKey, "conversation-message", prepared.recordID)
	correlationID, _ := ids.DeterministicUUID(service.IDKey, "control-correlation", prepared.recordID)
	localStore := service.storeAt(service.now())
	runIDs, err := localStore.identifiers(runID)
	if err != nil {
		return executionapi.MessageResult{}, executionapi.ErrDependencyUnavailable
	}
	messageIDs, err := localStore.conversationMessageIdentifiers(messageID)
	if err != nil {
		return executionapi.MessageResult{}, executionapi.ErrDependencyUnavailable
	}
	binding, err := localStore.ResolveBehavior(ctx, command.TenantID, behavior.RoutePlanner, "production")
	if err != nil {
		return executionapi.MessageResult{}, service.mapError(err)
	}
	now := localStore.Now()
	dueAt := now.Add(service.RunTimeout)
	keyDigest, err := idempotency.KeyDigest(command.IdempotencyKey, service.IdempotencyKeyPepper)
	if err != nil {
		return executionapi.MessageResult{}, service.mapError(err)
	}
	messagePayload, err := service.putJSON(ctx, command.TenantID, messageID, controlMessageClass, map[string]any{"content": command.Content, "mode": command.Mode})
	if err != nil {
		return executionapi.MessageResult{}, executionapi.ErrDependencyUnavailable
	}
	contentDigest := sha256.Sum256([]byte(command.Content))
	contentHash := hex.EncodeToString(contentDigest[:])
	messageEvent, err := service.putJSON(ctx, command.TenantID, messageIDs.event, controlEventClass, map[string]any{
		"subject_id": command.ConversationID, "subject_version": command.ExpectedConversationVersion + 1,
		"payload_ref": messagePayload.Ref,
	})
	if err != nil {
		return executionapi.MessageResult{}, executionapi.ErrDependencyUnavailable
	}
	acceptedEvent, err := service.putJSON(ctx, command.TenantID, runIDs.acceptedEvent, controlEventClass, map[string]any{
		"subject_id": runID, "subject_version": 1, "request_id": command.ClientRequestID, "command_id": nil,
		"run_id": runID, "conversation_id": command.ConversationID, "run_version": 1,
		"idempotency_scope":    command.TenantID + ":" + command.UserID + ":" + createMessageOperation,
		"idempotency_key_hash": hex.EncodeToString(keyDigest[:]), "due_at": dueAt,
		"profile_snapshot_id": binding.SnapshotID, "behavior_profile": binding.Profile,
		"behavior_environment": binding.Environment, "behavior_channel_id": binding.ChannelID,
		"behavior_channel_sequence": binding.Sequence, "legacy_behavior_binding": false,
	})
	if err != nil {
		return executionapi.MessageResult{}, executionapi.ErrDependencyUnavailable
	}
	queuedEvent, err := service.putJSON(ctx, command.TenantID, runIDs.queuedEvent, controlEventClass, map[string]any{
		"subject_id": runID, "subject_version": 2, "command_id": nil,
		"run_id": runID, "run_version": 2, "pending_command_id": runIDs.startCommand,
		"command_type": "StartAgentRun", "store_epoch": localStore.StoreEpoch,
	})
	if err != nil {
		return executionapi.MessageResult{}, executionapi.ErrDependencyUnavailable
	}
	budget := json.RawMessage(mustControlJSON(map[string]any{"max_steps": service.RunMaxSteps, "max_cost_microunits": service.RunMaxCostMicrounits}))
	startCommand, err := service.putJSON(ctx, command.TenantID, runIDs.startCommand, controlCommandClass, map[string]any{
		"run_id": runID, "conversation_id": command.ConversationID, "message_id": messageID,
		"message_ref": messagePayload.Ref, "message_hash": messagePayload.Hash, "content_hash": contentHash,
		"admission_mode": command.Mode, "behavior_profile": binding.Profile, "behavior_environment": binding.Environment,
		"profile_snapshot_id": binding.SnapshotID, "behavior_channel_id": binding.ChannelID, "behavior_channel_sequence": binding.Sequence,
		"budget_snapshot": json.RawMessage(budget),
	})
	if err != nil {
		return executionapi.MessageResult{}, executionapi.ErrDependencyUnavailable
	}
	result := executionapi.MessageResult{RunID: runID, Status: "queued", AcceptedAt: now, ConversationVersion: command.ExpectedConversationVersion + 1}
	responseManifest, err := service.putJSON(ctx, command.TenantID, prepared.recordID, controlResponseClass, storedMessageResponse{
		RunID: result.RunID, Status: result.Status, AcceptedAt: result.AcceptedAt, ConversationVersion: result.ConversationVersion,
	})
	if err != nil {
		return executionapi.MessageResult{}, executionapi.ErrDependencyUnavailable
	}
	actor := controlActor(command.UserID, command.SessionID)
	response, _, err := executor.Execute(ctx, prepared.input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		stored, storeErr := localStore.AcceptMessageRunInTx(ctx, tx, AcceptMessageRunCommand{
			Run: AcceptRunCommand{
				RunID: runID, TenantID: command.TenantID, UserID: command.UserID, ConversationID: command.ConversationID, CorrelationID: correlationID,
				DueAt: dueAt, BehaviorProfile: behavior.RoutePlanner, BehaviorEnvironment: "production",
				ExpectedProfileSnapshotID: binding.SnapshotID, ExpectedBehaviorChannelID: binding.ChannelID, ExpectedBehaviorSequence: binding.Sequence,
				BudgetSnapshot: budget, Actor: actor, AcceptedEvent: acceptedEvent, QueuedEvent: queuedEvent, StartCommand: startCommand,
				QueueClass: service.queueClass(command.Mode), ResourceClass: "llm", Priority: service.priority(command.Mode), CostUnits: int64(service.RunMaxSteps), MaxAttempts: service.RunMaxAttempts,
			},
			MessageID: messageID, ExpectedConversationVersion: command.ExpectedConversationVersion,
			Message: messagePayload, ContentHash: contentHash, AppendedEvent: messageEvent, Actor: actor,
		})
		if storeErr != nil {
			return idempotency.Response{}, storeErr
		}
		if stored.RunID != result.RunID || stored.Status != result.Status || stored.ConversationVersion != result.ConversationVersion || !stored.AcceptedAt.Equal(result.AcceptedAt) {
			return idempotency.Response{}, ErrRunConflict
		}
		return idempotency.Response{Status: 202, ContentType: "application/json", PayloadRef: responseManifest.Ref, Hash: responseManifest.Hash, ResourceVersion: result.ConversationVersion}, nil
	})
	if err != nil {
		return executionapi.MessageResult{}, service.mapError(err)
	}
	return readMessageResponse(ctx, service.Payloads, prepared.descriptor, response)
}

func (service ControlService) GetRun(ctx context.Context, command executionapi.GetRunCommand) (executionapi.RunResult, error) {
	result, err := service.Store.GetRun(ctx, command.TenantID, command.UserID, command.RunID)
	if err != nil {
		return executionapi.RunResult{}, service.mapError(err)
	}
	return executionapi.RunResult{ID: result.ID, Version: result.Version, Status: result.Status, UpdatedAt: result.UpdatedAt}, nil
}

func (service ControlService) CancelRun(ctx context.Context, command executionapi.CancelRunCommand) (executionapi.RunResult, error) {
	if !service.valid() || !service.Store.validClaim() {
		return executionapi.RunResult{}, executionapi.ErrDependencyUnavailable
	}
	canonical, err := json.Marshal(struct {
		RequestID          string `json:"request_id"`
		RunID              string `json:"run_id"`
		Reason             string `json:"reason"`
		ExpectedRunVersion uint64 `json:"expected_run_version"`
	}{command.ClientRequestID, command.RunID, command.Reason, command.ExpectedRunVersion})
	if err != nil {
		return executionapi.RunResult{}, executionapi.ErrValidation
	}
	prepared, err := service.prepare(command.TenantID, command.UserID, "runs.cancel", command.IdempotencyKey, command.RequestID, canonical)
	if err != nil {
		return executionapi.RunResult{}, service.mapError(err)
	}
	executor := service.executor()
	if response, found, loadErr := executor.LoadCompleted(ctx, prepared.input); loadErr != nil {
		return executionapi.RunResult{}, service.mapError(loadErr)
	} else if found {
		return readControlResponse[executionapi.RunResult](ctx, service.Payloads, prepared.descriptor, response)
	}
	current, err := service.Store.GetRun(ctx, command.TenantID, command.UserID, command.RunID)
	if err != nil {
		return executionapi.RunResult{}, service.mapError(err)
	}
	if current.Version != command.ExpectedRunVersion {
		return executionapi.RunResult{}, executionapi.ErrVersionConflict
	}
	if err = service.Store.requireClaimEpoch(ctx, service.Store.StoreEpoch); err != nil {
		return executionapi.RunResult{}, service.mapError(err)
	}
	cancellationID, err := ids.DeterministicUUID(service.IDKey, "run-cancellation", prepared.recordID)
	if err != nil {
		return executionapi.RunResult{}, executionapi.ErrDependencyUnavailable
	}
	correlationID, _ := ids.DeterministicUUID(service.IDKey, "control-correlation", prepared.recordID)
	localStore := service.storeAt(service.now())
	identifiers, err := localStore.cancellationIdentifiers(cancellationID)
	if err != nil {
		return executionapi.RunResult{}, executionapi.ErrDependencyUnavailable
	}
	propagation, err := localStore.cancellationPropagationIdentifiers(cancellationID)
	if err != nil {
		return executionapi.RunResult{}, executionapi.ErrDependencyUnavailable
	}
	now := localStore.Now()
	nextVersion := command.ExpectedRunVersion + 1
	requestEvent, err := service.putJSON(ctx, command.TenantID, identifiers.requestEvent, controlEventClass, map[string]any{
		"subject_id": cancellationID, "subject_version": 1, "run_id": command.RunID,
		"run_version": command.ExpectedRunVersion, "cancellation_id": cancellationID,
		"root_cancellation_id": cancellationID, "parent_cancellation_id": nil, "cancel_generation": 1,
		"reason": command.Reason, "requested_by": command.UserID, "requested_at": now,
		"reconciliation_due_at": now.Add(cancellationReconciliationDelay),
	})
	if err != nil {
		return executionapi.RunResult{}, executionapi.ErrDependencyUnavailable
	}
	settlementEvent, err := service.putJSON(ctx, command.TenantID, identifiers.settlementEvent, controlEventClass, map[string]any{
		"subject_id": command.RunID, "subject_version": nextVersion, "previous_state": current.Status,
		"new_state": "cancelled", "reason_code": command.Reason, "command_id": nil,
		"run_id": command.RunID, "run_version": nextVersion, "cancellation_id": cancellationID,
		"cancel_generation": 1, "barrier_settled_at": now,
	})
	if err != nil {
		return executionapi.RunResult{}, executionapi.ErrDependencyUnavailable
	}
	attemptEvent, err := service.putJSON(ctx, command.TenantID, cancellationID+"-attempt", controlEventClass, map[string]any{
		"subject_id": command.RunID, "subject_version": nextVersion, "command_id": nil,
	})
	if err != nil {
		return executionapi.RunResult{}, executionapi.ErrDependencyUnavailable
	}
	reconcileCommand, err := service.putJSON(ctx, command.TenantID, identifiers.reconcileCommand, controlCommandClass, map[string]any{
		"cancellation_id": cancellationID, "run_id": command.RunID, "store_epoch": localStore.StoreEpoch,
	})
	if err != nil {
		return executionapi.RunResult{}, executionapi.ErrDependencyUnavailable
	}
	propagateCommand, err := service.putJSON(ctx, command.TenantID, propagation.command, controlCommandClass, map[string]any{
		"cancellation_id": cancellationID, "run_id": command.RunID, "store_epoch": localStore.StoreEpoch,
	})
	if err != nil {
		return executionapi.RunResult{}, executionapi.ErrDependencyUnavailable
	}
	actor := controlActor(command.UserID, command.SessionID)
	response, _, err := executor.Execute(ctx, prepared.input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		cancelled, cancelErr := localStore.RequestCancellationInTx(ctx, tx, RequestRunCancellationCommand{
			CancellationID: cancellationID, TenantID: command.TenantID, UserID: command.UserID, RunID: command.RunID,
			ExpectedRunVersion: command.ExpectedRunVersion, Reason: command.Reason, RequestHash: prepared.input.RequestHash,
			CorrelationID: correlationID, Actor: actor, RequestEvent: requestEvent, SettlementEvent: settlementEvent,
			AttemptCancelledEvent: attemptEvent, ReconcileCommand: reconcileCommand, PropagateCommand: propagateCommand,
		})
		if cancelErr != nil {
			return idempotency.Response{}, cancelErr
		}
		result := executionapi.RunResult{ID: cancelled.RunID, Version: cancelled.RunVersion, Status: string(cancelled.RunStatus), UpdatedAt: cancelled.UpdatedAt}
		manifest, putErr := service.putJSON(ctx, command.TenantID, prepared.recordID, controlResponseClass, result)
		if putErr != nil {
			return idempotency.Response{}, putErr
		}
		return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: manifest.Ref, Hash: manifest.Hash, ResourceVersion: result.Version}, nil
	})
	if err != nil {
		return executionapi.RunResult{}, service.mapError(err)
	}
	return readControlResponse[executionapi.RunResult](ctx, service.Payloads, prepared.descriptor, response)
}

func (service ControlService) prepare(tenantID, userID, operationID, rawKey, requestID string, canonical []byte) (controlPrepared, error) {
	recordID, err := ids.DeterministicUUID(service.IDKey, "control-idempotency:"+operationID, tenantID+"\x00"+userID+"\x00"+rawKey)
	if err != nil {
		return controlPrepared{}, err
	}
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return controlPrepared{}, err
	}
	descriptor := payload.Descriptor{TenantID: tenantID, ObjectID: recordID, Class: controlResponseClass, ContentType: "application/json"}
	return controlPrepared{
		input:      idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: tenantID, UserID: userID, OperationID: operationID}, RawKey: rawKey, RequestHash: requestHash, RequestID: requestID},
		descriptor: descriptor, recordID: recordID,
	}, nil
}

func (service ControlService) executor() idempotencypostgres.Executor {
	return idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
}

func (service ControlService) storeAt(now time.Time) RunStore {
	store := service.Store
	store.Now = func() time.Time { return now }
	return store
}

func (service ControlService) now() time.Time {
	now := time.Now().UTC()
	if service.Now != nil {
		now = service.Now().UTC()
	}
	return now.Truncate(time.Microsecond)
}

func (service ControlService) valid() bool {
	return service.Pool != nil && service.Store.Pool != nil && service.Payloads != nil && len(service.IDKey) >= 32 && len(service.IdempotencyKeyPepper) >= 32 && len(service.RequestDigestPepper) >= 32 && service.IdempotencyTTL > 0 && service.RunTimeout > 0 && service.RunMaxSteps > 0 && service.RunMaxCostMicrounits > 0 && service.RunMaxAttempts > 0
}

func (service ControlService) putJSON(ctx context.Context, tenantID, objectID, class string, value any) (PayloadPointer, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return PayloadPointer{}, err
	}
	manifest, err := service.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: objectID, Class: class, ContentType: "application/json"}, encoded)
	if err != nil {
		return PayloadPointer{}, err
	}
	return PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, nil
}

func readControlResponse[T any](ctx context.Context, store payload.Store, descriptor payload.Descriptor, response idempotency.Response) (T, error) {
	var result T
	if response.PayloadRef == "" || response.Hash == "" {
		return result, executionapi.ErrDependencyUnavailable
	}
	encoded, err := store.Get(ctx, descriptor, payload.Manifest{Ref: response.PayloadRef, Hash: response.Hash})
	if err != nil || json.Unmarshal(encoded, &result) != nil {
		return result, executionapi.ErrDependencyUnavailable
	}
	return result, nil
}

func readMessageResponse(ctx context.Context, store payload.Store, descriptor payload.Descriptor, response idempotency.Response) (executionapi.MessageResult, error) {
	stored, err := readControlResponse[storedMessageResponse](ctx, store, descriptor, response)
	if err != nil || stored.RunID == "" || stored.Status == "" || stored.AcceptedAt.IsZero() || stored.ConversationVersion == 0 {
		return executionapi.MessageResult{}, executionapi.ErrDependencyUnavailable
	}
	return executionapi.MessageResult{RunID: stored.RunID, Status: stored.Status, AcceptedAt: stored.AcceptedAt, ConversationVersion: stored.ConversationVersion}, nil
}

func (service ControlService) mapError(err error) error {
	switch {
	case errors.Is(err, idempotency.ErrKeyConflict):
		return executionapi.ErrIdempotencyConflict
	case errors.Is(err, ErrRunNotFound):
		return executionapi.ErrResourceNotFound
	case errors.Is(err, ErrRunConflict), errors.Is(err, ErrRunMessageConflict):
		return executionapi.ErrVersionConflict
	case errors.Is(err, ErrInvalidCommand):
		return executionapi.ErrValidation
	default:
		return executionapi.ErrDependencyUnavailable
	}
}

func (service ControlService) queueClass(mode string) string {
	if mode == "parallel_background" {
		return "background"
	}
	return "interactive"
}

func (service ControlService) priority(mode string) int {
	if mode == "interrupt" {
		return 200
	}
	if mode == "parallel_background" {
		return 50
	}
	return 100
}

func controlActor(userID, sessionID string) json.RawMessage {
	return json.RawMessage(mustControlJSON(map[string]string{"kind": "user", "id": userID, "session_id": sessionID}))
}

func mustControlJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
