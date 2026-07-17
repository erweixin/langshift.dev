package postgres

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	contractsapi "github.com/langshift/lites/internal/contracts/api"
	"github.com/langshift/lites/internal/idempotency"
	idempotencypostgres "github.com/langshift/lites/internal/idempotency/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

const (
	auditExportOperation     = "admin.audit-exports.request.v2"
	auditExportResponseClass = "contract-audit-export-idempotency"
	auditExportContentClass  = "contract-audit-export"
	auditExportMaxRecords    = 100_000
)

func (service ControlService) RequestAuditExport(ctx context.Context, command contractsapi.AuditExportCommand) (contractsapi.AuditExport, error) {
	command.Reason = strings.TrimSpace(command.Reason)
	command.Kinds = append([]string(nil), command.Kinds...)
	sort.Strings(command.Kinds)
	if !service.valid() || !validMetadata(command.CommandMetadata) || command.PeriodStart.IsZero() || command.PeriodEnd.IsZero() || !command.PeriodEnd.After(command.PeriodStart) || command.PeriodEnd.Sub(command.PeriodStart) > 366*24*time.Hour || !validAuditExportKinds(command.Kinds) || command.Format != "jsonl" && command.Format != "csv" || command.Reason == "" || len(command.Reason) > 500 {
		return contractsapi.AuditExport{}, contractsapi.ErrValidation
	}
	canonical, err := json.Marshal(command)
	if err != nil {
		return contractsapi.AuditExport{}, contractsapi.ErrValidation
	}
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return contractsapi.AuditExport{}, service.mapError(err)
	}
	recordID, err := ids.DeterministicUUID(service.IDKey, "contract-idempotency:"+auditExportOperation, command.TenantID+"\x00"+command.UserID+"\x00"+command.IdempotencyKey)
	if err != nil {
		return contractsapi.AuditExport{}, service.mapError(err)
	}
	exportID, _ := ids.DeterministicUUID(service.IDKey, "contract-audit-export", recordID)
	responseDescriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: auditExportResponseClass, ContentType: "application/json"}
	input := idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: auditExportOperation}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	if response, found, loadErr := executor.LoadCompleted(ctx, input); loadErr != nil {
		return contractsapi.AuditExport{}, service.mapError(loadErr)
	} else if found {
		result, readErr := service.readAuditExportResponse(ctx, responseDescriptor, response)
		result.Replayed = readErr == nil
		return result, service.mapError(readErr)
	}
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		now := service.now()
		if _, innerErr := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if _, _, innerErr := service.currentPrincipal(ctx, tx, command.CommandMetadata, now); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		limit, innerErr := service.auditExportEntitlementLimit(ctx, tx, command.TenantID, command.Format, now)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		records, innerErr := service.queryAuditRecords(ctx, tx, command.TenantID, command.PeriodStart.UTC(), command.PeriodEnd.UTC(), command.Kinds, limit+1)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if len(records) > limit {
			return idempotency.Response{}, contractsapi.ErrValidation
		}
		content, contentType, innerErr := encodeAuditExport(records, command.Format)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		contentManifest, innerErr := service.Payloads.Put(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: exportID, Class: auditExportContentClass, ContentType: contentType}, content)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		expiresAt := now.Add(30 * 24 * time.Hour)
		_, innerErr = tx.Exec(ctx, `INSERT INTO contracts.audit_exports(id,tenant_id,requested_by_user_id,requested_by_membership_id,session_id,version,status,format,period_start,period_end,kinds,record_count,content_ref,content_hash,byte_size,reason_hash,created_at,expires_at) VALUES($1,$2,$3,$4,$5,1,'ready',$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, exportID, command.TenantID, command.UserID, command.MembershipID, command.SessionID, command.Format, command.PeriodStart.UTC(), command.PeriodEnd.UTC(), command.Kinds, len(records), contentManifest.Ref, contentManifest.Hash, len(content), plaintextHash(command.Reason), now, expiresAt)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if innerErr = service.recordAdminAccess(ctx, tx, adminAccessInput{RequestID: command.RequestID, TenantID: command.TenantID, UserID: command.UserID, MembershipID: command.MembershipID, SessionID: command.SessionID, Action: "audit_export_created", ResourceKind: "audit_export", Reason: command.Reason, OccurredAt: now}); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		result := contractsapi.AuditExport{ID: exportID, Version: 1, Status: "ready", Format: command.Format, RecordCount: len(records), ContentHash: contentManifest.Hash, ByteSize: int64(len(content)), PeriodStart: command.PeriodStart.UTC(), PeriodEnd: command.PeriodEnd.UTC(), CreatedAt: now, ExpiresAt: expiresAt}
		if _, innerErr = service.appendEvent(ctx, tx, command.CommandMetadata, recordID, "AuditExportCreated", "audit_export", exportID, 1, map[string]any{"export_id": exportID, "format": command.Format, "record_count": len(records), "content_hash": contentManifest.Hash, "period_start": command.PeriodStart.UTC(), "period_end": command.PeriodEnd.UTC()}, now); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		responseManifest, innerErr := service.putJSON(ctx, responseDescriptor, result)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		return idempotency.Response{Status: http.StatusOK, ContentType: "application/vnd.lites.admin-audit-export.v2+json", PayloadRef: responseManifest.Ref, Hash: responseManifest.Hash, ResourceVersion: 1}, nil
	})
	if err != nil {
		return contractsapi.AuditExport{}, service.mapError(err)
	}
	result, err := service.readAuditExportResponse(ctx, responseDescriptor, response)
	result.Replayed = replayed && err == nil
	return result, service.mapError(err)
}

func (service ControlService) ReadAuditExport(ctx context.Context, query contractsapi.AuditExportQuery) (contractsapi.AuditExportDownload, error) {
	query.Reason = strings.TrimSpace(query.Reason)
	if !service.valid() || query.RequestID == "" || len(query.RequestID) > 256 || uuid.Validate(query.TenantID) != nil || uuid.Validate(query.UserID) != nil || uuid.Validate(query.MembershipID) != nil || uuid.Validate(query.SessionID) != nil || uuid.Validate(query.ExportID) != nil || query.Reason == "" || len(query.Reason) > 500 {
		return contractsapi.AuditExportDownload{}, contractsapi.ErrValidation
	}
	now := service.now()
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return contractsapi.AuditExportDownload{}, service.mapError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, query.TenantID); err != nil {
		return contractsapi.AuditExportDownload{}, service.mapError(err)
	}
	metadata := contractsapi.CommandMetadata{RequestID: query.RequestID, ClientRequestID: "audit-export-read", IdempotencyKey: "audit-export-read", TenantID: query.TenantID, UserID: query.UserID, MembershipID: query.MembershipID, SessionID: query.SessionID}
	if _, _, err = service.currentPrincipal(ctx, tx, metadata, now); err != nil {
		return contractsapi.AuditExportDownload{}, service.mapError(err)
	}
	var result contractsapi.AuditExportDownload
	var contentRef string
	err = tx.QueryRow(ctx, `SELECT id::text,version,status,format,record_count,content_ref,content_hash,byte_size,period_start,period_end,created_at,expires_at FROM contracts.audit_exports WHERE tenant_id=$1 AND id=$2 AND status='ready' AND expires_at>$3`, query.TenantID, query.ExportID, now).Scan(&result.ID, &result.Version, &result.Status, &result.Format, &result.RecordCount, &contentRef, &result.ContentHash, &result.ByteSize, &result.PeriodStart, &result.PeriodEnd, &result.CreatedAt, &result.ExpiresAt)
	if err != nil {
		return contractsapi.AuditExportDownload{}, service.mapError(err)
	}
	if err = service.recordAdminAccess(ctx, tx, adminAccessInput{RequestID: query.RequestID, TenantID: query.TenantID, UserID: query.UserID, MembershipID: query.MembershipID, SessionID: query.SessionID, Action: "audit_export_downloaded", ResourceKind: "audit_export", Reason: query.Reason, OccurredAt: now}); err != nil {
		return contractsapi.AuditExportDownload{}, service.mapError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return contractsapi.AuditExportDownload{}, service.mapError(err)
	}
	contentType := "application/x-ndjson"
	if result.Format == "csv" {
		contentType = "text/csv"
	}
	result.Content, err = service.Payloads.Get(ctx, payload.Descriptor{TenantID: query.TenantID, ObjectID: query.ExportID, Class: auditExportContentClass, ContentType: contentType}, payload.Manifest{Ref: contentRef, Hash: result.ContentHash})
	if err != nil || int64(len(result.Content)) != result.ByteSize {
		return contractsapi.AuditExportDownload{}, service.mapError(errors.Join(payload.ErrIntegrity, err))
	}
	return result, nil
}

func (service ControlService) auditExportEntitlementLimit(ctx context.Context, tx pgx.Tx, tenantID, format string, now time.Time) (int, error) {
	var limit *int64
	var config []byte
	err := tx.QueryRow(ctx, `SELECT e.limit_value,e.config FROM contracts.contracts c JOIN LATERAL (SELECT limit_value,config FROM contracts.contract_entitlements WHERE tenant_id=c.tenant_id AND contract_id=c.id AND entitlement_key='audit_export' AND effective_at<=$2 AND COALESCE(expires_at,'infinity'::timestamptz)>$2 ORDER BY version DESC LIMIT 1) e ON true WHERE c.tenant_id=$1 AND c.status='active' AND c.starts_at<=$2 AND c.ends_at>$2 ORDER BY c.version DESC LIMIT 1`, tenantID, now).Scan(&limit, &config)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, contractsapi.ErrPermissionDenied
	}
	if err != nil {
		return 0, err
	}
	configuredFormats := []string{"jsonl", "csv"}
	var settings struct {
		Format  string   `json:"format"`
		Formats []string `json:"formats"`
	}
	if len(config) != 0 && json.Unmarshal(config, &settings) != nil {
		return 0, contractsapi.ErrPermissionDenied
	}
	if len(settings.Formats) > 0 {
		configuredFormats = settings.Formats
	} else if settings.Format != "" {
		configuredFormats = []string{settings.Format}
	}
	allowed := false
	for _, candidate := range configuredFormats {
		allowed = allowed || candidate == format
	}
	if !allowed {
		return 0, contractsapi.ErrPermissionDenied
	}
	result := auditExportMaxRecords
	if limit != nil && *limit < int64(result) {
		result = int(*limit)
	}
	if result < 1 {
		return 0, contractsapi.ErrPermissionDenied
	}
	return result, nil
}

func (service ControlService) queryAuditRecords(ctx context.Context, tx pgx.Tx, tenantID string, start, end time.Time, kinds []string, limit int) ([]contractsapi.AuditRecord, error) {
	rows, err := tx.Query(ctx, `WITH audit_records AS (
      SELECT id,occurred_at,'contract_change'::text AS kind,event_type AS action,actor_user_id,'contract'::text AS resource_kind,COALESCE(contract_id,id)::text AS resource_id,reason AS reason_hash,before_version,after_version FROM contracts.contract_audit_events WHERE tenant_id=$1
      UNION ALL
      SELECT id,recorded_at,'accounting_adjustment'::text,'accounting_adjustment_executed',actor_user_id,'credit_bucket'::text,bucket_id::text,reason_hash,before_bucket_version,after_bucket_version FROM contracts.manual_adjustments WHERE tenant_id=$1
      UNION ALL
      SELECT id,occurred_at,'admin_read'::text,action,actor_user_id,resource_kind,id::text,reason_hash,NULL::bigint,NULL::bigint FROM contracts.admin_access_audit WHERE tenant_id=$1
    ) SELECT id::text,kind,action,actor_user_id::text,resource_kind,resource_id,reason_hash,before_version,after_version,occurred_at FROM audit_records WHERE occurred_at>=$2 AND occurred_at<$3 AND kind=ANY($4::text[]) ORDER BY occurred_at,id LIMIT $5`, tenantID, start, end, kinds, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]contractsapi.AuditRecord, 0)
	for rows.Next() {
		var record contractsapi.AuditRecord
		var beforeVersion, afterVersion *int64
		if err = rows.Scan(&record.ID, &record.Kind, &record.Action, &record.ActorUserID, &record.ResourceKind, &record.ResourceID, &record.ReasonHash, &beforeVersion, &afterVersion, &record.OccurredAt); err != nil {
			return nil, err
		}
		if beforeVersion != nil && *beforeVersion >= 0 {
			value := uint64(*beforeVersion)
			record.BeforeVersion = &value
		}
		if afterVersion != nil && *afterVersion >= 0 {
			value := uint64(*afterVersion)
			record.AfterVersion = &value
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func encodeAuditExport(records []contractsapi.AuditRecord, format string) ([]byte, string, error) {
	var buffer bytes.Buffer
	if format == "jsonl" {
		encoder := json.NewEncoder(&buffer)
		encoder.SetEscapeHTML(false)
		for _, record := range records {
			if err := encoder.Encode(record); err != nil {
				return nil, "", err
			}
		}
		return buffer.Bytes(), "application/x-ndjson", nil
	}
	writer := csv.NewWriter(&buffer)
	_ = writer.Write([]string{"id", "kind", "action", "actor_user_id", "resource_kind", "resource_id", "reason_hash", "before_version", "after_version", "occurred_at"})
	for _, record := range records {
		before, after := "", ""
		if record.BeforeVersion != nil {
			before = strconv.FormatUint(*record.BeforeVersion, 10)
		}
		if record.AfterVersion != nil {
			after = strconv.FormatUint(*record.AfterVersion, 10)
		}
		_ = writer.Write([]string{record.ID, record.Kind, record.Action, record.ActorUserID, record.ResourceKind, record.ResourceID, record.ReasonHash, before, after, record.OccurredAt.UTC().Format(time.RFC3339Nano)})
	}
	writer.Flush()
	return buffer.Bytes(), "text/csv", writer.Error()
}

func (service ControlService) readAuditExportResponse(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (contractsapi.AuditExport, error) {
	var result contractsapi.AuditExport
	encoded, err := service.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: response.PayloadRef, Hash: response.Hash})
	if err != nil {
		return result, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return result, payload.ErrIntegrity
	}
	return result, nil
}

func validAuditExportKinds(values []string) bool {
	if len(values) < 1 || len(values) > 3 {
		return false
	}
	seen := map[string]bool{}
	for _, value := range values {
		if value != "contract_change" && value != "accounting_adjustment" && value != "admin_read" || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}
