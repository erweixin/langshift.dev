package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/langshift/lites/internal/identity/api"
	"github.com/langshift/lites/internal/platform/cursor"
)

const maximumTenantsPage = 100

type tenantCursor struct {
	JoinedAt time.Time `json:"joined_at"`
	ID       string    `json:"id"`
}

func (service AuthService) ListTenants(ctx context.Context, query api.TenantsQuery) (api.TenantsPage, error) {
	if err := service.validateSessionRequest(query.AuthenticatedRequestMetadata); err != nil {
		return api.TenantsPage{}, err
	}
	if query.Limit < 1 || query.Limit > maximumTenantsPage {
		return api.TenantsPage{}, api.ErrValidation
	}
	var after tenantCursor
	if query.Cursor != "" {
		if err := (cursor.Codec{Key: service.CursorKey}).Decode(query.Cursor, "tenants.list:"+query.UserID, &after); err != nil || after.ID == "" || after.JoinedAt.IsZero() {
			return api.TenantsPage{}, api.ErrValidation
		}
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return api.TenantsPage{}, api.ErrDependencyUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := service.now()
	if err = service.authorizeSession(ctx, tx, query.AuthenticatedRequestMetadata, now, false); err != nil {
		return api.TenantsPage{}, err
	}
	rows, err := tx.Query(ctx, `SELECT tenant_id::text,tenant_version,kind,name,status,region,membership_id::text,membership_version,role,joined_at,updated_at FROM identity.list_user_tenants($1,$2,$3,$4,$5,$6)`, query.SessionID, query.UserID, query.TenantID, nullableTime(after.JoinedAt), nullableString(after.ID), query.Limit+1)
	if err != nil {
		return api.TenantsPage{}, api.ErrDependencyUnavailable
	}
	items := make([]api.TenantItem, 0, query.Limit+1)
	for rows.Next() {
		var item api.TenantItem
		if err = rows.Scan(&item.ID, &item.Version, &item.Kind, &item.Name, &item.Status, &item.Region, &item.MembershipID, &item.MembershipVersion, &item.Role, &item.JoinedAt, &item.UpdatedAt); err != nil {
			rows.Close()
			return api.TenantsPage{}, api.ErrDependencyUnavailable
		}
		item.Active = item.ID == query.TenantID
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return api.TenantsPage{}, api.ErrDependencyUnavailable
	}
	rows.Close()
	var next *string
	if len(items) > query.Limit {
		last := items[query.Limit-1]
		encoded, encodeErr := (cursor.Codec{Key: service.CursorKey}).Encode("tenants.list:"+query.UserID, tenantCursor{JoinedAt: last.JoinedAt, ID: last.ID})
		if encodeErr != nil {
			return api.TenantsPage{}, api.ErrDependencyUnavailable
		}
		next = &encoded
		items = items[:query.Limit]
	}
	auditID, err := service.newID()
	if err != nil {
		return api.TenantsPage{}, api.ErrDependencyUnavailable
	}
	details, _ := json.Marshal(map[string]any{"returned_count": len(items), "cursor_used": query.Cursor != ""})
	if _, err = tx.Exec(ctx, `INSERT INTO identity.security_events(id,tenant_id,actor_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES($1,$2,$3,'tenants_listed',$4,$5,$6,$7,$8)`, auditID, query.TenantID, query.UserID, query.RequestID, query.ClientIPHash, query.UserAgentHash, details, now); err != nil {
		return api.TenantsPage{}, api.ErrDependencyUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return api.TenantsPage{}, api.ErrDependencyUnavailable
	}
	return api.TenantsPage{Items: items, NextCursor: next}, nil
}

var _ api.TenantService = AuthService{}
