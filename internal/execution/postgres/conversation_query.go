package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	executionapi "github.com/langshift/lites/internal/execution/api"
	"github.com/langshift/lites/internal/payload"
)

type conversationMessagePointer struct {
	ID, RunID, Role, Ref, Hash string
	CreatedAt                  time.Time
}

type conversationMessageDocument struct {
	SchemaVersion int    `json:"schema_version"`
	Role          string `json:"role"`
	Content       []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// GetConversation returns an owner-scoped, bounded page of displayable
// conversation messages. Product context and tool messages are deliberately
// excluded so private Agent prompts never cross the public API boundary.
func (service ControlService) GetConversation(ctx context.Context, command executionapi.GetConversationCommand) (executionapi.ConversationDetailResult, error) {
	if service.Pool == nil || service.Payloads == nil || command.TenantID == "" || command.UserID == "" || command.ConversationID == "" || command.Limit < 1 || command.Limit > 100 || (command.BeforeCreatedAt == nil) != (command.BeforeMessageID == "") {
		return executionapi.ConversationDetailResult{}, executionapi.ErrValidation
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return executionapi.ConversationDetailResult{}, executionapi.ErrDependencyUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return executionapi.ConversationDetailResult{}, executionapi.ErrDependencyUnavailable
	}
	var result executionapi.ConversationDetailResult
	err = tx.QueryRow(ctx, `SELECT id::text,mission_id::text,version,title,mode,status,updated_at FROM agent.conversations WHERE tenant_id=$1 AND user_id=$2 AND id::text=$3`, command.TenantID, command.UserID, command.ConversationID).
		Scan(&result.ID, &result.MissionID, &result.Version, &result.Title, &result.Mode, &result.Status, &result.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return executionapi.ConversationDetailResult{}, executionapi.ErrResourceNotFound
	}
	if err != nil {
		return executionapi.ConversationDetailResult{}, executionapi.ErrDependencyUnavailable
	}
	rows, err := tx.Query(ctx, `SELECT m.id::text,m.run_id::text,m.role,m.payload_ref,m.payload_hash,m.created_at
		FROM agent.run_messages m
		JOIN agent.runs r ON r.tenant_id=m.tenant_id AND r.id=m.run_id
		WHERE m.tenant_id=$1 AND m.user_id=$2 AND r.user_id=$2 AND r.conversation_id::text=$3
		  AND m.source_kind IN ('conversation_user','agent_output')
		  AND ($4::timestamptz IS NULL OR (m.created_at,m.id::text)<($4,$5))
		ORDER BY m.created_at DESC,m.id::text DESC LIMIT $6`, command.TenantID, command.UserID, command.ConversationID, command.BeforeCreatedAt, command.BeforeMessageID, command.Limit+1)
	if err != nil {
		return executionapi.ConversationDetailResult{}, executionapi.ErrDependencyUnavailable
	}
	defer rows.Close()
	pointers := make([]conversationMessagePointer, 0, command.Limit+1)
	for rows.Next() {
		var item conversationMessagePointer
		if err = rows.Scan(&item.ID, &item.RunID, &item.Role, &item.Ref, &item.Hash, &item.CreatedAt); err != nil {
			return executionapi.ConversationDetailResult{}, executionapi.ErrDependencyUnavailable
		}
		item.CreatedAt = item.CreatedAt.UTC()
		pointers = append(pointers, item)
	}
	if err = rows.Err(); err != nil {
		return executionapi.ConversationDetailResult{}, executionapi.ErrDependencyUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return executionapi.ConversationDetailResult{}, executionapi.ErrDependencyUnavailable
	}
	if len(pointers) > command.Limit {
		oldest := pointers[command.Limit-1]
		cursor, marshalErr := json.Marshal(struct {
			CreatedAt time.Time `json:"created_at"`
			ID        string    `json:"id"`
		}{oldest.CreatedAt, oldest.ID})
		if marshalErr != nil {
			return executionapi.ConversationDetailResult{}, executionapi.ErrDependencyUnavailable
		}
		value := base64.RawURLEncoding.EncodeToString(cursor)
		result.NextCursor = &value
		pointers = pointers[:command.Limit]
	}
	result.Messages = make([]executionapi.ConversationMessageResult, 0, len(pointers))
	for index := len(pointers) - 1; index >= 0; index-- {
		item := pointers[index]
		encoded, getErr := service.Payloads.Get(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: item.ID, Class: controlMessageClass, ContentType: "application/json"}, payload.Manifest{Ref: item.Ref, Hash: item.Hash})
		if getErr != nil {
			return executionapi.ConversationDetailResult{}, executionapi.ErrDependencyUnavailable
		}
		var document conversationMessageDocument
		if json.Unmarshal(encoded, &document) != nil || document.SchemaVersion != 1 || document.Role != item.Role {
			return executionapi.ConversationDetailResult{}, executionapi.ErrDependencyUnavailable
		}
		parts := make([]string, 0, len(document.Content))
		for _, block := range document.Content {
			if (block.Type == "text" || block.Type == "output_text") && block.Text != "" {
				parts = append(parts, block.Text)
			}
		}
		if len(parts) == 0 {
			continue
		}
		result.Messages = append(result.Messages, executionapi.ConversationMessageResult{ID: item.ID, RunID: item.RunID, Role: item.Role, Content: strings.Join(parts, "\n"), CreatedAt: item.CreatedAt})
	}
	result.UpdatedAt = result.UpdatedAt.UTC()
	return result, nil
}
