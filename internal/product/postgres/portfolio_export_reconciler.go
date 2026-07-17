package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/payload"
)

var (
	ErrPortfolioExportReceipt = errors.New("portfolio export trusted tool receipt is invalid")
	portfolioDigestRE         = regexp.MustCompile(`^[0-9a-f]{64}$`)
	portfolioObjectRefRE      = regexp.MustCompile(`^s3://[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]/[A-Za-z0-9][A-Za-z0-9._/-]*$`)
)

type PortfolioExportReconciler struct {
	Pool       *pgxpool.Pool
	Store      PortfolioExportStore
	Appender   eventpostgres.Appender
	Payloads   payload.Store
	IDKey      []byte
	StoreEpoch string
	Retention  time.Duration
	Now        func() time.Time
}

type PortfolioExportReconcileResult struct{ Scanned, Ready, Failed, Replayed, Pending int }

type portfolioFinalizationCandidate struct {
	ExportID, UserID, RunID, ExportStatus, RunStatus, Format, CorrelationID string
	ExportVersion                                                           uint64
}

type portfolioToolCandidate struct {
	ToolCallID, ResultEventID, EventRef, EventHash, OriginalAttemptID string
	ReconciliationAttemptID, ExternalResourceRef                      string
}

type portfolioReceipt struct {
	SchemaVersion  int    `json:"schema_version"`
	TargetKind     string `json:"target_kind"`
	TargetID       string `json:"target_id"`
	ObjectRef      string `json:"object_ref"`
	ObjectVersion  string `json:"object_version"`
	ContentHash    string `json:"content_hash"`
	MediaType      string `json:"media_type"`
	ByteSize       int64  `json:"byte_size"`
	ScanResultHash string `json:"scan_result_hash"`
}

func (reconciler PortfolioExportReconciler) ListTenantIDs(ctx context.Context, after string, limit int) ([]string, error) {
	if !reconciler.valid() || limit < 1 || limit > 5000 {
		return nil, ErrConfiguration
	}
	if err := reconciler.Store.requireEpoch(ctx); err != nil {
		return nil, err
	}
	rows, err := reconciler.Pool.Query(ctx, `SELECT tenant_id FROM agent.list_portfolio_export_finalization_tenants($1::uuid,NULLIF($2,'')::uuid,$3)`, reconciler.StoreEpoch, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []string{}
	for rows.Next() {
		var tenantID string
		if err = rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		values = append(values, tenantID)
	}
	return values, rows.Err()
}

func (reconciler PortfolioExportReconciler) ReconcileTenant(ctx context.Context, tenantID string, limit int) (PortfolioExportReconcileResult, error) {
	if !reconciler.valid() || tenantID == "" || limit < 1 || limit > 500 {
		return PortfolioExportReconcileResult{}, ErrConfiguration
	}
	if err := reconciler.Store.requireEpoch(ctx); err != nil {
		return PortfolioExportReconcileResult{}, err
	}
	items, err := reconciler.load(ctx, tenantID, limit)
	if err != nil {
		return PortfolioExportReconcileResult{}, err
	}
	result := PortfolioExportReconcileResult{Scanned: len(items)}
	for _, item := range items {
		status, replayed, err := reconciler.one(ctx, tenantID, item)
		if err != nil {
			return result, err
		}
		if replayed {
			result.Replayed++
			continue
		}
		switch status {
		case "ready":
			result.Ready++
		case "failed":
			result.Failed++
		default:
			result.Pending++
		}
	}
	return result, nil
}

func (reconciler PortfolioExportReconciler) one(ctx context.Context, tenantID string, item portfolioFinalizationCandidate) (string, bool, error) {
	tool, count, err := reconciler.loadToolCandidate(ctx, tenantID, item)
	if err != nil {
		return "", false, err
	}
	var receipt *portfolioReceipt
	failureCode := ""
	if count > 1 {
		failureCode = "artifact_export_ambiguous"
	} else if count == 1 {
		value, loadErr := reconciler.loadReceipt(ctx, tenantID, item, tool)
		if loadErr != nil {
			if errors.Is(loadErr, ErrPortfolioExportReceipt) {
				failureCode = "artifact_export_receipt_invalid"
			} else {
				return "", false, loadErr
			}
		} else {
			receipt = &value
		}
	}
	if receipt == nil && failureCode == "" {
		if !terminalRunStatus(item.RunStatus) {
			return "pending", false, nil
		}
		failureCode = "artifact_builder_run_" + item.RunStatus
	}
	return reconciler.commit(ctx, tenantID, item, tool, receipt, failureCode)
}

func (reconciler PortfolioExportReconciler) loadToolCandidate(ctx context.Context, tenantID string, item portfolioFinalizationCandidate) (portfolioToolCandidate, int, error) {
	tx, err := reconciler.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return portfolioToolCandidate{}, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return portfolioToolCandidate{}, 0, err
	}
	var count int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM agent.tool_calls t JOIN agent.tool_effects e ON e.tenant_id=t.tenant_id AND e.tool_call_id=t.id JOIN agent.events ev ON ev.tenant_id=t.tenant_id AND ev.id=t.result_event_id AND ev.store_epoch=$3 WHERE t.tenant_id=$1 AND t.run_id=$2 AND t.tool_name='artifact_export' AND t.status='succeeded' AND e.status='confirmed' AND NULLIF(e.external_resource_ref,'') IS NOT NULL`, tenantID, item.RunID, reconciler.StoreEpoch).Scan(&count)
	if err != nil || count == 0 {
		if err == nil {
			err = tx.Commit(ctx)
		}
		return portfolioToolCandidate{}, count, err
	}
	var value portfolioToolCandidate
	err = tx.QueryRow(ctx, `SELECT t.id::text,t.result_event_id::text,ev.payload_ref,ev.payload_hash,COALESCE(e.execution_attempt_id::text,''),COALESCE((SELECT a.id::text FROM agent.job_attempts a JOIN agent.outbox o ON o.tenant_id=a.tenant_id AND o.command_id=a.command_id AND o.command_type='ReconcileToolEffect' WHERE a.tenant_id=ev.tenant_id AND a.command_id=ev.causation_id AND a.status='succeeded' ORDER BY a.finished_at DESC,a.id DESC LIMIT 1),''),e.external_resource_ref FROM agent.tool_calls t JOIN agent.tool_effects e ON e.tenant_id=t.tenant_id AND e.tool_call_id=t.id JOIN agent.events ev ON ev.tenant_id=t.tenant_id AND ev.id=t.result_event_id WHERE t.tenant_id=$1 AND t.run_id=$2 AND t.tool_name='artifact_export' AND t.status='succeeded' AND e.status='confirmed' AND NULLIF(e.external_resource_ref,'') IS NOT NULL AND ev.store_epoch=$3 ORDER BY ev.occurred_at,ev.id LIMIT 1`, tenantID, item.RunID, reconciler.StoreEpoch).Scan(&value.ToolCallID, &value.ResultEventID, &value.EventRef, &value.EventHash, &value.OriginalAttemptID, &value.ReconciliationAttemptID, &value.ExternalResourceRef)
	if err != nil {
		return portfolioToolCandidate{}, 0, err
	}
	return value, count, tx.Commit(ctx)
}

func (reconciler PortfolioExportReconciler) loadReceipt(ctx context.Context, tenantID string, item portfolioFinalizationCandidate, tool portfolioToolCandidate) (portfolioReceipt, error) {
	manifest := payload.Manifest{Ref: tool.EventRef, Hash: tool.EventHash}
	var encoded []byte
	var err error
	if tool.ReconciliationAttemptID != "" {
		encoded, err = reconciler.Payloads.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: tool.ReconciliationAttemptID + ":tool-reconciled", Class: "event-payload", ContentType: "application/json"}, manifest)
	} else if tool.OriginalAttemptID != "" {
		encoded, err = reconciler.Payloads.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: tool.OriginalAttemptID + ":tool-completed", Class: "event-payload", ContentType: "application/json"}, manifest)
	}
	if err != nil {
		return portfolioReceipt{}, err
	}
	if len(encoded) == 0 {
		return portfolioReceipt{}, ErrPortfolioExportReceipt
	}
	var envelope struct {
		EventType string          `json:"event_type"`
		Data      json.RawMessage `json:"data"`
	}
	if decodeClosed(encoded, &envelope) != nil {
		return portfolioReceipt{}, ErrPortfolioExportReceipt
	}
	var receipt portfolioReceipt
	switch envelope.EventType {
	case "tool_call_completed":
		var data directArtifactToolEvent
		if decodeClosed(envelope.Data, &data) != nil || !data.valid(item, tool) {
			return portfolioReceipt{}, ErrPortfolioExportReceipt
		}
		result, getErr := reconciler.Payloads.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: tool.ToolCallID, Class: "tool-result", ContentType: "application/json"}, payload.Manifest{Ref: data.ResultRef, Hash: data.ResultPayloadHash})
		if getErr != nil {
			return portfolioReceipt{}, getErr
		}
		if decodeClosed(result, &receipt) != nil {
			return portfolioReceipt{}, ErrPortfolioExportReceipt
		}
	case "tool_effect_reconciled":
		var data reconciledArtifactToolEvent
		if decodeClosed(envelope.Data, &data) != nil || !data.valid(item, tool) || decodeClosed(data.ProviderEvidence, &receipt) != nil {
			return portfolioReceipt{}, ErrPortfolioExportReceipt
		}
	default:
		return portfolioReceipt{}, ErrPortfolioExportReceipt
	}
	if !validPortfolioReceipt(receipt, item, tool.ExternalResourceRef) {
		return portfolioReceipt{}, ErrPortfolioExportReceipt
	}
	return receipt, nil
}

type directArtifactToolEvent struct {
	SchemaVersion        int    `json:"schema_version"`
	ToolCallID           string `json:"tool_call_id"`
	RunID                string `json:"run_id"`
	AttemptID            string `json:"attempt_id"`
	Fence                uint64 `json:"fence"`
	ToolName             string `json:"tool_name"`
	DescriptorSnapshotID string `json:"descriptor_snapshot_id"`
	RequestHash          string `json:"request_hash"`
	EffectClass          string `json:"effect_class"`
	ProviderRequestID    string `json:"provider_request_id"`
	ResultRef            string `json:"result_ref"`
	ResultPayloadHash    string `json:"result_payload_hash"`
	ResultHash           string `json:"result_hash"`
	TargetState          string `json:"target_state"`
	ExternalResourceRef  string `json:"external_resource_ref"`
	EffectDisposition    string `json:"effect_disposition"`
	PolicySnapshotID     string `json:"policy_snapshot_id"`
	PolicySnapshotHash   string `json:"policy_snapshot_hash"`
	OverlayVersion       uint64 `json:"overlay_version"`
}

func (event directArtifactToolEvent) valid(item portfolioFinalizationCandidate, tool portfolioToolCandidate) bool {
	return event.SchemaVersion == 1 && event.ToolCallID == tool.ToolCallID && event.RunID == item.RunID && event.AttemptID == tool.OriginalAttemptID && event.Fence > 0 && event.ToolName == "artifact_export" && event.DescriptorSnapshotID != "" && portfolioDigestRE.MatchString(event.RequestHash) && event.EffectClass == "reconcilable_write" && event.ProviderRequestID != "" && event.ResultRef != "" && portfolioDigestRE.MatchString(event.ResultPayloadHash) && portfolioDigestRE.MatchString(event.ResultHash) && event.TargetState == "succeeded" && event.ExternalResourceRef == tool.ExternalResourceRef && event.EffectDisposition == "confirmed" && event.PolicySnapshotID != "" && portfolioDigestRE.MatchString(event.PolicySnapshotHash) && event.OverlayVersion > 0
}

type reconciledArtifactToolEvent struct {
	SchemaVersion        int             `json:"schema_version"`
	ToolCallID           string          `json:"tool_call_id"`
	RunID                string          `json:"run_id"`
	EffectID             string          `json:"effect_id"`
	AttemptID            string          `json:"attempt_id"`
	Fence                uint64          `json:"fence"`
	ToolName             string          `json:"tool_name"`
	DescriptorSnapshotID string          `json:"descriptor_snapshot_id"`
	DescriptorHash       string          `json:"descriptor_hash"`
	EffectClass          string          `json:"effect_class"`
	EffectKey            string          `json:"effect_key"`
	EffectScope          string          `json:"effect_scope"`
	ProviderID           string          `json:"provider_id"`
	ProviderRequestID    string          `json:"provider_request_id"`
	ReconciliationRound  int             `json:"reconciliation_round"`
	Disposition          string          `json:"disposition"`
	ExternalResourceRef  string          `json:"external_resource_ref"`
	TargetState          string          `json:"target_state"`
	ProviderEvidence     json.RawMessage `json:"provider_evidence"`
}

func (event reconciledArtifactToolEvent) valid(item portfolioFinalizationCandidate, tool portfolioToolCandidate) bool {
	return event.SchemaVersion == 1 && event.ToolCallID == tool.ToolCallID && event.RunID == item.RunID && event.EffectID != "" && event.AttemptID == tool.ReconciliationAttemptID && event.Fence > 0 && event.ToolName == "artifact_export" && event.DescriptorSnapshotID != "" && portfolioDigestRE.MatchString(event.DescriptorHash) && event.EffectClass == "reconcilable_write" && event.EffectKey != "" && event.EffectScope != "" && event.ProviderID != "" && event.ProviderRequestID != "" && event.ReconciliationRound > 0 && event.Disposition == "confirmed" && event.ExternalResourceRef == tool.ExternalResourceRef && event.TargetState == "succeeded" && len(event.ProviderEvidence) > 0
}

func (reconciler PortfolioExportReconciler) commit(ctx context.Context, tenantID string, item portfolioFinalizationCandidate, tool portfolioToolCandidate, receipt *portfolioReceipt, failureCode string) (string, bool, error) {
	now := reconciler.now()
	buildIDs, err := reconciler.Store.buildStartedEventIDs(item.ExportID)
	if err != nil {
		return "", false, ErrConfiguration
	}
	completeIDs, err := reconciler.Store.completedEventIDs(item.ExportID)
	if err != nil {
		return "", false, ErrConfiguration
	}
	failIDs, err := reconciler.Store.failedEventIDs(item.ExportID)
	if err != nil {
		return "", false, ErrConfiguration
	}
	buildPointer, err := reconciler.putEvent(ctx, tenantID, buildIDs.event, map[string]any{"subject_id": item.ExportID, "subject_version": 2, "run_id": item.RunID})
	if err != nil {
		return "", false, err
	}
	var terminalPointer PayloadPointer
	if receipt != nil {
		terminalPointer, err = reconciler.putEvent(ctx, tenantID, completeIDs.event, map[string]any{"subject_id": item.ExportID, "subject_version": 3, "tool_call_id": tool.ToolCallID, "object_ref": receipt.ObjectRef, "object_version": receipt.ObjectVersion, "content_hash": receipt.ContentHash, "media_type": receipt.MediaType, "byte_size": receipt.ByteSize, "scan_result_hash": receipt.ScanResultHash})
	} else {
		failureObjectID := failIDs.event + ":v" + strconv.FormatUint(item.ExportVersion+1, 10) + ":" + failureCode
		terminalPointer, err = reconciler.putEvent(ctx, tenantID, failureObjectID, map[string]any{"subject_id": item.ExportID, "subject_version": item.ExportVersion + 1, "failure_code": failureCode})
	}
	if err != nil {
		return "", false, err
	}
	tx, err := reconciler.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return "", false, err
	}
	var userID, runID, status, format, runStatus string
	var version uint64
	var createdAt time.Time
	var startedAt *time.Time
	err = tx.QueryRow(ctx, `SELECT p.user_id::text,p.run_id::text,p.status,p.version,p.export_format,p.created_at,p.started_at,r.status FROM product.portfolio_exports p JOIN agent.runs r ON r.tenant_id=p.tenant_id AND r.id=p.run_id WHERE p.tenant_id=$1 AND p.id=$2 FOR UPDATE OF p,r`, tenantID, item.ExportID).Scan(&userID, &runID, &status, &version, &format, &createdAt, &startedAt, &runStatus)
	if err != nil || userID != item.UserID || runID != item.RunID {
		return "", false, ErrPortfolioExportConflict
	}
	if status == "ready" || status == "failed" || status == "expired" {
		return status, true, tx.Commit(ctx)
	}
	if status != item.ExportStatus || version != item.ExportVersion || format != item.Format {
		return "pending", true, tx.Commit(ctx)
	}
	actor := reconciler.actor()
	if receipt != nil {
		var confirmed bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.tool_calls t JOIN agent.tool_effects e ON e.tenant_id=t.tenant_id AND e.tool_call_id=t.id JOIN agent.events ev ON ev.tenant_id=t.tenant_id AND ev.id=t.result_event_id AND ev.store_epoch=$6 WHERE t.tenant_id=$1 AND t.id=$2 AND t.run_id=$3 AND t.tool_name='artifact_export' AND t.status='succeeded' AND t.result_event_id=$4 AND e.status='confirmed' AND e.external_resource_ref=$5)`, tenantID, tool.ToolCallID, runID, tool.ResultEventID, receipt.ObjectRef, reconciler.StoreEpoch).Scan(&confirmed)
		if err != nil || !confirmed {
			return "", false, ErrPortfolioExportConflict
		}
		var runStartedID string
		var runStartedAt time.Time
		err = tx.QueryRow(ctx, `SELECT id::text,occurred_at FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run' AND aggregate_id=$2 AND event_type='RunStarted' AND event_schema_version=1 AND store_epoch=$3 ORDER BY aggregate_version LIMIT 1`, tenantID, runID, reconciler.StoreEpoch).Scan(&runStartedID, &runStartedAt)
		if err != nil {
			return "", false, ErrPortfolioExportConflict
		}
		if status == "requested" {
			if version != 1 {
				return "", false, ErrPortfolioExportVersion
			}
			if tag, updateErr := tx.Exec(ctx, `UPDATE product.portfolio_exports SET status='building',version=2,started_at=$1,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND status='requested' AND version=1`, runStartedAt, now, tenantID, item.ExportID); updateErr != nil || tag.RowsAffected() != 1 {
				return "", false, ErrPortfolioExportVersion
			}
			causation := runStartedID
			if _, err = reconciler.Appender.Append(ctx, tx, eventInput(buildIDs, "PortfolioExportBuildStarted", 1, tenantID, userID, item.ExportID, 2, reconciler.StoreEpoch, runStartedAt, actor, &causation, item.CorrelationID, buildPointer)); err != nil {
				return "", false, err
			}
			status, version, startedAt = "building", 2, &runStartedAt
		}
		if status != "building" || version != 2 || startedAt == nil || !mediaTypeMatches(format, receipt.MediaType) {
			return "", false, ErrPortfolioExportVersion
		}
		expiresAt := now.Add(reconciler.Retention).UTC().Truncate(time.Microsecond)
		if tag, updateErr := tx.Exec(ctx, `UPDATE product.portfolio_exports SET status='ready',version=3,object_ref=$1,object_version=$2,content_hash=$3,media_type=$4,byte_size=$5,scan_result_hash=$6,completion_event_id=$7,completed_at=$8,expires_at=$9,updated_at=$8 WHERE tenant_id=$10 AND id=$11 AND status='building' AND version=2`, receipt.ObjectRef, receipt.ObjectVersion, receipt.ContentHash, receipt.MediaType, receipt.ByteSize, receipt.ScanResultHash, completeIDs.event, now, expiresAt, tenantID, item.ExportID); updateErr != nil || tag.RowsAffected() != 1 {
			return "", false, ErrPortfolioExportVersion
		}
		causation := tool.ResultEventID
		if _, err = reconciler.Appender.Append(ctx, tx, eventInput(completeIDs, "PortfolioExportCompleted", 2, tenantID, userID, item.ExportID, 3, reconciler.StoreEpoch, now, actor, &causation, item.CorrelationID, terminalPointer)); err != nil {
			return "", false, err
		}
		return "ready", false, tx.Commit(ctx)
	}
	if !terminalRunStatus(runStatus) && failureCode == "" {
		return "pending", false, tx.Commit(ctx)
	}
	causation := tool.ResultEventID
	if causation == "" {
		eventType := map[string]string{"succeeded": "RunSucceeded", "failed": "RunFailed", "cancelled": "RunCancelled", "expired": "RunExpired"}[runStatus]
		err = tx.QueryRow(ctx, `SELECT id::text FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run' AND aggregate_id=$2 AND event_type=$3 AND store_epoch=$4 ORDER BY aggregate_version DESC LIMIT 1`, tenantID, runID, eventType, reconciler.StoreEpoch).Scan(&causation)
		if err != nil {
			return "", false, ErrPortfolioExportConflict
		}
	}
	if startedAt == nil {
		startedAt = &createdAt
	}
	nextVersion := version + 1
	if tag, updateErr := tx.Exec(ctx, `UPDATE product.portfolio_exports SET status='failed',version=$1,started_at=$2,completion_event_id=$3,failed_at=$4,failure_code=$5,updated_at=$4 WHERE tenant_id=$6 AND id=$7 AND status=$8 AND version=$9`, nextVersion, startedAt, failIDs.event, now, failureCode, tenantID, item.ExportID, status, version); updateErr != nil || tag.RowsAffected() != 1 {
		return "", false, ErrPortfolioExportVersion
	}
	if _, err = reconciler.Appender.Append(ctx, tx, eventInput(failIDs, "PortfolioExportFailed", 1, tenantID, userID, item.ExportID, nextVersion, reconciler.StoreEpoch, now, actor, &causation, item.CorrelationID, terminalPointer)); err != nil {
		return "", false, err
	}
	return "failed", false, tx.Commit(ctx)
}

func (reconciler PortfolioExportReconciler) load(ctx context.Context, tenantID string, limit int) ([]portfolioFinalizationCandidate, error) {
	if !reconciler.valid() || tenantID == "" || limit < 1 || limit > 500 {
		return nil, ErrConfiguration
	}
	tx, err := reconciler.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT p.id::text,p.user_id::text,p.run_id::text,p.status,p.version,r.status,p.export_format,e.correlation_id::text FROM product.portfolio_exports p JOIN agent.runs r ON r.tenant_id=p.tenant_id AND r.id=p.run_id JOIN agent.events e ON e.tenant_id=p.tenant_id AND e.id=p.request_event_id WHERE p.tenant_id=$1 AND e.store_epoch=$2 AND p.status IN ('requested','building') AND (r.status IN ('succeeded','failed','cancelled','expired') OR EXISTS(SELECT 1 FROM agent.tool_calls t JOIN agent.tool_effects x ON x.tenant_id=t.tenant_id AND x.tool_call_id=t.id JOIN agent.events te ON te.tenant_id=t.tenant_id AND te.id=t.result_event_id AND te.store_epoch=$2 WHERE t.tenant_id=p.tenant_id AND t.run_id=p.run_id AND t.tool_name='artifact_export' AND t.status='succeeded' AND x.status='confirmed')) ORDER BY p.updated_at,p.id LIMIT $3`, tenantID, reconciler.StoreEpoch, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []portfolioFinalizationCandidate{}
	for rows.Next() {
		var item portfolioFinalizationCandidate
		if err = rows.Scan(&item.ExportID, &item.UserID, &item.RunID, &item.ExportStatus, &item.ExportVersion, &item.RunStatus, &item.Format, &item.CorrelationID); err != nil {
			return nil, err
		}
		values = append(values, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return values, tx.Commit(ctx)
}

func validPortfolioReceipt(receipt portfolioReceipt, item portfolioFinalizationCandidate, externalRef string) bool {
	return receipt.SchemaVersion == 1 && receipt.TargetKind == "portfolio_export" && receipt.TargetID == item.ExportID && receipt.ObjectRef == externalRef && portfolioObjectRefRE.MatchString(receipt.ObjectRef) && receipt.ObjectVersion != "" && len(receipt.ObjectVersion) <= 1024 && portfolioDigestRE.MatchString(receipt.ContentHash) && mediaTypeMatches(item.Format, receipt.MediaType) && receipt.ByteSize > 0 && receipt.ByteSize <= 11<<20 && portfolioDigestRE.MatchString(receipt.ScanResultHash)
}

func decodeClosed(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrPortfolioExportReceipt
	}
	return nil
}

func terminalRunStatus(status string) bool {
	return status == "succeeded" || status == "failed" || status == "cancelled" || status == "expired"
}

func (reconciler PortfolioExportReconciler) putEvent(ctx context.Context, tenantID, objectID string, value any) (PayloadPointer, error) {
	encoded, err := json.Marshal(map[string]any{"event_type": "portfolio_export_finalization", "data": value})
	if err != nil {
		return PayloadPointer{}, err
	}
	manifest, err := reconciler.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: objectID, Class: "event-payload", ContentType: "application/json"}, encoded)
	return PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, err
}

func (reconciler PortfolioExportReconciler) actor() json.RawMessage {
	return json.RawMessage(`{"kind":"service","name":"portfolio-export-reconciler"}`)
}

func (reconciler PortfolioExportReconciler) now() time.Time {
	if reconciler.Now != nil {
		return reconciler.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func (reconciler PortfolioExportReconciler) valid() bool {
	return reconciler.Pool != nil && reconciler.Payloads != nil && reconciler.Store.valid() && len(reconciler.IDKey) >= 32 && reconciler.StoreEpoch != "" && reconciler.Store.StoreEpoch == reconciler.StoreEpoch && reconciler.Retention >= 24*time.Hour && reconciler.Retention <= 365*24*time.Hour
}
