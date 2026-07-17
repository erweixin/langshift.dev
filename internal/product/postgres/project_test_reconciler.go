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
	projecttest "github.com/langshift/lites/internal/product/projecttest"
)

type ProjectTestReconciler struct {
	Pool       *pgxpool.Pool
	Store      ProjectStore
	Appender   eventpostgres.Appender
	Payloads   payload.Store
	IDKey      []byte
	StoreEpoch string
	Now        func() time.Time
}

type ProjectTestReconcileResult struct {
	Scanned, Succeeded, Superseded, Failed, Replayed int
}

type terminalProjectTestRun struct {
	GenerationID, UserID, ProjectID, MilestoneID, BindingID             string
	WorkspaceRevision, WorkspaceManifestHash, ValidationKind            string
	InputManifestHash, RunID, RunStatus, CorrelationID                  string
	GenerationVersion, ProjectVersion, MilestoneVersion, BindingVersion uint64
}

func (reconciler ProjectTestReconciler) ListTenantIDs(ctx context.Context, after string, limit int) ([]string, error) {
	if !reconciler.valid() || limit < 1 || limit > 5000 {
		return nil, ErrInvalidProjectCommand
	}
	rows, err := reconciler.Pool.Query(ctx, `SELECT tenant_id::text FROM agent.list_project_test_reconciliation_tenants(NULLIF($1,'')::uuid,$2)`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []string{}
	for rows.Next() {
		var value string
		if err = rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (reconciler ProjectTestReconciler) ReconcileTenant(ctx context.Context, tenantID string, limit int) (ProjectTestReconcileResult, error) {
	items, err := reconciler.load(ctx, tenantID, limit)
	if err != nil {
		return ProjectTestReconcileResult{}, err
	}
	result := ProjectTestReconcileResult{Scanned: len(items)}
	for _, item := range items {
		status, replayed, oneErr := reconciler.one(ctx, tenantID, item)
		if oneErr != nil {
			return result, oneErr
		}
		if replayed {
			result.Replayed++
			continue
		}
		switch status {
		case "succeeded":
			result.Succeeded++
		case "superseded":
			result.Superseded++
		case "failed":
			result.Failed++
		}
	}
	return result, nil
}

func (reconciler ProjectTestReconciler) one(ctx context.Context, tenantID string, item terminalProjectTestRun) (string, bool, error) {
	if item.RunStatus != "succeeded" {
		return reconciler.fail(ctx, tenantID, item, "evaluator_run_"+item.RunStatus)
	}
	encoded, err := (DailyTaskPlannerReconciler{Pool: reconciler.Pool, Payloads: reconciler.Payloads, IDKey: reconciler.IDKey}).loadAssistantMessage(ctx, tenantID, terminalDailyPlannerRun{UserID: item.UserID, RunID: item.RunID})
	if err != nil {
		return reconciler.fail(ctx, tenantID, item, "evaluator_output_invalid")
	}
	document, err := projecttest.Parse(encoded)
	if err != nil {
		return reconciler.fail(ctx, tenantID, item, "evaluator_output_invalid")
	}
	canonical, err := json.Marshal(document)
	if err != nil {
		return "", false, err
	}
	testRunID, evidenceID, mutationID, evidenceEventID, err := reconciler.identifiers(item.RunID)
	if err != nil {
		return "", false, err
	}
	resultManifest, err := json.Marshal(map[string]any{"schema_version": 1, "generation_id": item.GenerationID, "evaluator_run_id": item.RunID, "input_manifest_hash": item.InputManifestHash, "result": document.Result, "summary": document.Summary, "checks": document.Checks, "uncertainty": document.Uncertainty})
	if err != nil {
		return "", false, err
	}
	evidenceBody, err := json.Marshal(map[string]any{"schema_version": 1, "project_id": item.ProjectID, "milestone_id": item.MilestoneID, "workspace_revision": item.WorkspaceRevision, "validation_kind": item.ValidationKind, "generation_id": item.GenerationID, "evaluator_run_id": item.RunID, "input_manifest_hash": item.InputManifestHash, "evaluation": json.RawMessage(canonical)})
	if err != nil {
		return "", false, err
	}
	evidencePayload, err := reconciler.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: evidenceID, Class: "product-evidence", ContentType: "application/json"}, evidenceBody)
	if err != nil {
		return "", false, err
	}
	evidenceEventPayload, err := reconciler.putEvent(ctx, tenantID, evidenceEventID, map[string]any{"subject_id": evidenceID, "subject_version": 1, "payload_ref": evidencePayload.Ref, "claim_set_hash": item.InputManifestHash})
	if err != nil {
		return "", false, err
	}
	projectEventIDs, err := reconciler.Store.projectEventIDs("project-test-run-recorded", mutationID)
	if err != nil {
		return "", false, err
	}
	projectEventPayload, err := reconciler.putEvent(ctx, tenantID, projectEventIDs.event, map[string]any{"subject_id": item.ProjectID, "subject_version": item.ProjectVersion + 1, "command_id": item.RunID})
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
	var generationVersion, projectVersion, milestoneVersion, bindingVersion uint64
	var generationStatus, projectStatus, milestoneStatus, workspaceRevision, workspaceHash string
	err = tx.QueryRow(ctx, `SELECT g.version,g.status,p.version,p.status,pm.version,pm.status,w.version,w.head_revision,w.binding_manifest_hash FROM product.project_test_generations g JOIN product.projects p ON p.tenant_id=g.tenant_id AND p.id=g.project_id JOIN product.project_milestones pm ON pm.tenant_id=g.tenant_id AND pm.id=g.milestone_id JOIN product.project_workspace_bindings w ON w.tenant_id=g.tenant_id AND w.id=g.workspace_binding_id WHERE g.tenant_id=$1 AND g.id=$2 FOR UPDATE OF g,p,pm,w`, tenantID, item.GenerationID).Scan(&generationVersion, &generationStatus, &projectVersion, &projectStatus, &milestoneVersion, &milestoneStatus, &bindingVersion, &workspaceRevision, &workspaceHash)
	if err != nil {
		return "", false, err
	}
	if generationStatus != "generating" {
		return "", true, tx.Commit(ctx)
	}
	if projectVersion != item.ProjectVersion || projectStatus != "active" && projectStatus != "blocked" || milestoneVersion != item.MilestoneVersion || milestoneStatus != "submitted" && milestoneStatus != "rework" || bindingVersion != item.BindingVersion || workspaceRevision != item.WorkspaceRevision || workspaceHash != item.WorkspaceManifestHash {
		tag, updateErr := tx.Exec(ctx, `UPDATE product.project_test_generations SET version=version+1,status='superseded',failure_reason='project_state_changed',completed_at=$1,updated_at=$1 WHERE tenant_id=$2 AND id=$3 AND version=$4 AND status='generating'`, now, tenantID, item.GenerationID, generationVersion)
		if updateErr != nil || tag.RowsAffected() != 1 {
			return "", false, ErrProjectConflict
		}
		return "superseded", false, tx.Commit(ctx)
	}
	var missionID string
	if err = tx.QueryRow(ctx, `SELECT mission_id::text FROM product.projects WHERE tenant_id=$1 AND id=$2`, tenantID, item.ProjectID).Scan(&missionID); err != nil {
		return "", false, err
	}
	plaintextHash := sha256.Sum256(evidenceBody)
	_, err = tx.Exec(ctx, `INSERT INTO product.evidence(id,tenant_id,user_id,version,mission_id,evidence_type,status,source_kind,source_id,payload_ref,content_hash,plaintext_hash,recorded_at,created_at,updated_at) VALUES($1,$2,$3,1,$4,'project_test','verified','evaluator_run',$5,$6,$7,$8,$9,$9,$9)`, evidenceID, tenantID, item.UserID, missionID, item.RunID, evidencePayload.Ref, evidencePayload.Hash, hex.EncodeToString(plaintextHash[:]), now)
	if err != nil {
		return "", false, err
	}
	if err = reconciler.appendEvidenceEvent(ctx, tx, tenantID, item, evidenceEventID, evidenceEventPayload); err != nil {
		return "", false, err
	}
	store := reconciler.Store
	store.Tx = tx
	store.EpochVerified = true
	stored, err := store.RecordTestRun(ctx, RecordProjectTestRunCommand{MutationID: mutationID, TestRunID: testRunID, ProjectID: item.ProjectID, MilestoneID: item.MilestoneID, TenantID: tenantID, UserID: item.UserID, WorkspaceRevision: item.WorkspaceRevision, ValidationKind: item.ValidationKind, Result: document.Result, EvidenceID: evidenceID, CorrelationID: item.CorrelationID, ExpectedProjectVersion: item.ProjectVersion, ResultManifest: resultManifest, Actor: reconciler.actor(), RecordedEvent: projectEventPayload})
	if err != nil || stored.Version != item.ProjectVersion+1 {
		if err != nil {
			return "", false, err
		}
		return "", false, ErrProjectConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE product.project_test_generations SET version=version+1,status='succeeded',project_test_run_id=$1,evidence_id=$2,completed_at=$3,updated_at=$3 WHERE tenant_id=$4 AND id=$5 AND version=$6 AND status='generating'`, testRunID, evidenceID, now, tenantID, item.GenerationID, generationVersion)
	if err != nil || tag.RowsAffected() != 1 {
		return "", false, ErrProjectConflict
	}
	return "succeeded", false, tx.Commit(ctx)
}

func (reconciler ProjectTestReconciler) fail(ctx context.Context, tenantID string, item terminalProjectTestRun, reason string) (string, bool, error) {
	tx, err := reconciler.Pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return "", false, err
	}
	tag, err := tx.Exec(ctx, `UPDATE product.project_test_generations SET version=version+1,status='failed',failure_reason=$1,completed_at=$2,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND status='generating'`, reason, reconciler.now(), tenantID, item.GenerationID)
	if err != nil {
		return "", false, err
	}
	if tag.RowsAffected() == 0 {
		return "", true, tx.Commit(ctx)
	}
	return "failed", false, tx.Commit(ctx)
}

func (reconciler ProjectTestReconciler) load(ctx context.Context, tenantID string, limit int) ([]terminalProjectTestRun, error) {
	if !reconciler.valid() || tenantID == "" || limit < 1 || limit > 500 {
		return nil, ErrInvalidProjectCommand
	}
	tx, err := reconciler.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT g.id::text,g.user_id::text,g.version,g.project_id::text,g.project_version,g.milestone_id::text,g.milestone_version,g.workspace_binding_id::text,g.workspace_binding_version,g.workspace_revision,g.workspace_manifest_hash,g.validation_kind,g.input_manifest_hash,g.evaluator_run_id::text,r.status,e.correlation_id::text FROM product.project_test_generations g JOIN agent.runs r ON r.tenant_id=g.tenant_id AND r.id=g.evaluator_run_id JOIN agent.events e ON e.tenant_id=r.tenant_id AND e.aggregate_kind='run' AND e.aggregate_id=r.id AND e.event_type='RunAccepted' WHERE g.tenant_id=$1 AND g.status='generating' AND r.status IN ('succeeded','failed','cancelled','expired') ORDER BY g.updated_at,g.id LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []terminalProjectTestRun{}
	for rows.Next() {
		var value terminalProjectTestRun
		if err = rows.Scan(&value.GenerationID, &value.UserID, &value.GenerationVersion, &value.ProjectID, &value.ProjectVersion, &value.MilestoneID, &value.MilestoneVersion, &value.BindingID, &value.BindingVersion, &value.WorkspaceRevision, &value.WorkspaceManifestHash, &value.ValidationKind, &value.InputManifestHash, &value.RunID, &value.RunStatus, &value.CorrelationID); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return values, nil
}

func (reconciler ProjectTestReconciler) putEvent(ctx context.Context, tenantID, objectID string, value any) (PayloadPointer, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return PayloadPointer{}, err
	}
	manifest, err := reconciler.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: objectID, Class: projectEventClass, ContentType: "application/json"}, encoded)
	return PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, err
}

func (reconciler ProjectTestReconciler) appendEvidenceEvent(ctx context.Context, tx pgx.Tx, tenantID string, item terminalProjectTestRun, eventID string, pointer PayloadPointer) error {
	outboxID, err := ids.DeterministicUUID(reconciler.IDKey, "project-test-evidence-event-outbox", eventID)
	if err != nil {
		return err
	}
	publishID, err := ids.DeterministicUUID(reconciler.IDKey, "project-test-evidence-event-publish", eventID)
	if err != nil {
		return err
	}
	_, err = reconciler.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: tenantID, UserID: item.UserID, EventType: "CapabilityEvidenceRecorded", SchemaVersion: 1, AggregateKind: "evidence", AggregateID: mustProjectTestID(reconciler.IDKey, "project-test-evidence", item.RunID), AggregateVersion: 1, StoreEpoch: reconciler.StoreEpoch, OccurredAt: reconciler.now(), Actor: reconciler.actor(), CorrelationID: item.CorrelationID, PayloadRef: pointer.Ref, PayloadHash: pointer.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishID, CommandType: "events.publish", PayloadRef: pointer.Ref, PayloadHash: pointer.Hash}}})
	return err
}

func (reconciler ProjectTestReconciler) identifiers(runID string) (string, string, string, string, error) {
	domains := []string{"project-test-result", "project-test-evidence", "project-test-record-mutation", "project-test-evidence-event"}
	values := make([]string, len(domains))
	for index, domain := range domains {
		value, err := ids.DeterministicUUID(reconciler.IDKey, domain, runID)
		if err != nil {
			return "", "", "", "", err
		}
		values[index] = value
	}
	return values[0], values[1], values[2], values[3], nil
}

func mustProjectTestID(key []byte, domain, seed string) string {
	value, _ := ids.DeterministicUUID(key, domain, seed)
	return value
}

func (reconciler ProjectTestReconciler) actor() json.RawMessage {
	return json.RawMessage(`{"kind":"service","service":"product-worker","component":"project-test-reconciler"}`)
}

func (reconciler ProjectTestReconciler) now() time.Time {
	if reconciler.Now != nil {
		return reconciler.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func (reconciler ProjectTestReconciler) valid() bool {
	return reconciler.Pool != nil && reconciler.Store.Pool == reconciler.Pool && reconciler.Payloads != nil && len(reconciler.IDKey) >= 32 && reconciler.StoreEpoch != ""
}
