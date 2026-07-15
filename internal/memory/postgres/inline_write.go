// Package postgres implements governed Memory and retrieval persistence on the
// shared PostgreSQL EventStore.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/platform/ids"
)

var (
	ErrConfiguration = errors.New("memory inline handler is not configured")
	ErrInvalidWrite  = errors.New("memory write is invalid")
	ErrPolicy        = errors.New("memory write is denied by policy")
	ErrScope         = errors.New("memory scope is not writable")
	ErrVersion       = errors.New("memory version conflicts with durable state")
)

type SourceReference struct {
	Kind    string
	Ref     string
	Version uint64
}

type RevisionReference struct {
	MemoryID string
	Version  uint64
}

type MemoryWrite struct {
	MemoryID, ScopeKind, ScopeID, MemoryKind string
	ExpectedVersion                          uint64
	ContentRef                               string
	ContentHMAC                              [32]byte
	ContentType, SummaryRef                  string
	SensitivityLabels                        []string
	SourceKind                               string
	Sources                                  []SourceReference
	DataSubjectIDs                           []string
	DerivedFrom                              []RevisionReference
	DerivationKind                           string
	Confidence                               float64
	EncryptionSubjectID, KeyRef              string
	EmbeddingModelID, EmbeddingModelVersion  string
	VectorDimensions                         int
	Tags                                     []string
	PolicyVersion, IndexGeneration           uint64
	GuardrailSnapshotID                      string
	ExpiresAt                                *time.Time
	UpsertedEvent                            executionpostgres.PayloadPointer
}

type InlineWriteHandler struct {
	Appender eventpostgres.Appender
	IDKey    []byte
}

func (handler InlineWriteHandler) Validate(value any) bool {
	write, ok := value.(MemoryWrite)
	return ok && len(handler.IDKey) >= 32 && validWrite(write)
}

func (handler InlineWriteHandler) Commit(ctx context.Context, tx pgx.Tx, tool executionpostgres.InlinePlatformToolContext, value any) error {
	write, ok := value.(MemoryWrite)
	if !ok || tx == nil || len(handler.IDKey) < 32 || tool.TenantID == "" || tool.UserID == "" || tool.RunID == "" || tool.ToolCallID == "" || tool.ToolCallVersion != 2 || tool.ToolSucceededEventID == "" || tool.StoreEpoch == "" || !validWrite(write) {
		return ErrInvalidWrite
	}
	now := tool.Now.UTC().Truncate(time.Microsecond)
	if now.IsZero() || write.ExpiresAt != nil && !write.ExpiresAt.After(now) {
		return ErrInvalidWrite
	}
	var retentionDays *int
	err := tx.QueryRow(ctx, `SELECT retention_days FROM product.memory_policies WHERE tenant_id=$1 AND user_id=$2 AND enabled AND version=$3 AND allowed_kinds ? $4`, tool.TenantID, tool.UserID, write.PolicyVersion, write.MemoryKind).Scan(&retentionDays)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPolicy
		}
		return fmt.Errorf("load memory policy: %w", err)
	}
	if retentionDays != nil {
		maximumExpiry := now.Add(time.Duration(*retentionDays) * 24 * time.Hour)
		if write.ExpiresAt == nil || write.ExpiresAt.After(maximumExpiry) {
			return fmt.Errorf("%w: retention expiry exceeds %s", ErrPolicy, maximumExpiry.Format(time.RFC3339Nano))
		}
	}
	if err = validateWritableScope(ctx, tx, tool, write); err != nil {
		return err
	}
	memoryVersion := write.ExpectedVersion + 1
	eventIDs, err := handler.eventIDs(write.MemoryID, memoryVersion, tool.ToolSucceededEventID)
	if err != nil {
		return ErrConfiguration
	}
	if write.ExpectedVersion == 0 {
		if _, err = tx.Exec(ctx, `INSERT INTO agent.memory_documents(id,tenant_id,user_id,status,version,scope_kind,scope_id,current_revision,upserted_event_id,created_at,updated_at) VALUES($1,$2,$3,'active',1,$4,$5,1,$6,$7,$7)`, write.MemoryID, tool.TenantID, tool.UserID, write.ScopeKind, write.ScopeID, eventIDs.event, now); err != nil {
			return ErrVersion
		}
	} else {
		tag, updateErr := tx.Exec(ctx, `UPDATE agent.memory_documents SET version=$1,current_revision=$1,upserted_event_id=$2,updated_at=$3 WHERE tenant_id=$4 AND id=$5 AND user_id=$6 AND status='active' AND version=$7 AND current_revision=$7 AND scope_kind=$8 AND scope_id=$9`, memoryVersion, eventIDs.event, now, tool.TenantID, write.MemoryID, tool.UserID, write.ExpectedVersion, write.ScopeKind, write.ScopeID)
		if updateErr != nil || tag.RowsAffected() != 1 {
			return ErrVersion
		}
	}
	labels, err := json.Marshal(write.SensitivityLabels)
	if err != nil {
		return ErrInvalidWrite
	}
	tags, err := json.Marshal(write.Tags)
	if err != nil {
		return ErrInvalidWrite
	}
	var summary any
	if write.SummaryRef != "" {
		summary = write.SummaryRef
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.memory_document_revisions(tenant_id,memory_id,memory_version,user_id,scope_kind,scope_id,memory_kind,content_ref,content_hmac,content_type,summary_ref,sensitivity_labels,source_kind,derivation_kind,confidence,encryption_subject_id,key_ref,embedding_model_id,tags,tool_call_id,tool_call_version,policy_version,guardrail_snapshot_id,index_generation,expires_at,upserted_event_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27)`, tool.TenantID, write.MemoryID, memoryVersion, tool.UserID, write.ScopeKind, write.ScopeID, write.MemoryKind, write.ContentRef, write.ContentHMAC[:], write.ContentType, summary, labels, write.SourceKind, write.DerivationKind, write.Confidence, write.EncryptionSubjectID, write.KeyRef, write.EmbeddingModelID, tags, tool.ToolCallID, tool.ToolCallVersion, write.PolicyVersion, write.GuardrailSnapshotID, write.IndexGeneration, write.ExpiresAt, eventIDs.event, now); err != nil {
		return err
	}
	for index, source := range write.Sources {
		var version any
		if source.Version > 0 {
			version = source.Version
		}
		if _, err = tx.Exec(ctx, `INSERT INTO agent.memory_revision_sources(tenant_id,memory_id,memory_version,source_ordinal,source_kind,source_ref,source_version,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, tool.TenantID, write.MemoryID, memoryVersion, index+1, source.Kind, source.Ref, version, now); err != nil {
			return err
		}
	}
	for _, subjectID := range write.DataSubjectIDs {
		if _, err = tx.Exec(ctx, `INSERT INTO agent.memory_revision_subjects(tenant_id,memory_id,memory_version,data_subject_id,created_at) VALUES($1,$2,$3,$4,$5)`, tool.TenantID, write.MemoryID, memoryVersion, subjectID, now); err != nil {
			return err
		}
	}
	for _, source := range write.DerivedFrom {
		if _, err = tx.Exec(ctx, `INSERT INTO agent.memory_revision_derivations(tenant_id,memory_id,memory_version,source_memory_id,source_memory_version,created_at) VALUES($1,$2,$3,$4,$5,$6)`, tool.TenantID, write.MemoryID, memoryVersion, source.MemoryID, source.Version, now); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.memory_index_projections(tenant_id,memory_id,memory_version,index_generation,embedding_model_id,embedding_model_version,vector_dimensions,status,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,'pending',$8,$8)`, tool.TenantID, write.MemoryID, memoryVersion, write.IndexGeneration, write.EmbeddingModelID, write.EmbeddingModelVersion, write.VectorDimensions, now); err != nil {
		return err
	}
	causationID := tool.ToolSucceededEventID
	event := eventpostgres.Input{Event: eventpostgres.Event{ID: eventIDs.event, TenantID: tool.TenantID, UserID: tool.UserID, EventType: "MemoryUpserted", SchemaVersion: 2, AggregateKind: "memory_document", AggregateID: write.MemoryID, AggregateVersion: memoryVersion, StoreEpoch: tool.StoreEpoch, OccurredAt: now, Actor: tool.Actor, CausationID: &causationID, CorrelationID: tool.CorrelationID, PayloadRef: write.UpsertedEvent.Ref, PayloadHash: write.UpsertedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: eventIDs.outbox, CommandID: eventIDs.publish, CommandType: "events.publish", PayloadRef: write.UpsertedEvent.Ref, PayloadHash: write.UpsertedEvent.Hash}}}
	if _, err = handler.Appender.Append(ctx, tx, event); err != nil {
		return err
	}
	return nil
}

type memoryEventIDs struct{ event, outbox, publish string }

func (handler InlineWriteHandler) eventIDs(memoryID string, version uint64, toolEventID string) (memoryEventIDs, error) {
	scope := fmt.Sprintf("%s\x00%d\x00%s", memoryID, version, toolEventID)
	domains := []string{"memory-upserted-event", "memory-upserted-publish-outbox", "memory-upserted-publish-command"}
	values := make([]string, len(domains))
	for index, domain := range domains {
		value, err := ids.DeterministicUUID(handler.IDKey, domain, scope)
		if err != nil {
			return memoryEventIDs{}, err
		}
		values[index] = value
	}
	return memoryEventIDs{event: values[0], outbox: values[1], publish: values[2]}, nil
}

func validateWritableScope(ctx context.Context, tx pgx.Tx, tool executionpostgres.InlinePlatformToolContext, write MemoryWrite) error {
	switch write.ScopeKind {
	case "conversation":
		var conversationID string
		if err := tx.QueryRow(ctx, `SELECT conversation_id::text FROM agent.runs WHERE tenant_id=$1 AND id=$2 AND user_id=$3`, tool.TenantID, tool.RunID, tool.UserID).Scan(&conversationID); err != nil || conversationID != write.ScopeID {
			return ErrScope
		}
	case "user":
		if write.ScopeID != tool.UserID {
			return ErrScope
		}
	case "project":
		var projectID string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM product.projects WHERE tenant_id=$1 AND id=$2 AND user_id=$3 AND status IN ('active','blocked')`, tool.TenantID, write.ScopeID, tool.UserID).Scan(&projectID); err != nil {
			return ErrScope
		}
	case "team":
		if write.ScopeID != tool.TenantID {
			return ErrScope
		}
	default:
		return ErrScope
	}
	return nil
}

func validWrite(write MemoryWrite) bool {
	if write.MemoryID == "" || write.ScopeID == "" || write.ContentRef == "" || write.KeyRef == "" || write.EncryptionSubjectID == "" || write.EmbeddingModelID == "" || write.EmbeddingModelVersion == "" || write.VectorDimensions < 1 || write.PolicyVersion < 1 || write.IndexGeneration < 1 || write.GuardrailSnapshotID == "" || !validPointer(write.UpsertedEvent) || write.Confidence < 0 || write.Confidence > 1 || len(write.Sources) == 0 || len(write.DataSubjectIDs) == 0 {
		return false
	}
	if !member(write.ScopeKind, "conversation", "user", "project", "team") || !member(write.MemoryKind, "preference", "goal", "capability_context", "learning_history") || !member(write.ContentType, "text", "structured", "code_snippet") || !member(write.SourceKind, "agent_extracted", "user_stated", "system_derived") || !member(write.DerivationKind, "direct", "summarized", "merged", "inferred") {
		return false
	}
	if !uniqueStrings(write.SensitivityLabels) || !uniqueStrings(write.Tags) || !uniqueStrings(write.DataSubjectIDs) {
		return false
	}
	sourceKeys := map[string]bool{}
	for _, source := range write.Sources {
		if source.Ref == "" || !member(source.Kind, "event", "run", "payload", "memory", "user_statement", "system_rule") {
			return false
		}
		key := fmt.Sprintf("%s\x00%s\x00%d", source.Kind, source.Ref, source.Version)
		if sourceKeys[key] {
			return false
		}
		sourceKeys[key] = true
	}
	derivedKeys := map[string]bool{}
	for _, source := range write.DerivedFrom {
		if source.MemoryID == "" || source.Version < 1 || source.MemoryID == write.MemoryID && source.Version == write.ExpectedVersion+1 {
			return false
		}
		key := fmt.Sprintf("%s\x00%d", source.MemoryID, source.Version)
		if derivedKeys[key] {
			return false
		}
		derivedKeys[key] = true
	}
	return true
}

func validPointer(pointer executionpostgres.PayloadPointer) bool {
	return pointer.Ref != "" && pointer.Hash != ""
}

func member(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) bool {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}
