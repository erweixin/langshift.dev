package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/identity/session"
)

type SessionStore struct {
	Pool *pgxpool.Pool
	Now  func() time.Time
}

type SwitchTenantInput struct {
	SessionID       string
	UserID          string
	TargetTenantID  string
	ExpectedVersion uint64
	SecurityEventID string
	RequestID       string
	IPHash          []byte
	UserAgentHash   []byte
}

func (store SessionStore) SwitchActiveTenant(ctx context.Context, input SwitchTenantInput) (uint64, error) {
	if store.Pool == nil || input.SessionID == "" || input.UserID == "" || input.TargetTenantID == "" || input.ExpectedVersion == 0 || input.SecurityEventID == "" || input.RequestID == "" {
		return 0, session.ErrUnauthenticated
	}
	now := time.Now().UTC()
	if store.Now != nil {
		now = store.Now().UTC()
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var currentTenantID string
	var currentVersion uint64
	err = tx.QueryRow(ctx, `SELECT active_tenant_id::text, version FROM identity.sessions WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL AND expires_at>$3 FOR UPDATE`, input.SessionID, input.UserID, now).Scan(&currentTenantID, &currentVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, session.ErrUnauthenticated
		}
		return 0, err
	}
	if currentVersion != input.ExpectedVersion {
		return 0, session.ErrVersionConflict
	}
	if currentTenantID == input.TargetTenantID {
		if err = tx.Commit(ctx); err != nil {
			return 0, err
		}
		return currentVersion, nil
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id', $1, true)`, input.TargetTenantID); err != nil {
		return 0, err
	}
	var membershipID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM identity.memberships WHERE tenant_id=$1 AND user_id=$2 AND status='active'`, input.TargetTenantID, input.UserID).Scan(&membershipID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, session.ErrTenantUnavailable
		}
		return 0, err
	}
	var nextVersion uint64
	err = tx.QueryRow(ctx, `UPDATE identity.sessions SET active_tenant_id=$1, version=version+1, updated_at=$2 WHERE id=$3 AND user_id=$4 AND version=$5 RETURNING version`, input.TargetTenantID, now, input.SessionID, input.UserID, input.ExpectedVersion).Scan(&nextVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, session.ErrVersionConflict
		}
		return 0, err
	}
	details, err := json.Marshal(map[string]string{"previous_tenant_id": currentTenantID, "active_tenant_id": input.TargetTenantID, "membership_id": membershipID})
	if err != nil {
		return 0, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,subject_user_id,actor_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES ($1,$2,$3,$3,'session_active_tenant_changed',$4,$5,$6,$7,$8)`, input.SecurityEventID, input.TargetTenantID, input.UserID, input.RequestID, input.IPHash, input.UserAgentHash, details, now)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return nextVersion, nil
}
