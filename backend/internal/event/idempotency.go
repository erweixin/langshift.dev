package event

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func (s *Service) reserveIdempotency(ctx context.Context, tx pgx.Tx, request AppendRequest) (string, bool, *IdempotencyResponse, error) {
	commandID, err := s.ids.NewID()
	if err != nil {
		return "", false, nil, fmt.Errorf("generate idempotency command id: %w", err)
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO idempotency_keys (
			user_id, scope, key, command_id, request_hash
		)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, scope, key) DO NOTHING
	`, request.UserID, request.Idempotency.Scope, request.Idempotency.Key, commandID, request.Idempotency.RequestHash)
	if err != nil {
		return "", false, nil, fmt.Errorf("reserve idempotency key: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return commandID, false, nil, nil
	}

	var existingCommandID string
	var existingHash string
	var status sql.NullInt64
	var body sql.NullString
	if err := tx.QueryRow(ctx, `
		SELECT command_id, request_hash, response_status, response_body::text
		FROM idempotency_keys
		WHERE user_id = $1 AND scope = $2 AND key = $3
		FOR UPDATE
	`, request.UserID, request.Idempotency.Scope, request.Idempotency.Key).Scan(
		&existingCommandID,
		&existingHash,
		&status,
		&body,
	); err != nil {
		return "", false, nil, fmt.Errorf("read idempotency key: %w", err)
	}

	if existingHash != request.Idempotency.RequestHash {
		return "", false, nil, ErrIdempotencyConflict
	}
	if !status.Valid || !body.Valid {
		return "", false, nil, ErrIdempotencyInFlight
	}

	return existingCommandID, true, &IdempotencyResponse{
		Status: int(status.Int64),
		Body:   json.RawMessage(body.String),
	}, nil
}

func storeIdempotencyResponse(ctx context.Context, tx pgx.Tx, request AppendRequest, response *IdempotencyResponse) error {
	tag, err := tx.Exec(ctx, `
		UPDATE idempotency_keys
		SET response_status = $4,
			response_body = $5::jsonb,
			updated_at = now()
		WHERE user_id = $1 AND scope = $2 AND key = $3
	`, request.UserID, request.Idempotency.Scope, request.Idempotency.Key, response.Status, string(response.Body))
	if err != nil {
		return fmt.Errorf("store idempotency response: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("store idempotency response: %w", ErrIdempotencyInFlight)
	}
	return nil
}
