package agentworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/behavior"
	"github.com/langshift/lites/internal/llmgateway/provider"
	"github.com/langshift/lites/internal/payload"
)

var (
	ErrContextConfiguration = errors.New("agent context source configuration is invalid")
	ErrContextFence         = errors.New("agent context source is not bound to the active Run fence")
	ErrContextIntegrity     = errors.New("agent context source integrity check failed")
)

type MessageDocument struct {
	SchemaVersion int                     `json:"schema_version"`
	Role          string                  `json:"role"`
	Content       []provider.ContentBlock `json:"content"`
}

type MessageSource struct {
	ID, RunID, Role, SourceKind, TrustLabel, ContentHash string
	Version                                              uint64
	Index                                                int
	Document                                             MessageDocument
}

type ContextSources struct {
	RunID, ConversationID string
	RunVersion            uint64
	RunHash               string
	Budget                json.RawMessage
	Behavior              behavior.Manifest
	BehaviorSnapshotID    string
	BehaviorManifestHash  string
	Messages              []MessageSource
}

// ContextSourceStore captures immutable metadata in one RLS transaction and
// then opens the content-addressed message envelopes. A later LLM-attempt
// transaction rechecks the same Run fence before any provider dispatch.
type ContextSourceStore struct {
	Pool                *pgxpool.Pool
	Payloads            payload.Store
	MaximumMessageBytes int
	MaximumMessages     int
}

func (store ContextSourceStore) Load(ctx context.Context, execution Execution) (ContextSources, error) {
	if store.Pool == nil || store.Payloads == nil || execution.Claim.TenantID == "" || execution.Claim.RunID == "" || execution.Claim.UserID == "" || execution.Claim.CommandID == "" || execution.Claim.AttemptID == "" || execution.Claim.RunVersion < 1 || execution.Claim.Fence < 1 || store.maximumMessageBytes() < 1 || store.maximumMessages() < 1 || store.maximumMessages() > 4096 {
		return ContextSources{}, ErrContextConfiguration
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return ContextSources{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, execution.Claim.TenantID); err != nil {
		return ContextSources{}, err
	}
	var result ContextSources
	var budget, behaviorJSON json.RawMessage
	var runCreated time.Time
	err = tx.QueryRow(ctx, `SELECT r.id::text,r.conversation_id::text,r.run_version,r.budget_snapshot,r.profile_snapshot_id,r.created_at,b.manifest,b.manifest_hash
		FROM agent.runs r JOIN agent.behavior_snapshots b ON b.tenant_id=r.tenant_id AND b.snapshot_id=r.profile_snapshot_id
		WHERE r.tenant_id=$1 AND r.id=$2 AND r.user_id=$3 AND r.status='executing' AND r.run_version=$4
		  AND r.active_command_id=$5 AND r.active_attempt_id=$6 AND r.current_fence=$7 AND r.lease_expires_at>CURRENT_TIMESTAMP
		  AND r.cancel_requested_at IS NULL`, execution.Claim.TenantID, execution.Claim.RunID, execution.Claim.UserID, execution.Claim.RunVersion, execution.Claim.CommandID, execution.Claim.AttemptID, execution.Claim.Fence).
		Scan(&result.RunID, &result.ConversationID, &result.RunVersion, &budget, &result.BehaviorSnapshotID, &runCreated, &behaviorJSON, &result.BehaviorManifestHash)
	if err != nil {
		return ContextSources{}, ErrContextFence
	}
	if json.Unmarshal(behaviorJSON, &result.Behavior) != nil {
		return ContextSources{}, ErrContextIntegrity
	}
	canonical, err := result.Behavior.Canonical()
	if err != nil {
		return ContextSources{}, ErrContextIntegrity
	}
	manifestHash, err := canonical.Hash()
	if err != nil || manifestHash != result.BehaviorManifestHash {
		return ContextSources{}, ErrContextIntegrity
	}
	result.Behavior = canonical
	result.Budget = append(json.RawMessage(nil), budget...)
	result.RunHash, err = hashJSON(struct {
		RunID, ConversationID, BehaviorSnapshotID, BehaviorManifestHash string
		RunVersion                                                      uint64
		Budget                                                          json.RawMessage
	}{result.RunID, result.ConversationID, result.BehaviorSnapshotID, result.BehaviorManifestHash, result.RunVersion, result.Budget})
	if err != nil {
		return ContextSources{}, ErrContextIntegrity
	}
	type metadata struct {
		ID, RunID, Role, PayloadRef, PayloadHash, ContentHash, SourceKind, TrustLabel string
		Version                                                                       uint64
		Index                                                                         int
	}
	rows, err := tx.Query(ctx, `SELECT m.id::text,m.run_id::text,m.version,m.role,m.message_index,m.payload_ref,m.payload_hash,m.content_hash,m.source_kind,m.trust_label
		FROM agent.run_messages m JOIN agent.runs source ON source.tenant_id=m.tenant_id AND source.id=m.run_id
		WHERE m.tenant_id=$1 AND m.user_id=$2 AND source.conversation_id=$3
		  AND (source.created_at<$4 OR (source.created_at=$4 AND source.id=$5))
		  AND (m.source_kind<>'product_context' OR source.id=$5)
		ORDER BY source.created_at,source.id,m.message_index,m.id LIMIT $6`, execution.Claim.TenantID, execution.Claim.UserID, result.ConversationID, runCreated, result.RunID, store.maximumMessages()+1)
	if err != nil {
		return ContextSources{}, err
	}
	var messages []metadata
	for rows.Next() {
		var item metadata
		if err = rows.Scan(&item.ID, &item.RunID, &item.Version, &item.Role, &item.Index, &item.PayloadRef, &item.PayloadHash, &item.ContentHash, &item.SourceKind, &item.TrustLabel); err != nil {
			rows.Close()
			return ContextSources{}, err
		}
		messages = append(messages, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return ContextSources{}, err
	}
	rows.Close()
	if len(messages) > store.maximumMessages() {
		return ContextSources{}, ErrContextIntegrity
	}
	if err = tx.Commit(ctx); err != nil {
		return ContextSources{}, err
	}
	result.Messages = make([]MessageSource, 0, len(messages))
	for _, item := range messages {
		document, loadErr := store.loadMessage(ctx, execution.Claim.TenantID, item.ID, item.PayloadRef, item.PayloadHash, item.ContentHash, item.Role)
		if loadErr != nil {
			return ContextSources{}, loadErr
		}
		result.Messages = append(result.Messages, MessageSource{ID: item.ID, RunID: item.RunID, Role: item.Role, SourceKind: item.SourceKind, TrustLabel: item.TrustLabel, ContentHash: item.ContentHash, Version: item.Version, Index: item.Index, Document: document})
	}
	return result, nil
}

func (store ContextSourceStore) loadMessage(ctx context.Context, tenantID, messageID, ref, payloadHash, contentHash, role string) (MessageDocument, error) {
	encoded, err := store.Payloads.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: messageID, Class: "run-message", ContentType: "application/json"}, payload.Manifest{Ref: ref, Hash: payloadHash})
	if err != nil || len(encoded) == 0 || len(encoded) > store.maximumMessageBytes() {
		return MessageDocument{}, ErrContextIntegrity
	}
	digest := sha256.Sum256(encoded)
	if hex.EncodeToString(digest[:]) != contentHash {
		return MessageDocument{}, ErrContextIntegrity
	}
	var document MessageDocument
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&document) != nil || document.SchemaVersion != 1 || document.Role != role || len(document.Content) == 0 || len(document.Content) > 1024 {
		return MessageDocument{}, ErrContextIntegrity
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return MessageDocument{}, ErrContextIntegrity
	}
	for _, block := range document.Content {
		if block.Type == "" {
			return MessageDocument{}, ErrContextIntegrity
		}
	}
	return document, nil
}

func hashJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (store ContextSourceStore) maximumMessageBytes() int {
	if store.MaximumMessageBytes == 0 {
		return 4 << 20
	}
	return store.MaximumMessageBytes
}

func (store ContextSourceStore) maximumMessages() int {
	if store.MaximumMessages == 0 {
		return 4096
	}
	return store.MaximumMessages
}
