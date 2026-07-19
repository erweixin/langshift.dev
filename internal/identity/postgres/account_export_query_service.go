package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/langshift/lites/internal/identity/api"
	"github.com/langshift/lites/internal/payload"
)

// GetAccountExport returns only an owner-scoped lifecycle projection. Object
// references and encryption metadata never cross the public API boundary.
func (service AuthService) GetAccountExport(ctx context.Context, query api.AccountExportQuery) (api.AccountExportResource, error) {
	if err := service.validateSessionRequest(query.AuthenticatedRequestMetadata); err != nil {
		return api.AccountExportResource{}, err
	}
	if _, err := uuid.Parse(query.ExportID); err != nil {
		return api.AccountExportResource{}, api.ErrValidation
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return api.AccountExportResource{}, api.ErrDependencyUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := service.now()
	if err = service.authorizeSession(ctx, tx, query.AuthenticatedRequestMetadata, now, false); err != nil {
		return api.AccountExportResource{}, err
	}
	result, err := readAccountExportResource(ctx, tx, query.TenantID, query.UserID, query.ExportID)
	if err != nil {
		return api.AccountExportResource{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return api.AccountExportResource{}, api.ErrDependencyUnavailable
	}
	return result, nil
}

// DownloadAccountExport requires a fresh password reauthentication and opens
// the encrypted object only after checking owner, lifecycle, and expiry state.
func (service AuthService) DownloadAccountExport(ctx context.Context, query api.AccountExportQuery) (api.AccountExportDownload, error) {
	if err := service.validateSessionRequest(query.AuthenticatedRequestMetadata); err != nil {
		return api.AccountExportDownload{}, err
	}
	if _, err := uuid.Parse(query.ExportID); err != nil {
		return api.AccountExportDownload{}, api.ErrValidation
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return api.AccountExportDownload{}, api.ErrDependencyUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := service.now()
	if err = service.authorizeSession(ctx, tx, query.AuthenticatedRequestMetadata, now, false); err != nil {
		return api.AccountExportDownload{}, err
	}
	if err = service.requireRecentReauthentication(ctx, tx, query.AuthenticatedRequestMetadata, now); err != nil {
		return api.AccountExportDownload{}, err
	}
	var status, format, objectRef, manifestHash string
	var expiresAt *time.Time
	err = tx.QueryRow(ctx, `SELECT status,scope->>'format',COALESCE(object_ref,''),COALESCE(content_hash,''),expires_at FROM product.data_export_requests WHERE tenant_id=$1 AND user_id=$2 AND id=$3`, query.TenantID, query.UserID, query.ExportID).Scan(&status, &format, &objectRef, &manifestHash, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.AccountExportDownload{}, api.ErrResourceNotFound
	}
	if err != nil {
		return api.AccountExportDownload{}, api.ErrDependencyUnavailable
	}
	if status != "ready" || expiresAt == nil || !expiresAt.After(now) || objectRef == "" || manifestHash == "" || format != "json" && format != "zip" {
		return api.AccountExportDownload{}, api.ErrStateConflict
	}
	body, err := service.Payloads.Get(ctx, payload.Descriptor{TenantID: query.TenantID, ObjectID: query.ExportID, Class: "account-export", ContentType: accountExportContentType(format)}, payload.Manifest{Ref: objectRef, Hash: manifestHash})
	if err != nil || len(body) == 0 {
		return api.AccountExportDownload{}, api.ErrDependencyUnavailable
	}
	auditID, err := service.newID()
	if err != nil {
		return api.AccountExportDownload{}, api.ErrDependencyUnavailable
	}
	if _, err = tx.Exec(ctx, `INSERT INTO identity.security_events(id,tenant_id,subject_user_id,actor_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES($1,$2,$3,$3,'account_export_downloaded',$4,$5,$6,jsonb_build_object('export_id',$7::text),$8)`, auditID, query.TenantID, query.UserID, query.RequestID, query.ClientIPHash, query.UserAgentHash, query.ExportID, now); err != nil {
		return api.AccountExportDownload{}, api.ErrDependencyUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return api.AccountExportDownload{}, api.ErrDependencyUnavailable
	}
	digest := sha256.Sum256(body)
	return api.AccountExportDownload{Filename: "lites-account-export-" + query.ExportID + "." + format, MediaType: accountExportContentType(format), ContentHash: hex.EncodeToString(digest[:]), Body: body}, nil
}

func readAccountExportResource(ctx context.Context, tx pgx.Tx, tenantID, userID, exportID string) (api.AccountExportResource, error) {
	var result api.AccountExportResource
	var encodedScope []byte
	err := tx.QueryRow(ctx, `SELECT id::text,version,status,scope->'categories',scope->>'format',created_at,updated_at,completed_at,expires_at,failure_code FROM product.data_export_requests WHERE tenant_id=$1 AND user_id=$2 AND id=$3`, tenantID, userID, exportID).Scan(&result.ID, &result.Version, &result.Status, &encodedScope, &result.Format, &result.CreatedAt, &result.UpdatedAt, &result.CompletedAt, &result.ExpiresAt, &result.FailureCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.AccountExportResource{}, api.ErrResourceNotFound
	}
	if err != nil || json.Unmarshal(encodedScope, &result.Scope) != nil || len(result.Scope) == 0 || result.Format != "json" && result.Format != "zip" || !validAccountExportResource(result) {
		return api.AccountExportResource{}, api.ErrDependencyUnavailable
	}
	return result, nil
}

func validAccountExportResource(result api.AccountExportResource) bool {
	switch result.Status {
	case "requested":
		return result.CompletedAt == nil && result.ExpiresAt == nil && result.FailureCode == nil
	case "ready":
		return result.CompletedAt != nil && result.ExpiresAt != nil && result.ExpiresAt.After(*result.CompletedAt) && result.FailureCode == nil
	case "failed":
		return result.CompletedAt != nil && result.ExpiresAt == nil && result.FailureCode != nil && (*result.FailureCode == accountExportFailureInvalidCommand || *result.FailureCode == accountExportFailureTooLarge || *result.FailureCode == accountExportFailureLegacy)
	default:
		return false
	}
}

func accountExportContentType(format string) string {
	if format == "zip" {
		return "application/zip"
	}
	return "application/json"
}
