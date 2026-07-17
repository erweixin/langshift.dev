package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/idempotency"
	idempotencypostgres "github.com/langshift/lites/internal/idempotency/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	productapi "github.com/langshift/lites/internal/product/api"
	"github.com/langshift/lites/internal/product/mission"
)

const (
	missionStatusOperation = "missions.update.v2"
	missionFocusOperation  = "missions.focus.v2"
	missionCreateOperation = "missions.create.v2"
	missionResponseClass   = "product-idempotency"
	missionEventClass      = "event-payload"
	missionCommandClass    = "product-command"
	missionGoalClass       = "mission-goal"
	emptyClaimSetHash      = "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"
)

type MissionMutationService struct {
	Pool                 *pgxpool.Pool
	Store                MissionFocusStore
	Payloads             payload.Store
	IDKey                []byte
	IdempotencyKeyPepper []byte
	RequestDigestPepper  []byte
	IdempotencyTTL       time.Duration
	Now                  func() time.Time
}

type observedMissionState struct {
	MissionStatus    mission.Status
	MissionVersion   uint64
	FocusedMissionID string
	FocusVersion     uint64
}

func (service MissionMutationService) Create(ctx context.Context, command productapi.CreateMissionCommand) (productapi.MissionMutationResult, error) {
	if !service.valid() || !validMissionMetadata(command.CommandMetadata) || command.TargetRoleProfileID == "" || !validGoal(command.Goal) {
		return productapi.MissionMutationResult{}, productapi.ErrValidation
	}
	canonical, err := json.Marshal(struct {
		RequestID           string `json:"request_id"`
		SourceRoleProfileID string `json:"source_role_profile_id"`
		TargetRoleProfileID string `json:"target_role_profile_id"`
		Goal                string `json:"goal"`
	}{command.ClientRequestID, command.SourceRoleProfileID, command.TargetRoleProfileID, command.Goal})
	if err != nil {
		return productapi.MissionMutationResult{}, productapi.ErrDependencyUnavailable
	}
	input, responseDescriptor, err := service.idempotencyInput(command.CommandMetadata, missionCreateOperation, canonical)
	if err != nil {
		return productapi.MissionMutationResult{}, productapi.ErrDependencyUnavailable
	}
	if result, found, loadErr := service.loadCompleted(ctx, input, responseDescriptor); loadErr != nil {
		return productapi.MissionMutationResult{}, service.mapError(loadErr)
	} else if found {
		return result, nil
	}
	if err = service.Store.requireMissionEpoch(ctx); err != nil {
		return productapi.MissionMutationResult{}, service.mapError(err)
	}
	missionID, err := ids.DeterministicUUID(service.IDKey, missionCreateOperation+":mission", input.RecordID)
	if err != nil {
		return productapi.MissionMutationResult{}, productapi.ErrDependencyUnavailable
	}
	goal, err := service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: missionID, Class: missionGoalClass, ContentType: "application/json"}, map[string]any{"goal": command.Goal})
	if err != nil {
		return productapi.MissionMutationResult{}, productapi.ErrDependencyUnavailable
	}
	eventID, err := ids.DeterministicUUID(service.IDKey, "mission-created:event", missionID)
	if err != nil {
		return productapi.MissionMutationResult{}, productapi.ErrDependencyUnavailable
	}
	eventPayload, err := service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: eventID, Class: missionEventClass, ContentType: "application/json"}, map[string]any{"subject_id": missionID, "subject_version": 1, "new_state": mission.Draft, "reason_code": "user_create", "source_role_profile_id": nullableString(command.SourceRoleProfileID), "target_role_profile_id": command.TargetRoleProfileID, "goal_payload_ref": goal.Ref, "goal_payload_hash": goal.Hash})
	if err != nil {
		return productapi.MissionMutationResult{}, productapi.ErrDependencyUnavailable
	}
	commandID, err := ids.DeterministicUUID(service.IDKey, "mission-created:route-command", missionID)
	if err != nil {
		return productapi.MissionMutationResult{}, productapi.ErrDependencyUnavailable
	}
	outboxID, err := ids.DeterministicUUID(service.IDKey, "mission-created:route-outbox", missionID)
	if err != nil {
		return productapi.MissionMutationResult{}, productapi.ErrDependencyUnavailable
	}
	commandPayload, err := service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: commandID, Class: missionCommandClass, ContentType: "application/json"}, map[string]any{
		"schema_version": 1, "mission_id": missionID, "user_id": command.UserID,
		"expected_route_version": 0, "expected_claim_set_hash": emptyClaimSetHash,
		"correlation_id": command.RequestID,
	})
	if err != nil {
		return productapi.MissionMutationResult{}, productapi.ErrDependencyUnavailable
	}
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if err := service.validateRoleProfiles(ctx, tx, command.TenantID, command.SourceRoleProfileID, command.TargetRoleProfileID); err != nil {
			return idempotency.Response{}, err
		}
		now := service.now()
		if _, err := tx.Exec(ctx, `INSERT INTO product.missions(id,tenant_id,user_id,status,source_role_profile_id,target_role_profile_id,goal_payload_ref,goal_payload_hash,route_version,claim_set_hash,created_at,updated_at) VALUES($1,$2,$3,'draft',$4,$5,$6,$7,0,$8,$9,$9)`, missionID, command.TenantID, command.UserID, nullableString(command.SourceRoleProfileID), command.TargetRoleProfileID, goal.Ref, goal.Hash, emptyClaimSetHash, now); err != nil {
			return idempotency.Response{}, err
		}
		if _, err := service.Store.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: command.TenantID, UserID: command.UserID, EventType: "MissionCreated", SchemaVersion: 1, AggregateKind: "mission", AggregateID: missionID, AggregateVersion: 1, StoreEpoch: service.Store.StoreEpoch, OccurredAt: now, Actor: missionActor(command.UserID, command.SessionID), CorrelationID: command.RequestID, PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: commandID, CommandType: "GenerateMissionRoute", PayloadRef: commandPayload.Ref, PayloadHash: commandPayload.Hash}}}); err != nil {
			return idempotency.Response{}, err
		}
		focus, err := readMissionFocus(ctx, tx, command.TenantID, command.UserID)
		if err != nil {
			return idempotency.Response{}, err
		}
		result := productapi.MissionMutationResult{Mission: productapi.MissionResource{ID: missionID, Version: 1, Status: mission.Draft, SourceRoleProfileID: optionalString(command.SourceRoleProfileID), TargetRoleProfileID: command.TargetRoleProfileID, CreatedAt: now, UpdatedAt: now}, Focus: focus, EventIDs: []string{eventID}}
		encoded, err := json.Marshal(result)
		if err != nil {
			return idempotency.Response{}, err
		}
		manifest, err := service.Payloads.Put(ctx, responseDescriptor, encoded)
		if err != nil {
			return idempotency.Response{}, err
		}
		return idempotency.Response{Status: http.StatusOK, ContentType: "application/vnd.lites.mission.v2+json", PayloadRef: manifest.Ref, Hash: manifest.Hash, ResourceVersion: 1}, nil
	})
	if err != nil {
		return productapi.MissionMutationResult{}, service.mapError(err)
	}
	result, err := service.readResponse(ctx, responseDescriptor, response)
	if err != nil {
		return productapi.MissionMutationResult{}, service.mapError(err)
	}
	if replayed {
		result.Replayed = true
	}
	return result, nil
}

func (service MissionMutationService) ChangeStatus(ctx context.Context, command productapi.ChangeMissionStatusCommand) (productapi.MissionMutationResult, error) {
	if !service.valid() || !validMissionMetadata(command.CommandMetadata) || command.MissionID == "" || command.ExpectedMissionVersion < 1 {
		return productapi.MissionMutationResult{}, productapi.ErrValidation
	}
	canonical, err := json.Marshal(struct {
		RequestID              string         `json:"request_id"`
		MissionID              string         `json:"mission_id"`
		Status                 mission.Status `json:"status"`
		ReplacementMissionID   string         `json:"replacement_mission_id"`
		ExpectedMissionVersion uint64         `json:"expected_mission_version"`
		ExpectedFocusVersion   uint64         `json:"expected_focus_version"`
	}{command.ClientRequestID, command.MissionID, command.Status, command.ReplacementMissionID, command.ExpectedMissionVersion, command.ExpectedFocusVersion})
	if err != nil {
		return productapi.MissionMutationResult{}, productapi.ErrDependencyUnavailable
	}
	input, responseDescriptor, err := service.idempotencyInput(command.CommandMetadata, missionStatusOperation, canonical)
	if err != nil {
		return productapi.MissionMutationResult{}, productapi.ErrDependencyUnavailable
	}
	if result, found, loadErr := service.loadCompleted(ctx, input, responseDescriptor); loadErr != nil {
		return productapi.MissionMutationResult{}, service.mapError(loadErr)
	} else if found {
		return result, nil
	}
	if err = service.Store.requireMissionEpoch(ctx); err != nil {
		return productapi.MissionMutationResult{}, service.mapError(err)
	}
	observed, err := service.observe(ctx, command.TenantID, command.UserID, command.MissionID)
	if err != nil {
		return productapi.MissionMutationResult{}, service.mapError(err)
	}
	if observed.MissionVersion != command.ExpectedMissionVersion || observed.FocusVersion != command.ExpectedFocusVersion {
		return productapi.MissionMutationResult{}, productapi.ErrStateConflict
	}
	mutationID, err := ids.DeterministicUUID(service.IDKey, missionStatusOperation+":mutation", input.RecordID)
	if err != nil {
		return productapi.MissionMutationResult{}, productapi.ErrDependencyUnavailable
	}
	statusEventID, _ := ids.DeterministicUUID(service.IDKey, "mission-status-changed:event", mutationID)
	statusEvent, err := service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: statusEventID, Class: missionEventClass, ContentType: "application/json"}, map[string]any{"subject_id": command.MissionID, "subject_version": command.ExpectedMissionVersion + 1, "previous_state": observed.MissionStatus, "new_state": command.Status, "reason_code": "user_status_change"})
	if err != nil {
		return productapi.MissionMutationResult{}, productapi.ErrDependencyUnavailable
	}
	focusEventID, _ := ids.DeterministicUUID(service.IDKey, "mission-focus-changed:event", mutationID+":focus")
	newFocusID := observed.FocusedMissionID
	if observed.FocusedMissionID == command.MissionID && command.Status != mission.Active {
		newFocusID = command.ReplacementMissionID
	} else if command.Status == mission.Active && observed.FocusedMissionID == "" {
		newFocusID = command.MissionID
	}
	focusEvent := PayloadPointer{}
	if newFocusID != observed.FocusedMissionID {
		manifest, putErr := service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: focusEventID, Class: missionEventClass, ContentType: "application/json"}, focusEventBody(command.UserID, observed.FocusedMissionID, newFocusID, observed.FocusVersion, "mission_status_change"))
		if putErr != nil {
			return productapi.MissionMutationResult{}, productapi.ErrDependencyUnavailable
		}
		focusEvent = PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}
	}
	dailyTask := DailyTaskTrigger{}
	if command.Status == mission.Active && newFocusID == command.MissionID && newFocusID != observed.FocusedMissionID {
		dailyTask, err = service.prepareDailyTaskTrigger(ctx, command.TenantID, command.UserID, command.MissionID, observed.FocusVersion+1, command.RequestID, mutationID+":focus")
		if err != nil {
			return productapi.MissionMutationResult{}, service.mapError(err)
		}
	}
	storeCommand := ChangeMissionStatusCommand{MutationID: mutationID, TenantID: command.TenantID, UserID: command.UserID, MissionID: command.MissionID, NextStatus: command.Status, ExpectedMissionVersion: command.ExpectedMissionVersion, ExpectedFocusVersion: command.ExpectedFocusVersion, ReplacementMissionID: command.ReplacementMissionID, CorrelationID: command.RequestID, Actor: missionActor(command.UserID, command.SessionID), StatusChangedEvent: PayloadPointer{Ref: statusEvent.Ref, Hash: statusEvent.Hash}, FocusChangedEvent: focusEvent, DailyTask: dailyTask}
	return service.execute(ctx, input, responseDescriptor, func(ctx context.Context, tx pgx.Tx) (MissionFocusResult, error) {
		return service.Store.ChangeStatusInTx(ctx, tx, storeCommand)
	})
}

func (service MissionMutationService) SetFocus(ctx context.Context, command productapi.SetMissionFocusCommand) (productapi.MissionMutationResult, error) {
	if !service.valid() || !validMissionMetadata(command.CommandMetadata) || command.MissionID == "" {
		return productapi.MissionMutationResult{}, productapi.ErrValidation
	}
	canonical, err := json.Marshal(struct {
		RequestID            string `json:"request_id"`
		MissionID            string `json:"mission_id"`
		ExpectedFocusVersion uint64 `json:"expected_focus_version"`
	}{command.ClientRequestID, command.MissionID, command.ExpectedFocusVersion})
	if err != nil {
		return productapi.MissionMutationResult{}, productapi.ErrDependencyUnavailable
	}
	input, responseDescriptor, err := service.idempotencyInput(command.CommandMetadata, missionFocusOperation, canonical)
	if err != nil {
		return productapi.MissionMutationResult{}, productapi.ErrDependencyUnavailable
	}
	if result, found, loadErr := service.loadCompleted(ctx, input, responseDescriptor); loadErr != nil {
		return productapi.MissionMutationResult{}, service.mapError(loadErr)
	} else if found {
		return result, nil
	}
	if err = service.Store.requireMissionEpoch(ctx); err != nil {
		return productapi.MissionMutationResult{}, service.mapError(err)
	}
	observed, err := service.observe(ctx, command.TenantID, command.UserID, command.MissionID)
	if err != nil {
		return productapi.MissionMutationResult{}, service.mapError(err)
	}
	if observed.FocusVersion != command.ExpectedFocusVersion {
		return productapi.MissionMutationResult{}, productapi.ErrStateConflict
	}
	mutationID, err := ids.DeterministicUUID(service.IDKey, missionFocusOperation+":mutation", input.RecordID)
	if err != nil {
		return productapi.MissionMutationResult{}, productapi.ErrDependencyUnavailable
	}
	focusEventID, _ := ids.DeterministicUUID(service.IDKey, "mission-focus-changed:event", mutationID)
	focusEvent, err := service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: focusEventID, Class: missionEventClass, ContentType: "application/json"}, focusEventBody(command.UserID, observed.FocusedMissionID, command.MissionID, observed.FocusVersion, "user_focus_change"))
	if err != nil {
		return productapi.MissionMutationResult{}, productapi.ErrDependencyUnavailable
	}
	dailyTask := DailyTaskTrigger{}
	if observed.FocusedMissionID != command.MissionID {
		dailyTask, err = service.prepareDailyTaskTrigger(ctx, command.TenantID, command.UserID, command.MissionID, observed.FocusVersion+1, command.RequestID, mutationID)
		if err != nil {
			return productapi.MissionMutationResult{}, service.mapError(err)
		}
	}
	storeCommand := SetMissionFocusCommand{MutationID: mutationID, TenantID: command.TenantID, UserID: command.UserID, MissionID: command.MissionID, ExpectedFocusVersion: command.ExpectedFocusVersion, CorrelationID: command.RequestID, Actor: missionActor(command.UserID, command.SessionID), FocusChangedEvent: PayloadPointer{Ref: focusEvent.Ref, Hash: focusEvent.Hash}, DailyTask: dailyTask}
	return service.execute(ctx, input, responseDescriptor, func(ctx context.Context, tx pgx.Tx) (MissionFocusResult, error) {
		return service.Store.SetFocusInTx(ctx, tx, storeCommand)
	})
}

func (service MissionMutationService) prepareDailyTaskTrigger(ctx context.Context, tenantID, userID, missionID string, focusVersion uint64, correlationID, seed string) (DailyTaskTrigger, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return DailyTaskTrigger{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return DailyTaskTrigger{}, err
	}
	var routeRevisionID, routeStatus string
	var routeVersion uint64
	err = tx.QueryRow(ctx, `SELECT m.route_version,COALESCE(m.current_route_revision_id::text,''),COALESCE(r.status,'') FROM product.missions m LEFT JOIN product.route_revisions r ON r.tenant_id=m.tenant_id AND r.id=m.current_route_revision_id WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.id=$3`, tenantID, userID, missionID).Scan(&routeVersion, &routeRevisionID, &routeStatus)
	if err != nil {
		return DailyTaskTrigger{}, err
	}
	if routeVersion == 0 || routeRevisionID == "" || routeStatus != "accepted" {
		if err = tx.Commit(ctx); err != nil {
			return DailyTaskTrigger{}, err
		}
		return DailyTaskTrigger{}, nil
	}
	document, err := dailyCommandDocumentFor(ctx, tx, tenantID, userID, missionID, routeRevisionID, routeVersion, focusVersion, correlationID, service.Now)
	if err != nil {
		return DailyTaskTrigger{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return DailyTaskTrigger{}, err
	}
	commandID, err := ids.DeterministicUUID(service.IDKey, "mission-focus:daily-command", seed)
	if err != nil {
		return DailyTaskTrigger{}, err
	}
	manifest, err := service.putJSON(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: commandID, Class: missionCommandClass, ContentType: "application/json"}, document)
	if err != nil {
		return DailyTaskTrigger{}, err
	}
	return DailyTaskTrigger{CommandID: commandID, MissionID: missionID, RouteRevisionID: routeRevisionID, ExpectedRouteVersion: routeVersion, ExpectedFocusVersion: focusVersion, Payload: PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}}, nil
}

func (service MissionMutationService) execute(ctx context.Context, input idempotencypostgres.Input, descriptor payload.Descriptor, mutation func(context.Context, pgx.Tx) (MissionFocusResult, error)) (productapi.MissionMutationResult, error) {
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		stored, mutationErr := mutation(ctx, tx)
		if mutationErr != nil {
			return idempotency.Response{}, mutationErr
		}
		result, loadErr := service.resource(ctx, tx, input.Scope.TenantID, input.Scope.UserID, stored)
		if loadErr != nil {
			return idempotency.Response{}, loadErr
		}
		encoded, encodeErr := json.Marshal(result)
		if encodeErr != nil {
			return idempotency.Response{}, encodeErr
		}
		manifest, putErr := service.Payloads.Put(ctx, descriptor, encoded)
		if putErr != nil {
			return idempotency.Response{}, putErr
		}
		return idempotency.Response{Status: http.StatusOK, ContentType: "application/vnd.lites.mission.v2+json", PayloadRef: manifest.Ref, Hash: manifest.Hash, ResourceVersion: result.Focus.Version}, nil
	})
	if err != nil {
		return productapi.MissionMutationResult{}, service.mapError(err)
	}
	result, err := service.readResponse(ctx, descriptor, response)
	if err != nil {
		return productapi.MissionMutationResult{}, service.mapError(err)
	}
	if replayed {
		result.Replayed = true
	}
	return result, nil
}

func (service MissionMutationService) resource(ctx context.Context, tx pgx.Tx, tenantID, userID string, stored MissionFocusResult) (productapi.MissionMutationResult, error) {
	var resource productapi.MissionResource
	resource.ID, resource.Version, resource.Status = stored.MissionID, stored.MissionVersion, stored.MissionStatus
	if err := tx.QueryRow(ctx, `SELECT source_role_profile_id::text,target_role_profile_id::text,current_route_revision_id::text,created_at,updated_at FROM product.missions WHERE tenant_id=$1 AND user_id=$2 AND id=$3`, tenantID, userID, stored.MissionID).Scan(&resource.SourceRoleProfileID, &resource.TargetRoleProfileID, &resource.CurrentRouteRevisionID, &resource.CreatedAt, &resource.UpdatedAt); err != nil {
		return productapi.MissionMutationResult{}, err
	}
	resource.Focused = stored.FocusedMissionID == stored.MissionID
	var focusID *string
	if stored.FocusedMissionID != "" {
		value := stored.FocusedMissionID
		focusID = &value
	}
	return productapi.MissionMutationResult{Mission: resource, Focus: productapi.MissionFocus{MissionID: focusID, Version: stored.FocusVersion}, EventIDs: stored.EventIDs}, nil
}

func (service MissionMutationService) observe(ctx context.Context, tenantID, userID, missionID string) (observedMissionState, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return observedMissionState{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return observedMissionState{}, err
	}
	var observed observedMissionState
	if err = tx.QueryRow(ctx, `SELECT status,version FROM product.missions WHERE tenant_id=$1 AND user_id=$2 AND id=$3`, tenantID, userID, missionID).Scan(&observed.MissionStatus, &observed.MissionVersion); err != nil {
		return observedMissionState{}, err
	}
	var focusID *string
	if err = tx.QueryRow(ctx, `SELECT mission_id::text,focus_version FROM product.mission_focuses WHERE tenant_id=$1 AND user_id=$2`, tenantID, userID).Scan(&focusID, &observed.FocusVersion); errors.Is(err, pgx.ErrNoRows) {
		observed.FocusVersion = 0
	} else if err != nil {
		return observedMissionState{}, err
	}
	if focusID != nil {
		observed.FocusedMissionID = *focusID
	}
	if err = tx.Commit(ctx); err != nil {
		return observedMissionState{}, err
	}
	return observed, nil
}

func (service MissionMutationService) idempotencyInput(metadata productapi.CommandMetadata, operation string, canonical []byte) (idempotencypostgres.Input, payload.Descriptor, error) {
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return idempotencypostgres.Input{}, payload.Descriptor{}, err
	}
	recordID, err := ids.DeterministicUUID(service.IDKey, "product-idempotency:"+operation, metadata.TenantID+"\x00"+metadata.UserID+"\x00"+metadata.IdempotencyKey)
	if err != nil {
		return idempotencypostgres.Input{}, payload.Descriptor{}, err
	}
	input := idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: metadata.TenantID, UserID: metadata.UserID, OperationID: operation}, RawKey: metadata.IdempotencyKey, RequestHash: requestHash, RequestID: metadata.RequestID}
	return input, payload.Descriptor{TenantID: metadata.TenantID, ObjectID: recordID, Class: missionResponseClass, ContentType: "application/json"}, nil
}

func (service MissionMutationService) loadCompleted(ctx context.Context, input idempotencypostgres.Input, descriptor payload.Descriptor) (productapi.MissionMutationResult, bool, error) {
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	response, found, err := executor.LoadCompleted(ctx, input)
	if err != nil || !found {
		return productapi.MissionMutationResult{}, found, err
	}
	result, err := service.readResponse(ctx, descriptor, response)
	if err == nil {
		result.Replayed = true
	}
	return result, true, err
}

func (service MissionMutationService) readResponse(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (productapi.MissionMutationResult, error) {
	encoded, err := service.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: response.PayloadRef, Hash: response.Hash})
	if err != nil {
		return productapi.MissionMutationResult{}, err
	}
	var result productapi.MissionMutationResult
	decoder := json.NewDecoder(bytesReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return productapi.MissionMutationResult{}, errors.New("invalid mission response payload")
	}
	return result, nil
}

func (service MissionMutationService) putJSON(ctx context.Context, descriptor payload.Descriptor, value any) (payload.Manifest, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return payload.Manifest{}, err
	}
	return service.Payloads.Put(ctx, descriptor, encoded)
}

func (service MissionMutationService) valid() bool {
	return service.Pool != nil && service.Payloads != nil && len(service.IDKey) >= 32 && len(service.IdempotencyKeyPepper) >= 32 && len(service.RequestDigestPepper) >= 32 && service.IdempotencyTTL > 0 && service.Store.Pool == service.Pool
}

func validMissionMetadata(metadata productapi.CommandMetadata) bool {
	return metadata.RequestID != "" && metadata.ClientRequestID != "" && metadata.IdempotencyKey != "" && metadata.TenantID != "" && metadata.UserID != "" && metadata.SessionID != ""
}

func missionActor(userID, sessionID string) json.RawMessage {
	encoded, _ := json.Marshal(map[string]any{"kind": "user", "user_id": userID, "session_id": sessionID})
	return encoded
}

func focusEventBody(userID, previousID, nextID string, previousVersion uint64, reason string) map[string]any {
	var previous, next any
	if previousID != "" {
		previous = previousID
	}
	if nextID != "" {
		next = nextID
	}
	return map[string]any{"subject_id": userID, "subject_version": previousVersion + 1, "previous_state": previousID, "new_state": nextID, "reason_code": reason, "user_id": userID, "previous_mission_id": previous, "mission_id": next, "previous_focus_version": previousVersion, "focus_version": previousVersion + 1}
}

func (service MissionMutationService) validateRoleProfiles(ctx context.Context, tx pgx.Tx, tenantID, sourceID, targetID string) error {
	var targetValid, sourceValid bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product.role_profiles WHERE tenant_id=$1 AND id=$2 AND status='active'),$3::uuid IS NULL OR EXISTS(SELECT 1 FROM product.role_profiles WHERE tenant_id=$1 AND id=$3 AND status='active')`, tenantID, targetID, nullableString(sourceID)).Scan(&targetValid, &sourceValid); err != nil {
		return err
	}
	if !targetValid || !sourceValid {
		return ErrMissionNotFound
	}
	return nil
}

func (service MissionMutationService) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC()
	}
	return time.Now().UTC()
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func validGoal(value string) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) == value && value != "" && utf8.RuneCountInString(value) <= 4000
}

func (service MissionMutationService) mapError(err error) error {
	switch {
	case errors.Is(err, pgx.ErrNoRows), errors.Is(err, ErrMissionNotFound):
		return productapi.ErrResourceNotFound
	case errors.Is(err, idempotency.ErrKeyConflict), errors.Is(err, idempotency.ErrInProgress):
		return productapi.ErrIdempotencyConflict
	case errors.Is(err, ErrMissionConflict), errors.Is(err, ErrMissionTransition), errors.Is(err, ErrFocusReplacement):
		return productapi.ErrStateConflict
	case errors.Is(err, mission.ErrInvalid):
		return productapi.ErrValidation
	default:
		return errors.Join(productapi.ErrDependencyUnavailable, err)
	}
}

func bytesReader(value []byte) *bytes.Reader { return bytes.NewReader(value) }

var _ productapi.MissionMutationService = MissionMutationService{}
