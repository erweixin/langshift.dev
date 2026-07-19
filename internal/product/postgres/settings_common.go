package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/langshift/lites/internal/platform/ids"
)

var (
	errSettingsPermission       = errors.New("settings session is not authorized")
	errSettingsReauthentication = errors.New("settings session requires recent reauthentication")
)

func requireSettingsSession(ctx context.Context, tx pgx.Tx, tenantID, userID, sessionID string, now time.Time, recent bool) error {
	var reauthenticatedAt *time.Time
	err := tx.QueryRow(ctx, `SELECT reauthenticated_at FROM identity.sessions WHERE id=$1 AND user_id=$2 AND active_tenant_id=$3 AND revoked_at IS NULL AND expires_at>$4`, sessionID, userID, tenantID, now).Scan(&reauthenticatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return errSettingsPermission
	}
	if err != nil {
		return err
	}
	if recent && (reauthenticatedAt == nil || reauthenticatedAt.Before(now.Add(-5*time.Minute))) {
		return errSettingsReauthentication
	}
	return nil
}

func insertSettingsAudit(ctx context.Context, tx pgx.Tx, key []byte, requestID, tenantID, userID, sessionID, action, resourceID string, now time.Time) error {
	auditID, err := ids.DeterministicUUID(key, "product-settings-audit:"+action, tenantID+"\x00"+userID+"\x00"+requestID+"\x00"+resourceID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO product.settings_access_audit(id,tenant_id,user_id,session_id,action,resource_id,request_id,occurred_at) VALUES($1,$2,$3,$4,$5,NULLIF($6,'')::uuid,$7,$8) ON CONFLICT (id) DO NOTHING`, auditID, tenantID, userID, sessionID, action, resourceID, requestID, now)
	return err
}
