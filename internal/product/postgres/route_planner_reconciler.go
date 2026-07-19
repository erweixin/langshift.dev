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
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	productroute "github.com/langshift/lites/internal/product/route"
)

type RoutePlannerReconciler struct {
	Pool     *pgxpool.Pool
	Routes   RouteStore
	Payloads payload.Store
	IDKey    []byte
	Now      func() time.Time
}

type RoutePlannerReconcileResult struct {
	Scanned, Proposed, Stale, Failed, Replayed int
}

type terminalPlannerRun struct {
	RevisionID, UserID, RunID, RunStatus, CorrelationID string
	RevisionVersion                                     uint64
	InputManifest                                       json.RawMessage
}

type plannerMessageDocument struct {
	SchemaVersion int    `json:"schema_version"`
	Role          string `json:"role"`
	Content       []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func (reconciler RoutePlannerReconciler) ListTenantIDs(ctx context.Context, afterTenantID string, limit int) ([]string, error) {
	if !reconciler.valid() || limit < 1 || limit > 5000 {
		return nil, ErrRoutePlannerCommand
	}
	rows, err := reconciler.Pool.Query(ctx, `SELECT tenant_id::text FROM agent.list_route_planner_reconciliation_tenants(NULLIF($1,'')::uuid,$2)`, afterTenantID, limit)
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

func (reconciler RoutePlannerReconciler) ReconcileTenant(ctx context.Context, tenantID string, limit int) (RoutePlannerReconcileResult, error) {
	if !reconciler.valid() || tenantID == "" || limit < 1 || limit > 500 {
		return RoutePlannerReconcileResult{}, ErrRoutePlannerCommand
	}
	items, err := reconciler.loadTerminalRuns(ctx, tenantID, limit)
	if err != nil {
		return RoutePlannerReconcileResult{}, err
	}
	result := RoutePlannerReconcileResult{Scanned: len(items)}
	for _, item := range items {
		if item.RunStatus == "succeeded" {
			outcome, replayed, reconcileErr := reconciler.reconcileSuccess(ctx, tenantID, item)
			if reconcileErr != nil {
				return result, reconcileErr
			}
			if replayed {
				result.Replayed++
			} else if outcome == "stale" {
				result.Stale++
			} else if outcome == "failed" {
				result.Failed++
			} else if outcome == "proposed" {
				result.Proposed++
			} else {
				return result, ErrRoutePlannerCommand
			}
			continue
		}
		replayed, reconcileErr := reconciler.reconcileFailure(ctx, tenantID, item, "planner_run_"+item.RunStatus)
		if reconcileErr != nil {
			return result, reconcileErr
		}
		if replayed {
			result.Replayed++
		} else {
			result.Failed++
		}
	}
	return result, nil
}

func (reconciler RoutePlannerReconciler) reconcileSuccess(ctx context.Context, tenantID string, item terminalPlannerRun) (string, bool, error) {
	encoded, err := reconciler.loadAssistantMessage(ctx, tenantID, item)
	if err != nil {
		replayed, failErr := reconciler.reconcileFailure(ctx, tenantID, item, "planner_output_invalid")
		return "failed", replayed, failErr
	}
	document, err := productroute.DecodeDocument(encoded)
	if err != nil {
		replayed, failErr := reconciler.reconcileFailure(ctx, tenantID, item, "planner_output_invalid")
		return "failed", replayed, failErr
	}
	manifest, err := decodeRouteInputManifest(item.InputManifest)
	if err != nil {
		replayed, failErr := reconciler.reconcileFailure(ctx, tenantID, item, "planner_input_invalid")
		return "failed", replayed, failErr
	}
	if err = productroute.ValidateGrounding(document, routeGroundingInput(manifest)); err != nil {
		replayed, failErr := reconciler.reconcileFailure(ctx, tenantID, item, "planner_output_ungrounded")
		return "failed", replayed, failErr
	}
	canonical, err := json.Marshal(document)
	if err != nil {
		return "", false, err
	}
	routeManifest, err := reconciler.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: item.RevisionID, Class: routePayloadClass, ContentType: "application/json"}, canonical)
	if err != nil {
		return "", false, err
	}
	mutationID, eventObjectID, err := reconciler.reconcileIDs("success", item.RunID)
	if err != nil {
		return "", false, err
	}
	event, err := reconciler.putEvent(ctx, tenantID, eventObjectID, map[string]any{"subject_id": item.RevisionID, "subject_version": item.RevisionVersion + 1, "route_revision_id": item.RevisionID, "planner_run_id": item.RunID, "route_payload_hash": routeManifest.Hash})
	if err != nil {
		return "", false, err
	}
	completed, err := reconciler.Routes.CompleteGeneration(ctx, CompleteRouteGenerationCommand{MutationID: mutationID, RevisionID: item.RevisionID, TenantID: tenantID, UserID: item.UserID, ExpectedRevisionVersion: item.RevisionVersion, RoutePayload: PayloadPointer{Ref: routeManifest.Ref, Hash: routeManifest.Hash}, CompletedEvent: event, CorrelationID: item.CorrelationID, Actor: reconciler.actor()})
	if errors.Is(err, ErrRouteResultStale) {
		return "stale", false, nil
	}
	if errors.Is(err, ErrRouteConflict) {
		terminal, terminalErr := reconciler.revisionTerminal(ctx, tenantID, item.RevisionID)
		return "", terminal, terminalErr
	}
	if err != nil {
		return "", false, err
	}
	if completed.Stale {
		return "stale", false, nil
	}
	return "proposed", false, nil
}

func (reconciler RoutePlannerReconciler) reconcileFailure(ctx context.Context, tenantID string, item terminalPlannerRun, reason string) (bool, error) {
	mutationID, eventObjectID, err := reconciler.reconcileIDs("failure", item.RunID)
	if err != nil {
		return false, err
	}
	event, err := reconciler.putEvent(ctx, tenantID, eventObjectID, map[string]any{"subject_id": item.RevisionID, "subject_version": item.RevisionVersion + 1, "route_revision_id": item.RevisionID, "planner_run_id": item.RunID, "reason": reason})
	if err != nil {
		return false, err
	}
	_, err = reconciler.Routes.FailGeneration(ctx, FailRouteGenerationCommand{MutationID: mutationID, RevisionID: item.RevisionID, TenantID: tenantID, UserID: item.UserID, ExpectedRevisionVersion: item.RevisionVersion, Reason: reason, CorrelationID: item.CorrelationID, Actor: reconciler.actor(), FailedEvent: event})
	if errors.Is(err, ErrRouteConflict) {
		return reconciler.revisionTerminal(ctx, tenantID, item.RevisionID)
	}
	return false, err
}

func (reconciler RoutePlannerReconciler) loadTerminalRuns(ctx context.Context, tenantID string, limit int) ([]terminalPlannerRun, error) {
	tx, err := reconciler.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT r.id::text,r.user_id::text,r.version,r.input_manifest,r.planner_run_id::text,ar.status,e.correlation_id::text
		FROM product.route_revisions r
		JOIN agent.runs ar ON ar.tenant_id=r.tenant_id AND ar.id=r.planner_run_id
		JOIN agent.events e ON e.tenant_id=ar.tenant_id AND e.aggregate_kind='run' AND e.aggregate_id=ar.id AND e.event_type='RunAccepted'
		WHERE r.tenant_id=$1 AND r.status='generating' AND ar.status IN ('succeeded','failed','cancelled','expired')
		ORDER BY r.updated_at,r.id LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]terminalPlannerRun, 0, limit)
	for rows.Next() {
		var item terminalPlannerRun
		if err = rows.Scan(&item.RevisionID, &item.UserID, &item.RevisionVersion, &item.InputManifest, &item.RunID, &item.RunStatus, &item.CorrelationID); err != nil {
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

func routeGroundingInput(manifest routeInputManifest) productroute.GroundingInput {
	result := productroute.GroundingInput{
		Claims:              make([]productroute.GroundingClaim, 0, len(manifest.ClaimRevisions)),
		Evidence:            make([]productroute.GroundingEvidence, 0, len(manifest.EvidenceRevisions)),
		TargetCapabilityIDs: make([]string, 0, len(manifest.TargetRequirements)),
	}
	for _, claim := range manifest.ClaimRevisions {
		result.Claims = append(result.Claims, productroute.GroundingClaim{CapabilityID: claim.CapabilityID, Status: claim.Status, VerificationLevel: claim.VerificationLevel, SupportingEvidenceIDs: append([]string(nil), claim.EvidenceIDs...)})
	}
	for _, evidence := range manifest.EvidenceRevisions {
		result.Evidence = append(result.Evidence, productroute.GroundingEvidence{ID: evidence.ID, Status: evidence.Status, Invalidated: evidence.Invalidated != nil})
	}
	for _, requirement := range manifest.TargetRequirements {
		result.TargetCapabilityIDs = append(result.TargetCapabilityIDs, requirement.CapabilityID)
	}
	return result
}

func (reconciler RoutePlannerReconciler) loadAssistantMessage(ctx context.Context, tenantID string, item terminalPlannerRun) ([]byte, error) {
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
		return nil, errors.Join(err, ErrRoutePlannerCommand)
	}
	digest := sha256.Sum256(encoded)
	if hex.EncodeToString(digest[:]) != contentHash {
		return nil, ErrRoutePlannerCommand
	}
	var message plannerMessageDocument
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&message) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || message.SchemaVersion != 1 || message.Role != "assistant" || len(message.Content) != 1 || message.Content[0].Type != "text" || message.Content[0].Text == "" {
		return nil, ErrRoutePlannerCommand
	}
	return []byte(message.Content[0].Text), nil
}

func (reconciler RoutePlannerReconciler) revisionTerminal(ctx context.Context, tenantID, revisionID string) (bool, error) {
	tx, err := reconciler.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return false, err
	}
	var terminal bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product.route_revisions WHERE tenant_id=$1 AND id=$2 AND status IN ('proposed','stale','failed','accepted','superseded'))`, tenantID, revisionID).Scan(&terminal); err != nil {
		return false, err
	}
	return terminal, tx.Commit(ctx)
}

func (reconciler RoutePlannerReconciler) reconcileIDs(kind, runID string) (string, string, error) {
	mutationID, err := ids.DeterministicUUID(reconciler.IDKey, "route-planner-reconcile:"+kind, runID)
	if err != nil {
		return "", "", err
	}
	eventObjectID, err := ids.DeterministicUUID(reconciler.IDKey, "route-planner-reconcile-event:"+kind, runID)
	return mutationID, eventObjectID, err
}

func (reconciler RoutePlannerReconciler) putEvent(ctx context.Context, tenantID, objectID string, value any) (PayloadPointer, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return PayloadPointer{}, err
	}
	manifest, err := reconciler.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: objectID, Class: routeEventClass, ContentType: "application/json"}, encoded)
	return PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, err
}

func (reconciler RoutePlannerReconciler) actor() json.RawMessage {
	return json.RawMessage(`{"kind":"service","name":"product-route-planner-reconciler"}`)
}

func (reconciler RoutePlannerReconciler) valid() bool {
	return reconciler.Pool != nil && reconciler.Routes.Pool == reconciler.Pool && reconciler.Payloads != nil && len(reconciler.IDKey) >= 32
}
