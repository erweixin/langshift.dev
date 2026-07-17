package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	productreview "github.com/langshift/lites/internal/product/review"
	producttask "github.com/langshift/lites/internal/product/task"
)

type ReviewReconciler struct {
	Pool       *pgxpool.Pool
	Appender   eventpostgres.Appender
	Payloads   payload.Store
	IDKey      []byte
	StoreEpoch string
	Now        func() time.Time
}
type ReviewReconcileResult struct{ Scanned, Succeeded, Superseded, Failed, Replayed int }
type terminalReviewRun struct {
	GenerationID, UserID, TaskID, SubmissionID, RubricID, RunID, RunStatus, CorrelationID string
	GenerationVersion, TaskVersion                                                        uint64
	SubmissionRevision                                                                    int
}

func (r ReviewReconciler) ListTenantIDs(ctx context.Context, after string, limit int) ([]string, error) {
	if !r.valid() || limit < 1 || limit > 5000 {
		return nil, ErrInvalidCommand
	}
	rows, e := r.Pool.Query(ctx, `SELECT tenant_id::text FROM agent.list_submission_review_reconciliation_tenants(NULLIF($1,'')::uuid,$2)`, after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
func (r ReviewReconciler) ReconcileTenant(ctx context.Context, tenant string, limit int) (ReviewReconcileResult, error) {
	items, e := r.load(ctx, tenant, limit)
	if e != nil {
		return ReviewReconcileResult{}, e
	}
	out := ReviewReconcileResult{Scanned: len(items)}
	for _, item := range items {
		status, replayed, e := r.one(ctx, tenant, item)
		if e != nil {
			return out, e
		}
		if replayed {
			out.Replayed++
			continue
		}
		switch status {
		case "succeeded":
			out.Succeeded++
		case "superseded":
			out.Superseded++
		case "failed":
			out.Failed++
		}
	}
	return out, nil
}
func (r ReviewReconciler) one(ctx context.Context, tenant string, item terminalReviewRun) (string, bool, error) {
	if item.RunStatus != "succeeded" {
		return r.fail(ctx, tenant, item, "evaluator_run_"+item.RunStatus)
	}
	encoded, e := (DailyTaskPlannerReconciler{Pool: r.Pool, Payloads: r.Payloads, IDKey: r.IDKey}).loadAssistantMessage(ctx, tenant, terminalDailyPlannerRun{UserID: item.UserID, RunID: item.RunID})
	if e != nil {
		return r.fail(ctx, tenant, item, "evaluator_output_invalid")
	}
	document, e := productreview.Parse(encoded)
	if e != nil {
		return r.fail(ctx, tenant, item, "evaluator_output_invalid")
	}
	if !r.capabilitiesAllowed(ctx, tenant, item, document) {
		return r.fail(ctx, tenant, item, "evaluator_output_invalid")
	}
	canonical, e := json.Marshal(document)
	if e != nil {
		return "", false, e
	}
	reviewID, evidenceID, reviewEvent, evidenceEvent, completeEvent, e := r.ids(item.RunID)
	if e != nil {
		return "", false, e
	}
	reviewPayload, e := r.Payloads.Put(ctx, payload.Descriptor{TenantID: tenant, ObjectID: reviewID, Class: "product-review", ContentType: "application/json"}, canonical)
	if e != nil {
		return "", false, e
	}
	evidenceBody, _ := json.Marshal(map[string]any{"schema_version": 1, "review_id": reviewID, "submission_id": item.SubmissionID, "verdict": document.Verdict, "capability_evidence": document.CapabilityEvidence, "review_payload_hash": reviewPayload.Hash})
	evidencePayload, e := r.Payloads.Put(ctx, payload.Descriptor{TenantID: tenant, ObjectID: evidenceID, Class: "product-evidence", ContentType: "application/json"}, evidenceBody)
	if e != nil {
		return "", false, e
	}
	deterministic, _ := json.Marshal(document.DeterministicResults)
	detHash := sha256.Sum256(deterministic)
	now := r.now()
	tx, e := r.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if e != nil {
		return "", false, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, e = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenant); e != nil {
		return "", false, e
	}
	var generationVersion, taskVersion uint64
	var generationStatus, taskStatus, currentSubmission string
	var currentReview *string
	e = tx.QueryRow(ctx, `SELECT g.version,g.status,d.version,d.status,COALESCE(d.current_submission_id::text,''),d.current_review_id::text FROM product.submission_review_generations g JOIN product.daily_tasks d ON d.tenant_id=g.tenant_id AND d.id=g.daily_task_id WHERE g.tenant_id=$1 AND g.id=$2 FOR UPDATE OF g,d`, tenant, item.GenerationID).Scan(&generationVersion, &generationStatus, &taskVersion, &taskStatus, &currentSubmission, &currentReview)
	if e != nil {
		return "", false, e
	}
	if generationStatus != "generating" {
		return "", true, tx.Commit(ctx)
	}
	if taskStatus != "submitted" || taskVersion != item.TaskVersion || currentSubmission != item.SubmissionID || currentReview != nil {
		tag, er := tx.Exec(ctx, `UPDATE product.submission_review_generations SET version=version+1,status='superseded',failure_reason='task_state_changed',completed_at=$1,updated_at=$1 WHERE tenant_id=$2 AND id=$3 AND version=$4 AND status='generating'`, now, tenant, item.GenerationID, generationVersion)
		if er != nil || tag.RowsAffected() != 1 {
			return "", false, ErrRouteConflict
		}
		return "superseded", false, tx.Commit(ctx)
	}
	var missionID string
	e = tx.QueryRow(ctx, `SELECT mission_id::text FROM product.daily_tasks WHERE tenant_id=$1 AND id=$2`, tenant, item.TaskID).Scan(&missionID)
	if e != nil {
		return "", false, e
	}
	evidencePlaintextHash := sha256.Sum256(evidenceBody)
	_, e = tx.Exec(ctx, `INSERT INTO product.evidence(id,tenant_id,user_id,version,mission_id,evidence_type,status,source_kind,source_id,payload_ref,content_hash,plaintext_hash,recorded_at,created_at,updated_at) VALUES($1,$2,$3,1,$4,'submission_review','verified','review',$5,$6,$7,$8,$9,$9,$9)`, evidenceID, tenant, item.UserID, missionID, reviewID, evidencePayload.Ref, evidencePayload.Hash, hex.EncodeToString(evidencePlaintextHash[:]), now)
	if e != nil {
		return "", false, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO product.reviews(id,tenant_id,user_id,version,submission_id,rubric_version_id,status,deterministic_results,review_payload_ref,evidence_id,reviewed_at,created_at,updated_at,generation_id,daily_task_id,submission_revision,deterministic_results_hash,review_payload_hash) VALUES($1,$2,$3,1,$4,$5,'completed',$6,$7,$8,$9,$9,$9,$10,$11,$12,$13,$14)`, reviewID, tenant, item.UserID, item.SubmissionID, item.RubricID, deterministic, reviewPayload.Ref, evidenceID, now, item.GenerationID, item.TaskID, item.SubmissionRevision, hex.EncodeToString(detHash[:]), reviewPayload.Hash)
	if e != nil {
		return "", false, e
	}
	tag, e := tx.Exec(ctx, `UPDATE product.daily_tasks SET version=version+2,status='completed',current_review_id=$1,completed_at=$2,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND version=$5 AND status='submitted' AND current_submission_id=$6`, reviewID, now, tenant, item.TaskID, item.TaskVersion, item.SubmissionID)
	if e != nil || tag.RowsAffected() != 1 {
		return "", false, ErrRouteConflict
	}
	tag, e = tx.Exec(ctx, `UPDATE product.submission_review_generations SET version=version+1,status='succeeded',review_id=$1,evidence_id=$2,completed_at=$3,updated_at=$3 WHERE tenant_id=$4 AND id=$5 AND version=$6 AND status='generating'`, reviewID, evidenceID, now, tenant, item.GenerationID, generationVersion)
	if e != nil || tag.RowsAffected() != 1 {
		return "", false, ErrRouteConflict
	}
	if e = r.append(ctx, tx, tenant, item, reviewEvent, "SubmissionReviewCompleted", "daily_task", item.TaskID, item.TaskVersion+1, map[string]any{"review_id": reviewID, "submission_id": item.SubmissionID, "verdict": document.Verdict, "subject_version": item.TaskVersion + 1}); e != nil {
		return "", false, e
	}
	if e = r.append(ctx, tx, tenant, item, evidenceEvent, "CapabilityEvidenceRecorded", "evidence", evidenceID, 1, map[string]any{"evidence_id": evidenceID, "review_id": reviewID, "content_hash": evidencePayload.Hash, "subject_version": 1}); e != nil {
		return "", false, e
	}
	if e = r.append(ctx, tx, tenant, item, completeEvent, "DailyTaskCompleted", "daily_task", item.TaskID, item.TaskVersion+2, map[string]any{"review_id": reviewID, "evidence_id": evidenceID, "subject_version": item.TaskVersion + 2}); e != nil {
		return "", false, e
	}
	return "succeeded", false, tx.Commit(ctx)
}

func (r ReviewReconciler) capabilitiesAllowed(ctx context.Context, tenant string, item terminalReviewRun, document productreview.Document) bool {
	tx, err := r.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return false
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenant); err != nil {
		return false
	}
	var ref, hash string
	if err = tx.QueryRow(ctx, `SELECT task_payload_ref,task_payload_hash FROM product.daily_tasks WHERE tenant_id=$1 AND user_id=$2 AND id=$3`, tenant, item.UserID, item.TaskID).Scan(&ref, &hash); err != nil {
		return false
	}
	if err = tx.Commit(ctx); err != nil {
		return false
	}
	encoded, err := r.Payloads.Get(ctx, payload.Descriptor{TenantID: tenant, ObjectID: item.TaskID, Class: dailyTaskPayloadClass, ContentType: "application/json"}, payload.Manifest{Ref: ref, Hash: hash})
	if err != nil {
		return false
	}
	task, err := producttask.ParseDocument(encoded)
	if err != nil {
		return false
	}
	allowed := map[string]bool{}
	for _, id := range task.CapabilityIDs {
		allowed[id] = true
	}
	for _, evidence := range document.CapabilityEvidence {
		if !allowed[evidence.CapabilityID] {
			return false
		}
	}
	return true
}
func (r ReviewReconciler) fail(ctx context.Context, tenant string, item terminalReviewRun, reason string) (string, bool, error) {
	tx, e := r.Pool.Begin(ctx)
	if e != nil {
		return "", false, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, e = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenant); e != nil {
		return "", false, e
	}
	tag, e := tx.Exec(ctx, `UPDATE product.submission_review_generations SET version=version+1,status='failed',failure_reason=$1,completed_at=$2,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND status='generating'`, reason, r.now(), tenant, item.GenerationID)
	if e != nil {
		return "", false, e
	}
	if tag.RowsAffected() == 0 {
		return "", true, tx.Commit(ctx)
	}
	return "failed", false, tx.Commit(ctx)
}
func (r ReviewReconciler) load(ctx context.Context, tenant string, limit int) ([]terminalReviewRun, error) {
	if !r.valid() || tenant == "" || limit < 1 || limit > 500 {
		return nil, ErrInvalidCommand
	}
	tx, e := r.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, e = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenant); e != nil {
		return nil, e
	}
	rows, e := tx.Query(ctx, `SELECT g.id::text,g.user_id::text,g.version,g.daily_task_id::text,g.task_version,g.submission_id::text,g.submission_revision,g.rubric_version_id::text,g.review_run_id::text,r.status,e.correlation_id::text FROM product.submission_review_generations g JOIN agent.runs r ON r.tenant_id=g.tenant_id AND r.id=g.review_run_id JOIN agent.events e ON e.tenant_id=r.tenant_id AND e.aggregate_kind='run' AND e.aggregate_id=r.id AND e.event_type='RunAccepted' WHERE g.tenant_id=$1 AND g.status='generating' AND r.status IN ('succeeded','failed','cancelled','expired') ORDER BY g.updated_at,g.id LIMIT $2`, tenant, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []terminalReviewRun{}
	for rows.Next() {
		var v terminalReviewRun
		if e = rows.Scan(&v.GenerationID, &v.UserID, &v.GenerationVersion, &v.TaskID, &v.TaskVersion, &v.SubmissionID, &v.SubmissionRevision, &v.RubricID, &v.RunID, &v.RunStatus, &v.CorrelationID); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}
func (r ReviewReconciler) append(ctx context.Context, tx pgx.Tx, tenant string, item terminalReviewRun, eventID, eventType, kind, aggregate string, version uint64, body map[string]any) error {
	body["subject_id"] = aggregate
	b, _ := json.Marshal(body)
	m, e := r.Payloads.Put(ctx, payload.Descriptor{TenantID: tenant, ObjectID: eventID, Class: "event-payload", ContentType: "application/json"}, b)
	if e != nil {
		return e
	}
	outbox, _ := ids.DeterministicUUID(r.IDKey, "review-event-outbox", eventID)
	publish, _ := ids.DeterministicUUID(r.IDKey, "review-event-publish", eventID)
	_, e = r.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: tenant, UserID: item.UserID, EventType: eventType, SchemaVersion: 1, AggregateKind: kind, AggregateID: aggregate, AggregateVersion: version, StoreEpoch: r.StoreEpoch, OccurredAt: r.now(), Actor: r.actor(), CorrelationID: item.CorrelationID, PayloadRef: m.Ref, PayloadHash: m.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outbox, CommandID: publish, CommandType: "events.publish", PayloadRef: m.Ref, PayloadHash: m.Hash}}})
	return e
}
func (r ReviewReconciler) ids(seed string) (string, string, string, string, string, error) {
	d := []string{"review", "review-evidence", "review-completed-event", "review-evidence-event", "review-task-completed-event"}
	v := make([]string, 5)
	for i, x := range d {
		n, e := ids.DeterministicUUID(r.IDKey, x, seed)
		if e != nil {
			return "", "", "", "", "", e
		}
		v[i] = n
	}
	return v[0], v[1], v[2], v[3], v[4], nil
}
func (r ReviewReconciler) actor() json.RawMessage {
	return json.RawMessage(`{"kind":"service","service":"product-worker","component":"review-reconciler"}`)
}
func (r ReviewReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}
func (r ReviewReconciler) valid() bool {
	return r.Pool != nil && r.Payloads != nil && len(r.IDKey) >= 32 && r.StoreEpoch != ""
}
