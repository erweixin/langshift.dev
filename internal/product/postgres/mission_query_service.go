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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	productapi "github.com/langshift/lites/internal/product/api"
)

const missionPageSize = 50

type MissionQueryService struct {
	Pool      *pgxpool.Pool
	CursorKey []byte
}

type missionCursor struct {
	TenantID string    `json:"tenant_id"`
	UserID   string    `json:"user_id"`
	Created  time.Time `json:"created_at"`
	ID       string    `json:"id"`
}

func (service MissionQueryService) List(ctx context.Context, query productapi.ListMissionsQuery) (productapi.MissionListResult, error) {
	if service.Pool == nil || len(service.CursorKey) < 32 || query.TenantID == "" || query.UserID == "" {
		return productapi.MissionListResult{}, productapi.ErrValidation
	}
	var cursor *missionCursor
	if query.Cursor != "" {
		decoded, err := service.decodeCursor(query.Cursor)
		if err != nil || decoded.TenantID != query.TenantID || decoded.UserID != query.UserID {
			return productapi.MissionListResult{}, productapi.ErrValidation
		}
		cursor = &decoded
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return productapi.MissionListResult{}, errors.Join(productapi.ErrDependencyUnavailable, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, query.TenantID); err != nil {
		return productapi.MissionListResult{}, errors.Join(productapi.ErrDependencyUnavailable, err)
	}
	focus, err := readMissionFocus(ctx, tx, query.TenantID, query.UserID)
	if err != nil {
		return productapi.MissionListResult{}, errors.Join(productapi.ErrDependencyUnavailable, err)
	}
	args := []any{query.TenantID, query.UserID, missionPageSize + 1}
	statement := `SELECT id::text,version,status,source_role_profile_id::text,target_role_profile_id::text,current_route_revision_id::text,created_at,updated_at FROM product.missions WHERE tenant_id=$1 AND user_id=$2`
	if cursor != nil {
		statement += ` AND (created_at < $4 OR (created_at = $4 AND id < $5::uuid))`
		args = append(args, cursor.Created, cursor.ID)
	}
	statement += ` ORDER BY created_at DESC,id DESC LIMIT $3`
	rows, err := tx.Query(ctx, statement, args...)
	if err != nil {
		return productapi.MissionListResult{}, errors.Join(productapi.ErrDependencyUnavailable, err)
	}
	defer rows.Close()
	items := make([]productapi.MissionResource, 0, missionPageSize+1)
	for rows.Next() {
		var item productapi.MissionResource
		if err = rows.Scan(&item.ID, &item.Version, &item.Status, &item.SourceRoleProfileID, &item.TargetRoleProfileID, &item.CurrentRouteRevisionID, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return productapi.MissionListResult{}, errors.Join(productapi.ErrDependencyUnavailable, err)
		}
		item.Focused = focus.MissionID != nil && *focus.MissionID == item.ID
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return productapi.MissionListResult{}, errors.Join(productapi.ErrDependencyUnavailable, err)
	}
	var next *string
	if len(items) > missionPageSize {
		items = items[:missionPageSize]
		last := items[len(items)-1]
		encoded, encodeErr := service.encodeCursor(missionCursor{TenantID: query.TenantID, UserID: query.UserID, Created: last.CreatedAt, ID: last.ID})
		if encodeErr != nil {
			return productapi.MissionListResult{}, errors.Join(productapi.ErrDependencyUnavailable, encodeErr)
		}
		next = &encoded
	}
	if err = tx.Commit(ctx); err != nil {
		return productapi.MissionListResult{}, errors.Join(productapi.ErrDependencyUnavailable, err)
	}
	return productapi.MissionListResult{Items: items, Focus: focus, NextCursor: next}, nil
}

func (service MissionQueryService) Get(ctx context.Context, query productapi.GetMissionQuery) (productapi.MissionResource, error) {
	if service.Pool == nil || query.TenantID == "" || query.UserID == "" || query.MissionID == "" {
		return productapi.MissionResource{}, productapi.ErrValidation
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return productapi.MissionResource{}, errors.Join(productapi.ErrDependencyUnavailable, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, query.TenantID); err != nil {
		return productapi.MissionResource{}, errors.Join(productapi.ErrDependencyUnavailable, err)
	}
	focus, err := readMissionFocus(ctx, tx, query.TenantID, query.UserID)
	if err != nil {
		return productapi.MissionResource{}, errors.Join(productapi.ErrDependencyUnavailable, err)
	}
	var item productapi.MissionResource
	err = tx.QueryRow(ctx, `SELECT id::text,version,status,source_role_profile_id::text,target_role_profile_id::text,current_route_revision_id::text,created_at,updated_at FROM product.missions WHERE tenant_id=$1 AND user_id=$2 AND id=$3`, query.TenantID, query.UserID, query.MissionID).Scan(&item.ID, &item.Version, &item.Status, &item.SourceRoleProfileID, &item.TargetRoleProfileID, &item.CurrentRouteRevisionID, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return productapi.MissionResource{}, productapi.ErrResourceNotFound
	}
	if err != nil {
		return productapi.MissionResource{}, errors.Join(productapi.ErrDependencyUnavailable, err)
	}
	item.Focused = focus.MissionID != nil && *focus.MissionID == item.ID
	if err = tx.Commit(ctx); err != nil {
		return productapi.MissionResource{}, errors.Join(productapi.ErrDependencyUnavailable, err)
	}
	return item, nil
}

func readMissionFocus(ctx context.Context, tx pgx.Tx, tenantID, userID string) (productapi.MissionFocus, error) {
	var focus productapi.MissionFocus
	err := tx.QueryRow(ctx, `SELECT mission_id::text,focus_version FROM product.mission_focuses WHERE tenant_id=$1 AND user_id=$2`, tenantID, userID).Scan(&focus.MissionID, &focus.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return focus, nil
	}
	return focus, err
}

func (service MissionQueryService) encodeCursor(cursor missionCursor) (string, error) {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, service.CursorKey)
	_, _ = mac.Write(payload)
	value := append(payload, mac.Sum(nil)...)
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (service MissionQueryService) decodeCursor(value string) (missionCursor, error) {
	if len(value) > 2048 {
		return missionCursor{}, productapi.ErrValidation
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) <= sha256.Size {
		return missionCursor{}, productapi.ErrValidation
	}
	payload, signature := decoded[:len(decoded)-sha256.Size], decoded[len(decoded)-sha256.Size:]
	mac := hmac.New(sha256.New, service.CursorKey)
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return missionCursor{}, productapi.ErrValidation
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var cursor missionCursor
	if decoder.Decode(&cursor) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || cursor.TenantID == "" || cursor.UserID == "" || cursor.ID == "" || cursor.Created.IsZero() {
		return missionCursor{}, productapi.ErrValidation
	}
	return cursor, nil
}
