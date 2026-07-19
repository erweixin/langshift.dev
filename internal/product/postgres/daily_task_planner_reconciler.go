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
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	productroute "github.com/langshift/lites/internal/product/route"
	producttask "github.com/langshift/lites/internal/product/task"
)

const dailyTaskPayloadClass = "daily-task"

type DailyTaskPlannerReconciler struct {
	Pool     *pgxpool.Pool
	Routes   RouteStore
	Payloads payload.Store
	IDKey    []byte
	Now      func() time.Time
}

type DailyTaskPlannerReconcileResult struct {
	Scanned, Scheduled, Superseded, Failed, Replayed int
}

type terminalDailyPlannerRun struct {
	GenerationID, UserID, MissionID, RouteRevisionID, RunID, RunStatus, CorrelationID string
	GenerationVersion, FocusVersion                                                   uint64
	ScheduledFor                                                                      time.Time
	Difficulty                                                                        string
	AvailableMinutes                                                                  int
	InputManifest                                                                     json.RawMessage
}

type replacedDailyTask struct {
	ID      string
	Status  string
	Version uint64
}

func (reconciler DailyTaskPlannerReconciler) ListTenantIDs(ctx context.Context, afterTenantID string, limit int) ([]string, error) {
	if !reconciler.valid() || limit < 1 || limit > 5000 {
		return nil, ErrDailyTaskCommand
	}
	rows, err := reconciler.Pool.Query(ctx, `SELECT tenant_id::text FROM agent.list_daily_task_planner_reconciliation_tenants(NULLIF($1,'')::uuid,$2)`, afterTenantID, limit)
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

func (reconciler DailyTaskPlannerReconciler) ReconcileTenant(ctx context.Context, tenantID string, limit int) (DailyTaskPlannerReconcileResult, error) {
	if !reconciler.valid() || tenantID == "" || limit < 1 || limit > 500 {
		return DailyTaskPlannerReconcileResult{}, ErrDailyTaskCommand
	}
	items, err := reconciler.loadTerminalRuns(ctx, tenantID, limit)
	if err != nil {
		return DailyTaskPlannerReconcileResult{}, err
	}
	result := DailyTaskPlannerReconcileResult{Scanned: len(items)}
	for _, item := range items {
		outcome, replayed, reconcileErr := reconciler.reconcileOne(ctx, tenantID, item)
		if reconcileErr != nil {
			return result, reconcileErr
		}
		if replayed {
			result.Replayed++
			continue
		}
		switch outcome {
		case "scheduled":
			result.Scheduled++
		case "superseded":
			result.Superseded++
		case "failed":
			result.Failed++
		default:
			return result, ErrDailyTaskCommand
		}
	}
	return result, nil
}

func (reconciler DailyTaskPlannerReconciler) reconcileOne(ctx context.Context, tenantID string, item terminalDailyPlannerRun) (string, bool, error) {
	if item.RunStatus != "succeeded" {
		return reconciler.finishFailure(ctx, tenantID, item, "planner_run_"+item.RunStatus)
	}
	encoded, err := reconciler.loadAssistantMessage(ctx, tenantID, item)
	if err != nil {
		return reconciler.finishFailure(ctx, tenantID, item, "planner_output_invalid")
	}
	document, err := producttask.ParseDocument(encoded)
	if err != nil || document.Difficulty != item.Difficulty || document.EstimatedMinutes != item.AvailableMinutes {
		return reconciler.finishFailure(ctx, tenantID, item, "planner_output_invalid")
	}
	manifest, err := decodeDailyTaskInputManifest(item.InputManifest)
	if err != nil || !reconciler.capabilitiesAllowed(ctx, tenantID, item.RouteRevisionID, manifest, document.CapabilityIDs) {
		return reconciler.finishFailure(ctx, tenantID, item, "planner_output_invalid")
	}
	canonical, err := json.Marshal(document)
	if err != nil {
		return "", false, err
	}
	taskID, eventID, outboxID, publishID, err := reconciler.successIDs(item.RunID)
	if err != nil {
		return "", false, err
	}
	taskPayload, err := reconciler.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: taskID, Class: dailyTaskPayloadClass, ContentType: "application/json"}, canonical)
	if err != nil {
		return "", false, err
	}
	eventPayload, err := reconciler.putEvent(ctx, tenantID, eventID, map[string]any{"subject_id": taskID, "subject_version": 1, "daily_task_id": taskID, "generation_id": item.GenerationID, "mission_id": item.MissionID, "route_revision_id": item.RouteRevisionID, "focus_version": item.FocusVersion, "scheduled_for": item.ScheduledFor.Format("2006-01-02"), "task_payload_hash": taskPayload.Hash, "planner_run_id": item.RunID})
	if err != nil {
		return "", false, err
	}
	now := reconciler.now()
	tx, err := reconciler.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return "", false, err
	}
	current, terminal, drifted, err := reconciler.lockGenerationState(ctx, tx, tenantID, item)
	if err != nil {
		return "", false, err
	}
	if terminal {
		return "", true, tx.Commit(ctx)
	}
	if drifted {
		if err = reconciler.supersedeInTx(ctx, tx, tenantID, item, current, now); err != nil {
			return "", false, err
		}
		return "superseded", false, tx.Commit(ctx)
	}
	if err = reconciler.skipReplacedDailyTask(ctx, tx, tenantID, item, now); err != nil {
		return "", false, err
	}
	manifestHash := sha256.Sum256(item.InputManifest)
	_, err = tx.Exec(ctx, `INSERT INTO product.daily_tasks(id,tenant_id,user_id,version,mission_id,route_revision_id,status,practice_kind,task_payload_ref,causal_manifest,estimated_minutes,scheduled_for,completed_at,generation_id,task_payload_hash,causal_manifest_hash,focus_version,difficulty,current_submission_id,current_review_id,rescheduled_to,created_at,updated_at) VALUES($1,$2,$3,1,$4,$5,'scheduled',$6,$7,$8,$9,$10,NULL,$11,$12,$13,$14,$15,NULL,NULL,NULL,$16,$16)`, taskID, tenantID, item.UserID, item.MissionID, item.RouteRevisionID, document.PracticeKind, taskPayload.Ref, item.InputManifest, document.EstimatedMinutes, item.ScheduledFor, item.GenerationID, taskPayload.Hash, hex.EncodeToString(manifestHash[:]), item.FocusVersion, document.Difficulty, now)
	if err != nil {
		return "", false, err
	}
	tag, err := tx.Exec(ctx, `UPDATE product.daily_task_generations SET version=version+1,status='succeeded',task_id=$1,completed_at=$2,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND version=$5 AND status='generating' AND planner_run_id=$6`, taskID, now, tenantID, item.GenerationID, current, item.RunID)
	if err != nil || tag.RowsAffected() != 1 {
		return "", false, ErrRouteConflict
	}
	_, err = reconciler.Routes.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: tenantID, UserID: item.UserID, EventType: "DailyTaskScheduled", SchemaVersion: 1, AggregateKind: "daily_task", AggregateID: taskID, AggregateVersion: 1, StoreEpoch: reconciler.Routes.StoreEpoch, OccurredAt: now, Actor: reconciler.actor(), CorrelationID: item.CorrelationID, PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishID, CommandType: "events.publish", PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}}})
	if err != nil {
		return "", false, err
	}
	return "scheduled", false, tx.Commit(ctx)
}

func (reconciler DailyTaskPlannerReconciler) skipReplacedDailyTask(ctx context.Context, tx pgx.Tx, tenantID string, item terminalDailyPlannerRun, now time.Time) error {
	var previous replacedDailyTask
	err := tx.QueryRow(ctx, `SELECT id::text,version,status FROM product.daily_tasks WHERE tenant_id=$1 AND user_id=$2 AND scheduled_for=$3 AND route_revision_id<>$4 AND status IN ('scheduled','in_progress') FOR UPDATE`, tenantID, item.UserID, item.ScheduledFor, item.RouteRevisionID).Scan(&previous.ID, &previous.Version, &previous.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	nextVersion := previous.Version + 1
	tag, err := tx.Exec(ctx, `UPDATE product.daily_tasks SET version=$1,status='skipped',updated_at=$2 WHERE tenant_id=$3 AND user_id=$4 AND id=$5 AND version=$6 AND status=$7`, nextVersion, now, tenantID, item.UserID, previous.ID, previous.Version, previous.Status)
	if err != nil || tag.RowsAffected() != 1 {
		return ErrRouteConflict
	}
	seed := item.RunID + "\x00" + previous.ID
	values := make([]string, 3)
	for index, domain := range []string{"daily-task-route-replaced-event", "daily-task-route-replaced-outbox", "daily-task-route-replaced-publish"} {
		values[index], err = ids.DeterministicUUID(reconciler.IDKey, domain, seed)
		if err != nil {
			return err
		}
	}
	eventPayload, err := reconciler.putEvent(ctx, tenantID, values[0], map[string]any{"subject_id": previous.ID, "subject_version": nextVersion, "action": "skip", "reschedule_for": nil})
	if err != nil {
		return err
	}
	causationID := item.RunID
	_, err = reconciler.Routes.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: values[0], TenantID: tenantID, UserID: item.UserID, EventType: "DailyTaskUpdated", SchemaVersion: 1, AggregateKind: "daily_task", AggregateID: previous.ID, AggregateVersion: nextVersion, StoreEpoch: reconciler.Routes.StoreEpoch, OccurredAt: now, Actor: reconciler.actor(), CausationID: &causationID, CorrelationID: item.CorrelationID, PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: values[1], CommandID: values[2], CommandType: "events.publish", PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}}})
	return err
}

func (reconciler DailyTaskPlannerReconciler) finishFailure(ctx context.Context, tenantID string, item terminalDailyPlannerRun, reason string) (string, bool, error) {
	now := reconciler.now()
	tx, err := reconciler.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return "", false, err
	}
	version, terminal, drifted, err := reconciler.lockGenerationState(ctx, tx, tenantID, item)
	if err != nil {
		return "", false, err
	}
	if terminal {
		return "", true, tx.Commit(ctx)
	}
	if drifted {
		if err = reconciler.supersedeInTx(ctx, tx, tenantID, item, version, now); err != nil {
			return "", false, err
		}
		return "superseded", false, tx.Commit(ctx)
	}
	tag, err := tx.Exec(ctx, `UPDATE product.daily_task_generations SET version=version+1,status='failed',failure_reason=$1,completed_at=$2,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND version=$5 AND status='generating' AND planner_run_id=$6`, reason, now, tenantID, item.GenerationID, version, item.RunID)
	if err != nil || tag.RowsAffected() != 1 {
		return "", false, ErrRouteConflict
	}
	return "failed", false, tx.Commit(ctx)
}

func (reconciler DailyTaskPlannerReconciler) supersedeInTx(ctx context.Context, tx pgx.Tx, tenantID string, item terminalDailyPlannerRun, version uint64, now time.Time) error {
	tag, err := tx.Exec(ctx, `UPDATE product.daily_task_generations SET version=version+1,status='superseded',failure_reason='focus_or_route_changed',completed_at=$1,updated_at=$1 WHERE tenant_id=$2 AND id=$3 AND version=$4 AND status='generating' AND planner_run_id=$5`, now, tenantID, item.GenerationID, version, item.RunID)
	if err != nil || tag.RowsAffected() != 1 {
		return ErrRouteConflict
	}
	return nil
}

func (reconciler DailyTaskPlannerReconciler) lockGenerationState(ctx context.Context, tx pgx.Tx, tenantID string, item terminalDailyPlannerRun) (uint64, bool, bool, error) {
	var version, focusVersion, routeVersion uint64
	var status, missionStatus, currentRouteID, focusedMissionID, routeStatus string
	err := tx.QueryRow(ctx, `SELECT g.version,g.status,m.status,m.route_version,COALESCE(m.current_route_revision_id::text,''),COALESCE(f.mission_id::text,''),COALESCE(f.focus_version,0),r.status FROM product.daily_task_generations g JOIN product.missions m ON m.tenant_id=g.tenant_id AND m.id=g.mission_id JOIN product.route_revisions r ON r.tenant_id=g.tenant_id AND r.id=g.route_revision_id LEFT JOIN product.mission_focuses f ON f.tenant_id=g.tenant_id AND f.user_id=g.user_id WHERE g.tenant_id=$1 AND g.id=$2 FOR UPDATE OF g,m,r`, tenantID, item.GenerationID).Scan(&version, &status, &missionStatus, &routeVersion, &currentRouteID, &focusedMissionID, &focusVersion, &routeStatus)
	if err != nil {
		return 0, false, false, err
	}
	terminal := status != "generating"
	drifted := missionStatus != "active" || routeVersion != item.InputRouteVersion() || currentRouteID != item.RouteRevisionID || focusedMissionID != item.MissionID || focusVersion != item.FocusVersion || routeStatus != "accepted"
	return version, terminal, drifted, nil
}

func (item terminalDailyPlannerRun) InputRouteVersion() uint64 {
	manifest, err := decodeDailyTaskInputManifest(item.InputManifest)
	if err != nil {
		return 0
	}
	return manifest.Command.ExpectedRouteVersion
}

func (reconciler DailyTaskPlannerReconciler) capabilitiesAllowed(ctx context.Context, tenantID, routeRevisionID string, manifest dailyTaskInputManifest, selected []string) bool {
	encoded, err := reconciler.Payloads.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: routeRevisionID, Class: routePayloadClass, ContentType: "application/json"}, payload.Manifest{Ref: manifest.RoutePayloadRef, Hash: manifest.RoutePayloadHash})
	if err != nil {
		return false
	}
	document, err := productroute.DecodeDocument(encoded)
	if err != nil {
		return false
	}
	allowed := map[string]bool{}
	for _, item := range document.TransferableExperience {
		for _, id := range item.CapabilityIDs {
			allowed[id] = true
		}
	}
	for _, item := range document.Gaps {
		for _, id := range item.CapabilityIDs {
			allowed[id] = true
		}
	}
	for _, item := range document.Bridge {
		for _, id := range item.FromCapabilityIDs {
			allowed[id] = true
		}
		for _, id := range item.ToCapabilityIDs {
			allowed[id] = true
		}
	}
	for _, item := range document.Stages {
		for _, id := range item.CapabilityIDs {
			allowed[id] = true
		}
	}
	for _, id := range document.FirstTask.CapabilityIDs {
		allowed[id] = true
	}
	for _, id := range selected {
		if !allowed[id] {
			return false
		}
	}
	return true
}

func decodeDailyTaskInputManifest(encoded []byte) (dailyTaskInputManifest, error) {
	var manifest dailyTaskInputManifest
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || manifest.SchemaVersion != 2 || manifest.Command.SchemaVersion != 1 || manifest.RoutePayloadRef == "" || manifest.RoutePayloadHash == "" || manifest.AgentProfile.Profile != "daily_planner" || manifest.AgentProfile.ChannelID == "" || manifest.AgentProfile.Sequence == 0 || manifest.AgentProfile.SnapshotID == "" || manifest.ContentSnapshotID == "" {
		return dailyTaskInputManifest{}, ErrDailyTaskCommand
	}
	return manifest, nil
}

func (reconciler DailyTaskPlannerReconciler) loadTerminalRuns(ctx context.Context, tenantID string, limit int) ([]terminalDailyPlannerRun, error) {
	tx, err := reconciler.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT g.id::text,g.user_id::text,g.version,g.mission_id::text,g.route_revision_id::text,g.focus_version,g.scheduled_for,g.difficulty,g.available_minutes,g.input_manifest,g.planner_run_id::text,r.status,e.correlation_id::text FROM product.daily_task_generations g JOIN agent.runs r ON r.tenant_id=g.tenant_id AND r.id=g.planner_run_id JOIN agent.events e ON e.tenant_id=r.tenant_id AND e.aggregate_kind='run' AND e.aggregate_id=r.id AND e.event_type='RunAccepted' WHERE g.tenant_id=$1 AND g.status='generating' AND r.status IN ('succeeded','failed','cancelled','expired') ORDER BY g.updated_at,g.id LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]terminalDailyPlannerRun, 0, limit)
	for rows.Next() {
		var item terminalDailyPlannerRun
		if err = rows.Scan(&item.GenerationID, &item.UserID, &item.GenerationVersion, &item.MissionID, &item.RouteRevisionID, &item.FocusVersion, &item.ScheduledFor, &item.Difficulty, &item.AvailableMinutes, &item.InputManifest, &item.RunID, &item.RunStatus, &item.CorrelationID); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return items, nil
}

func (reconciler DailyTaskPlannerReconciler) loadAssistantMessage(ctx context.Context, tenantID string, item terminalDailyPlannerRun) ([]byte, error) {
	tx, err := reconciler.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return nil, err
	}
	var messageID, ref, manifestHash, contentHash string
	err = tx.QueryRow(ctx, `SELECT id::text,payload_ref,payload_hash,content_hash FROM agent.run_messages WHERE tenant_id=$1 AND user_id=$2 AND run_id=$3 AND role='assistant' ORDER BY message_index DESC,id DESC LIMIT 1`, tenantID, item.UserID, item.RunID).Scan(&messageID, &ref, &manifestHash, &contentHash)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	encoded, err := reconciler.Payloads.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: messageID, Class: "run-message", ContentType: "application/json"}, payload.Manifest{Ref: ref, Hash: manifestHash})
	if err != nil || len(encoded) == 0 || len(encoded) > 2<<20 {
		return nil, errors.Join(err, ErrDailyTaskCommand)
	}
	digest := sha256.Sum256(encoded)
	if hex.EncodeToString(digest[:]) != contentHash {
		return nil, ErrDailyTaskCommand
	}
	var message plannerMessageDocument
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&message) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || message.SchemaVersion != 1 || message.Role != "assistant" || len(message.Content) != 1 || message.Content[0].Type != "text" || message.Content[0].Text == "" {
		return nil, ErrDailyTaskCommand
	}
	return []byte(message.Content[0].Text), nil
}

func (reconciler DailyTaskPlannerReconciler) successIDs(runID string) (string, string, string, string, error) {
	values := make([]string, 4)
	for index, domain := range []string{"daily-task", "daily-task-scheduled-event", "daily-task-scheduled-outbox", "daily-task-scheduled-publish"} {
		value, err := ids.DeterministicUUID(reconciler.IDKey, domain, runID)
		if err != nil {
			return "", "", "", "", err
		}
		values[index] = value
	}
	return values[0], values[1], values[2], values[3], nil
}

func (reconciler DailyTaskPlannerReconciler) putEvent(ctx context.Context, tenantID, objectID string, value any) (PayloadPointer, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return PayloadPointer{}, err
	}
	manifest, err := reconciler.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: objectID, Class: routeEventClass, ContentType: "application/json"}, encoded)
	return PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, err
}

func (reconciler DailyTaskPlannerReconciler) actor() json.RawMessage {
	return json.RawMessage(`{"kind":"service","name":"product-daily-task-planner-reconciler"}`)
}

func (reconciler DailyTaskPlannerReconciler) now() time.Time {
	if reconciler.Now != nil {
		return reconciler.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func (reconciler DailyTaskPlannerReconciler) valid() bool {
	return reconciler.Pool != nil && reconciler.Routes.Pool == reconciler.Pool && reconciler.Payloads != nil && len(reconciler.IDKey) >= 32
}
