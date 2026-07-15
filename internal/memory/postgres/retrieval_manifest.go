package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	llmpostgres "github.com/langshift/lites/internal/llmgateway/postgres"
	"github.com/langshift/lites/internal/platform/ids"
)

var ErrRetrievalConflict = errors.New("retrieval manifest conflicts with durable state")

type RetrievalChunk struct {
	MemoryID        string
	MemoryVersion   uint64
	IndexGeneration uint64
	ChunkID         string
	ContentRef      string
	ContentHMAC     [32]byte
	Score           float64
	TrustLabel      string
}

type RetrievalManifest struct {
	QueryHMAC                               [32]byte
	RetrievalModelID, RetrievalModelVersion string
	PolicySnapshotID, GuardrailSnapshotID   string
	IndexGeneration, TokenBudget            uint64
	Chunks                                  []RetrievalChunk
	CommittedEvent                          EventPointer
}

type RetrievalManifestHandler struct {
	Appender eventpostgres.Appender
	IDKey    []byte
}

func (handler RetrievalManifestHandler) Validate(value any) bool {
	manifest, ok := value.(RetrievalManifest)
	return ok && len(handler.IDKey) >= 32 && validRetrievalManifest(manifest)
}

func (handler RetrievalManifestHandler) Commit(ctx context.Context, tx pgx.Tx, binding llmpostgres.RetrievalManifestContext, value any) error {
	manifest, ok := value.(RetrievalManifest)
	if !ok || tx == nil || len(handler.IDKey) < 32 || binding.ManifestID == "" || binding.AttemptID == "" || binding.TenantID == "" || binding.UserID == "" || binding.RunID == "" || binding.StoreEpoch == "" || binding.ContextManifestHash == "" || binding.StartedEventID == "" || binding.CorrelationID == "" || binding.Now.IsZero() || !validRetrievalManifest(manifest) {
		return ErrInvalidWrite
	}
	eventIDs, err := handler.retrievalEventIDs(binding.ManifestID)
	if err != nil {
		return ErrConfiguration
	}
	var existingID, existingAttempt, existingContext, existingModel, existingModelVersion, existingPolicy, existingGuardrail, existingEvent, existingPayloadRef, existingPayloadHash string
	var existingGeneration, existingBudget uint64
	var existingQuery []byte
	var existingCommittedAt time.Time
	err = tx.QueryRow(ctx, `SELECT m.id::text,m.llm_attempt_id::text,m.context_manifest_hash,m.query_hmac,m.retrieval_model_id,m.retrieval_model_version,m.policy_snapshot_id,m.guardrail_snapshot_id,m.index_generation,m.token_budget,m.committed_event_id::text,m.committed_at,e.payload_ref,e.payload_hash FROM agent.retrieval_manifests m JOIN agent.events e ON e.tenant_id=m.tenant_id AND e.id=m.committed_event_id WHERE m.tenant_id=$1 AND (m.id=$2 OR m.llm_attempt_id=$3)`, binding.TenantID, binding.ManifestID, binding.AttemptID).Scan(&existingID, &existingAttempt, &existingContext, &existingQuery, &existingModel, &existingModelVersion, &existingPolicy, &existingGuardrail, &existingGeneration, &existingBudget, &existingEvent, &existingCommittedAt, &existingPayloadRef, &existingPayloadHash)
	if err == nil {
		if existingID != binding.ManifestID || existingAttempt != binding.AttemptID || existingContext != binding.ContextManifestHash || !bytes.Equal(existingQuery, manifest.QueryHMAC[:]) || existingModel != manifest.RetrievalModelID || existingModelVersion != manifest.RetrievalModelVersion || existingPolicy != manifest.PolicySnapshotID || existingGuardrail != manifest.GuardrailSnapshotID || existingGeneration != manifest.IndexGeneration || existingBudget != manifest.TokenBudget || existingEvent != eventIDs.event || !existingCommittedAt.Equal(binding.Now) || existingPayloadRef != manifest.CommittedEvent.Ref || existingPayloadHash != manifest.CommittedEvent.Hash {
			return ErrRetrievalConflict
		}
		return handler.validateChunkReplay(ctx, tx, binding.TenantID, binding.ManifestID, manifest.Chunks)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.retrieval_manifests(id,tenant_id,user_id,llm_attempt_id,context_manifest_hash,query_hmac,retrieval_model_id,retrieval_model_version,policy_snapshot_id,guardrail_snapshot_id,index_generation,token_budget,committed_event_id,committed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, binding.ManifestID, binding.TenantID, binding.UserID, binding.AttemptID, binding.ContextManifestHash, manifest.QueryHMAC[:], manifest.RetrievalModelID, manifest.RetrievalModelVersion, manifest.PolicySnapshotID, manifest.GuardrailSnapshotID, manifest.IndexGeneration, manifest.TokenBudget, eventIDs.event, binding.Now); err != nil {
		return ErrRetrievalConflict
	}
	for index, chunk := range manifest.Chunks {
		var scopeKind, trustLabel string
		err = tx.QueryRow(ctx, `SELECT d.scope_kind,r.trust_label FROM agent.memory_documents d JOIN agent.memory_document_revisions r ON r.tenant_id=d.tenant_id AND r.memory_id=d.id AND r.memory_version=$3 JOIN agent.memory_index_projections p ON p.tenant_id=r.tenant_id AND p.memory_id=r.memory_id AND p.memory_version=r.memory_version AND p.index_generation=$4 WHERE d.tenant_id=$1 AND d.id=$2 AND d.status='active' AND d.current_revision=$3 AND p.status='indexed'`, binding.TenantID, chunk.MemoryID, chunk.MemoryVersion, chunk.IndexGeneration).Scan(&scopeKind, &trustLabel)
		if err != nil || trustLabel != chunk.TrustLabel {
			return ErrRetrievalConflict
		}
		if _, err = tx.Exec(ctx, `INSERT INTO agent.retrieval_manifest_chunks(tenant_id,manifest_id,ordinal,memory_id,memory_version,index_generation,chunk_id,content_ref,content_hmac,score,scope_kind,source_label,trust_label) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'retrieved_memory',$12)`, binding.TenantID, binding.ManifestID, index+1, chunk.MemoryID, chunk.MemoryVersion, chunk.IndexGeneration, chunk.ChunkID, chunk.ContentRef, chunk.ContentHMAC[:], chunk.Score, scopeKind, chunk.TrustLabel); err != nil {
			return ErrRetrievalConflict
		}
		if _, err = tx.Exec(ctx, `INSERT INTO agent.memory_accesses(tenant_id,manifest_id,memory_id,memory_version,chunk_id,accessed_at) VALUES($1,$2,$3,$4,$5,$6)`, binding.TenantID, binding.ManifestID, chunk.MemoryID, chunk.MemoryVersion, chunk.ChunkID, binding.Now); err != nil {
			return ErrRetrievalConflict
		}
	}
	causationID := binding.StartedEventID
	event := eventpostgres.Input{Event: eventpostgres.Event{ID: eventIDs.event, TenantID: binding.TenantID, UserID: binding.UserID, EventType: "RetrievalManifestCommitted", SchemaVersion: 1, AggregateKind: "retrieval_manifest", AggregateID: binding.ManifestID, AggregateVersion: 1, StoreEpoch: binding.StoreEpoch, OccurredAt: binding.Now, Actor: binding.Actor, CausationID: &causationID, CorrelationID: binding.CorrelationID, PayloadRef: manifest.CommittedEvent.Ref, PayloadHash: manifest.CommittedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: eventIDs.outbox, CommandID: eventIDs.publish, CommandType: "events.publish", PayloadRef: manifest.CommittedEvent.Ref, PayloadHash: manifest.CommittedEvent.Hash}}}
	if _, err = handler.Appender.Append(ctx, tx, event); err != nil {
		return err
	}
	return nil
}

func (handler RetrievalManifestHandler) validateChunkReplay(ctx context.Context, tx pgx.Tx, tenantID, manifestID string, expected []RetrievalChunk) error {
	rows, err := tx.Query(ctx, `SELECT memory_id::text,memory_version,index_generation,chunk_id,content_ref,content_hmac,score::float8,trust_label FROM agent.retrieval_manifest_chunks WHERE tenant_id=$1 AND manifest_id=$2 ORDER BY ordinal`, tenantID, manifestID)
	if err != nil {
		return err
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		if index >= len(expected) {
			return ErrRetrievalConflict
		}
		var memoryID, chunkID, contentRef, trustLabel string
		var memoryVersion, generation uint64
		var hmac []byte
		var score float64
		if err = rows.Scan(&memoryID, &memoryVersion, &generation, &chunkID, &contentRef, &hmac, &score, &trustLabel); err != nil {
			return err
		}
		chunk := expected[index]
		if memoryID != chunk.MemoryID || memoryVersion != chunk.MemoryVersion || generation != chunk.IndexGeneration || chunkID != chunk.ChunkID || contentRef != chunk.ContentRef || !bytes.Equal(hmac, chunk.ContentHMAC[:]) || math.Float64bits(score) != math.Float64bits(chunk.Score) || trustLabel != chunk.TrustLabel {
			return ErrRetrievalConflict
		}
		index++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if index != len(expected) {
		return ErrRetrievalConflict
	}
	return nil
}

type retrievalEventIDs struct{ event, outbox, publish string }

func (handler RetrievalManifestHandler) retrievalEventIDs(manifestID string) (retrievalEventIDs, error) {
	values := make([]string, 3)
	for index, part := range []string{"event", "outbox", "publish"} {
		value, err := ids.DeterministicUUID(handler.IDKey, "retrieval-manifest-"+part, manifestID)
		if err != nil {
			return retrievalEventIDs{}, err
		}
		values[index] = value
	}
	return retrievalEventIDs{event: values[0], outbox: values[1], publish: values[2]}, nil
}

func validRetrievalManifest(manifest RetrievalManifest) bool {
	if !nonzeroDigest(manifest.QueryHMAC) || manifest.RetrievalModelID == "" || manifest.RetrievalModelVersion == "" || manifest.PolicySnapshotID == "" || manifest.GuardrailSnapshotID == "" || manifest.IndexGeneration < 1 || manifest.CommittedEvent.Ref == "" || manifest.CommittedEvent.Hash == "" || len(manifest.Chunks) > 10000 {
		return false
	}
	seen := map[string]bool{}
	for _, chunk := range manifest.Chunks {
		if chunk.MemoryID == "" || chunk.MemoryVersion < 1 || chunk.IndexGeneration != manifest.IndexGeneration || chunk.ChunkID == "" || chunk.ContentRef == "" || !nonzeroDigest(chunk.ContentHMAC) || math.IsNaN(chunk.Score) || math.IsInf(chunk.Score, 0) || chunk.Score < 0 || chunk.Score > 1 || math.Round(chunk.Score*1e8)/1e8 != chunk.Score || !member(chunk.TrustLabel, "user_asserted", "derived", "untrusted_external") {
			return false
		}
		key := fmt.Sprintf("%s\x00%d\x00%s", chunk.MemoryID, chunk.MemoryVersion, chunk.ChunkID)
		if seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}
