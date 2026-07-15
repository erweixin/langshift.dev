package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/platform/ids"
)

var (
	ErrProjectionConflict = errors.New("memory index projection conflicts with durable state")
	ErrStaleEpoch         = errors.New("memory index projection store epoch is stale")
)

type EpochAuthority interface {
	CurrentStoreEpoch(context.Context) (string, error)
}

type ProjectionStore struct {
	Pool       *pgxpool.Pool
	Appender   eventpostgres.Appender
	IDKey      []byte
	StoreEpoch string
	Epochs     EpochAuthority
	Now        func() time.Time
}

type MarkIndexedCommand struct {
	TenantID, UserID, MemoryID     string
	MemoryVersion, IndexGeneration uint64
	EmbeddingRef, LexicalRef       string
	CorrelationID                  string
	Actor                          json.RawMessage
	IndexedEvent                   EventPointer
}

type EventPointer struct{ Ref, Hash string }

type IndexedProjection struct {
	ProjectionID, Status, EventID  string
	MemoryVersion, IndexGeneration uint64
	IndexedAt                      time.Time
	Replayed                       bool
}

func (store ProjectionStore) MarkIndexed(ctx context.Context, command MarkIndexedCommand) (IndexedProjection, error) {
	if store.Pool == nil || store.Epochs == nil || len(store.IDKey) < 32 || store.StoreEpoch == "" || !validMarkIndexed(command) {
		return IndexedProjection{}, ErrConfiguration
	}
	epoch, err := store.Epochs.CurrentStoreEpoch(ctx)
	if err != nil || epoch == "" {
		return IndexedProjection{}, ErrConfiguration
	}
	if epoch != store.StoreEpoch {
		return IndexedProjection{}, ErrStaleEpoch
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if store.Now != nil {
		now = store.Now().UTC().Truncate(time.Microsecond)
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return IndexedProjection{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return IndexedProjection{}, err
	}
	var projectionID, status, embeddingRef, lexicalRef, indexedEventID, revisionEventID, revisionUser string
	var projectionUpdatedAt time.Time
	err = tx.QueryRow(ctx, `SELECT p.id::text,p.status,COALESCE(p.embedding_ref,''),COALESCE(p.lexical_ref,''),COALESCE(p.indexed_event_id::text,''),r.upserted_event_id::text,r.user_id::text,p.updated_at FROM agent.memory_index_projections p JOIN agent.memory_document_revisions r ON r.tenant_id=p.tenant_id AND r.memory_id=p.memory_id AND r.memory_version=p.memory_version WHERE p.tenant_id=$1 AND p.memory_id=$2 AND p.memory_version=$3 AND p.index_generation=$4 FOR UPDATE OF p`, command.TenantID, command.MemoryID, command.MemoryVersion, command.IndexGeneration).Scan(&projectionID, &status, &embeddingRef, &lexicalRef, &indexedEventID, &revisionEventID, &revisionUser, &projectionUpdatedAt)
	if err != nil || revisionUser != command.UserID {
		return IndexedProjection{}, ErrProjectionConflict
	}
	eventIDs, err := store.indexedEventIDs(projectionID)
	if err != nil {
		return IndexedProjection{}, ErrConfiguration
	}
	if status == "indexed" {
		if embeddingRef != command.EmbeddingRef || lexicalRef != command.LexicalRef || indexedEventID != eventIDs.event {
			return IndexedProjection{}, ErrProjectionConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return IndexedProjection{}, err
		}
		return IndexedProjection{ProjectionID: projectionID, Status: status, EventID: indexedEventID, MemoryVersion: command.MemoryVersion, IndexGeneration: command.IndexGeneration, IndexedAt: projectionUpdatedAt, Replayed: true}, nil
	}
	if status != "pending" {
		return IndexedProjection{}, ErrProjectionConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.memory_index_projections SET status='indexed',embedding_ref=$1,lexical_ref=$2,indexed_event_id=$3,updated_at=$4 WHERE tenant_id=$5 AND id=$6 AND status='pending' AND memory_id=$7 AND memory_version=$8 AND index_generation=$9`, command.EmbeddingRef, command.LexicalRef, eventIDs.event, now, command.TenantID, projectionID, command.MemoryID, command.MemoryVersion, command.IndexGeneration)
	if err != nil || tag.RowsAffected() != 1 {
		return IndexedProjection{}, ErrProjectionConflict
	}
	causationID := revisionEventID
	event := eventpostgres.Input{Event: eventpostgres.Event{ID: eventIDs.event, TenantID: command.TenantID, UserID: command.UserID, EventType: "MemoryIndexProjectionIndexed", SchemaVersion: 1, AggregateKind: "memory_index_projection", AggregateID: projectionID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.IndexedEvent.Ref, PayloadHash: command.IndexedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: eventIDs.outbox, CommandID: eventIDs.publish, CommandType: "events.publish", PayloadRef: command.IndexedEvent.Ref, PayloadHash: command.IndexedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, event); err != nil {
		return IndexedProjection{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return IndexedProjection{}, err
	}
	return IndexedProjection{ProjectionID: projectionID, Status: "indexed", EventID: eventIDs.event, MemoryVersion: command.MemoryVersion, IndexGeneration: command.IndexGeneration, IndexedAt: now}, nil
}

type projectionEventIDs struct{ event, outbox, publish string }

func (store ProjectionStore) indexedEventIDs(projectionID string) (projectionEventIDs, error) {
	values := make([]string, 3)
	for index, part := range []string{"event", "outbox", "publish"} {
		value, err := ids.DeterministicUUID(store.IDKey, "memory-indexed-"+part, projectionID+"\x00"+strconv.FormatUint(1, 10))
		if err != nil {
			return projectionEventIDs{}, err
		}
		values[index] = value
	}
	return projectionEventIDs{event: values[0], outbox: values[1], publish: values[2]}, nil
}

func validMarkIndexed(command MarkIndexedCommand) bool {
	return command.TenantID != "" && command.UserID != "" && command.MemoryID != "" && command.MemoryVersion > 0 && command.IndexGeneration > 0 && command.EmbeddingRef != "" && command.LexicalRef != "" && command.CorrelationID != "" && validJSONActor(command.Actor) && command.IndexedEvent.Ref != "" && command.IndexedEvent.Hash != ""
}

func validJSONActor(actor json.RawMessage) bool {
	var value map[string]any
	return json.Unmarshal(actor, &value) == nil && value != nil
}
