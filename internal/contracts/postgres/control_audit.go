package postgres

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	contractsapi "github.com/langshift/lites/internal/contracts/api"
	"github.com/langshift/lites/internal/platform/ids"
)

type adminAccessInput struct {
	RequestID, TenantID, UserID, MembershipID, SessionID string
	Action, ResourceKind, Reason                         string
	OccurredAt                                           time.Time
}

type signedAuditCursor struct {
	Version   int    `json:"version"`
	TenantID  string `json:"tenant_id"`
	BeforeAt  string `json:"before_at"`
	BeforeID  string `json:"before_id"`
	ExpiresAt int64  `json:"expires_at"`
}

func (service ControlService) ReadAudit(ctx context.Context, query contractsapi.AuditQuery) (contractsapi.AuditPage, error) {
	query.Reason = strings.TrimSpace(query.Reason)
	if !service.valid() || uuid.Validate(query.TenantID) != nil || uuid.Validate(query.UserID) != nil || uuid.Validate(query.MembershipID) != nil || uuid.Validate(query.SessionID) != nil || query.RequestID == "" || len(query.RequestID) > 256 || query.Reason == "" || len(query.Reason) > 500 || query.Limit < 1 || query.Limit > 200 {
		return contractsapi.AuditPage{}, contractsapi.ErrValidation
	}
	now := service.now()
	var beforeAt time.Time
	var beforeID string
	hasCursor := query.Before != ""
	if hasCursor {
		var err error
		beforeAt, beforeID, err = service.decodeAuditCursor(query.Before, query.TenantID, now)
		if err != nil {
			return contractsapi.AuditPage{}, contractsapi.ErrValidation
		}
	} else {
		beforeAt = now.Add(time.Second)
		beforeID = "ffffffff-ffff-ffff-ffff-ffffffffffff"
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return contractsapi.AuditPage{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, query.TenantID); err != nil {
		return contractsapi.AuditPage{}, err
	}
	allowed, err := service.authorizeAdminRead(ctx, tx, query.TenantID, query.UserID, query.MembershipID, query.SessionID, now)
	if err != nil {
		return contractsapi.AuditPage{}, err
	}
	if !allowed {
		return contractsapi.AuditPage{}, contractsapi.ErrPermissionDenied
	}
	rows, err := tx.Query(ctx, `WITH audit_records AS (
      SELECT id,occurred_at,'contract_change'::text AS kind,event_type AS action,actor_user_id,
        'contract'::text AS resource_kind,COALESCE(contract_id,id)::text AS resource_id,reason AS reason_hash,
        before_version,after_version
      FROM contracts.contract_audit_events WHERE tenant_id=$1
      UNION ALL
      SELECT id,recorded_at,'accounting_adjustment'::text,'accounting_adjustment_executed',actor_user_id,
        'credit_bucket'::text,bucket_id::text,reason_hash,before_bucket_version,after_bucket_version
      FROM contracts.manual_adjustments WHERE tenant_id=$1
      UNION ALL
      SELECT id,occurred_at,'admin_read'::text,action,actor_user_id,resource_kind,id::text,reason_hash,NULL::bigint,NULL::bigint
      FROM contracts.admin_access_audit WHERE tenant_id=$1
    )
    SELECT id::text,kind,action,actor_user_id::text,resource_kind,resource_id,reason_hash,before_version,after_version,occurred_at
    FROM audit_records WHERE (occurred_at,id)<($2,$3::uuid)
    ORDER BY occurred_at DESC,id DESC LIMIT $4`, query.TenantID, beforeAt, beforeID, query.Limit+1)
	if err != nil {
		return contractsapi.AuditPage{}, err
	}
	defer rows.Close()
	page := contractsapi.AuditPage{Items: make([]contractsapi.AuditRecord, 0, query.Limit)}
	for rows.Next() {
		var record contractsapi.AuditRecord
		var beforeVersion, afterVersion *int64
		if err = rows.Scan(&record.ID, &record.Kind, &record.Action, &record.ActorUserID, &record.ResourceKind, &record.ResourceID, &record.ReasonHash, &beforeVersion, &afterVersion, &record.OccurredAt); err != nil {
			return contractsapi.AuditPage{}, err
		}
		if beforeVersion != nil && *beforeVersion >= 0 {
			value := uint64(*beforeVersion)
			record.BeforeVersion = &value
		}
		if afterVersion != nil && *afterVersion >= 0 {
			value := uint64(*afterVersion)
			record.AfterVersion = &value
		}
		page.Items = append(page.Items, record)
	}
	if err = rows.Err(); err != nil {
		return contractsapi.AuditPage{}, err
	}
	if len(page.Items) > query.Limit {
		page.Items = page.Items[:query.Limit]
		last := page.Items[len(page.Items)-1]
		page.NextBefore, err = service.encodeAuditCursor(query.TenantID, last.OccurredAt, last.ID, now.Add(15*time.Minute))
		if err != nil {
			return contractsapi.AuditPage{}, err
		}
	}
	if err = service.recordAdminAccess(ctx, tx, adminAccessInput{RequestID: query.RequestID, TenantID: query.TenantID, UserID: query.UserID, MembershipID: query.MembershipID, SessionID: query.SessionID, Action: "audit_export_read", ResourceKind: "audit_export", Reason: query.Reason, OccurredAt: now}); err != nil {
		return contractsapi.AuditPage{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return contractsapi.AuditPage{}, err
	}
	return page, nil
}

func (service ControlService) authorizeAdminRead(ctx context.Context, tx pgx.Tx, tenantID, userID, membershipID, sessionID string, now time.Time) (bool, error) {
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM identity.memberships m JOIN identity.tenants t ON t.id=m.tenant_id JOIN identity.sessions s ON s.id=$4 AND s.user_id=m.user_id AND s.active_tenant_id=m.tenant_id WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.id=$3 AND m.status='active' AND m.role IN ('owner','contract_admin') AND t.kind='enterprise' AND t.status='active' AND s.revoked_at IS NULL AND s.expires_at>$5)`, tenantID, userID, membershipID, sessionID, now).Scan(&allowed)
	return allowed, err
}

func (service ControlService) recordAdminAccess(ctx context.Context, tx pgx.Tx, input adminAccessInput) error {
	auditID, err := ids.DeterministicUUID(service.IDKey, "contract-admin-access-audit", input.TenantID+"\x00"+input.Action+"\x00"+input.RequestID)
	if err != nil {
		return err
	}
	reasonHash := plaintextHash(input.Reason)
	tag, err := tx.Exec(ctx, `INSERT INTO contracts.admin_access_audit(id,tenant_id,actor_user_id,membership_id,session_id,action,resource_kind,request_id,reason,reason_hash,occurred_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (tenant_id,action,request_id) DO NOTHING`, auditID, input.TenantID, input.UserID, input.MembershipID, input.SessionID, input.Action, input.ResourceKind, input.RequestID, input.Reason, reasonHash, input.OccurredAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 0 {
		return nil
	}
	var replay bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM contracts.admin_access_audit WHERE tenant_id=$1 AND action=$2 AND request_id=$3 AND id=$4 AND actor_user_id=$5 AND membership_id=$6 AND session_id=$7 AND resource_kind=$8 AND reason_hash=$9)`, input.TenantID, input.Action, input.RequestID, auditID, input.UserID, input.MembershipID, input.SessionID, input.ResourceKind, reasonHash).Scan(&replay); err != nil {
		return err
	}
	if !replay {
		return contractsapi.ErrStateConflict
	}
	return nil
}

func (service ControlService) encodeAuditCursor(tenantID string, beforeAt time.Time, beforeID string, expiresAt time.Time) (string, error) {
	if uuid.Validate(tenantID) != nil || uuid.Validate(beforeID) != nil || beforeAt.IsZero() || expiresAt.IsZero() {
		return "", errors.New("invalid audit cursor")
	}
	payload, err := json.Marshal(signedAuditCursor{Version: 1, TenantID: tenantID, BeforeAt: beforeAt.UTC().Format(time.RFC3339Nano), BeforeID: beforeID, ExpiresAt: expiresAt.Unix()})
	if err != nil {
		return "", err
	}
	signature := service.signAuditCursor(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (service ControlService) decodeAuditCursor(token, tenantID string, now time.Time) (time.Time, string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(token) > 2048 {
		return time.Time{}, "", errors.New("invalid audit cursor")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return time.Time{}, "", errors.New("invalid audit cursor")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, service.signAuditCursor(payload)) {
		return time.Time{}, "", errors.New("invalid audit cursor")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var cursor signedAuditCursor
	if decoder.Decode(&cursor) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || cursor.Version != 1 || cursor.TenantID != tenantID || uuid.Validate(cursor.BeforeID) != nil || cursor.ExpiresAt <= now.Unix() || cursor.ExpiresAt > now.Add(time.Hour).Unix() {
		return time.Time{}, "", errors.New("invalid audit cursor")
	}
	beforeAt, err := time.Parse(time.RFC3339Nano, cursor.BeforeAt)
	if err != nil {
		return time.Time{}, "", errors.New("invalid audit cursor")
	}
	return beforeAt.UTC(), cursor.BeforeID, nil
}

func (service ControlService) signAuditCursor(payload []byte) []byte {
	derive := hmac.New(sha256.New, service.IDKey)
	_, _ = derive.Write([]byte("contract-audit-cursor-signing-v1"))
	signer := hmac.New(sha256.New, derive.Sum(nil))
	_, _ = signer.Write(payload)
	return signer.Sum(nil)
}
