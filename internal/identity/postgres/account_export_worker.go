package postgres

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

const (
	AccountExportPrepareCommand = "account.export.prepare"
	defaultAccountExportTTL     = 7 * 24 * time.Hour
	maxAccountExportBytes       = 24 << 20
)

var (
	ErrAccountExportInvalid  = errors.New("account export command is invalid")
	ErrAccountExportTooLarge = errors.New("account export exceeds the bounded archive size")
)

type AccountExportWork struct {
	RequestID string   `json:"request_id"`
	UserID    string   `json:"user_id"`
	Scope     []string `json:"scope"`
	Format    string   `json:"format"`
}

type AccountExportStore struct {
	Pool       *pgxpool.Pool
	Payloads   payload.Store
	Appender   eventpostgres.Appender
	Inbox      eventpostgres.InboxStore
	StoreEpoch string
	IDKey      []byte
	TTL        time.Duration
	Now        func() time.Time
}

type AccountExportProcessResult struct {
	Completed bool
	Replayed  bool
	Version   uint64
	Status    string
}

const (
	accountExportFailureInvalidCommand = "invalid_command"
	accountExportFailureTooLarge       = "archive_too_large"
	accountExportFailureLegacy         = "legacy_failure"
)

// Process builds the archive outside the final write transaction, then
// commits its immutable pointer, completion event, and Inbox receipt together.
// A retry may leave an unreferenced immutable candidate object, but can never
// replace a ready export or append a second terminal event.
func (store AccountExportStore) Process(ctx context.Context, command eventpostgres.DeliveredCommand, claim eventpostgres.InboxClaim, work AccountExportWork) (AccountExportProcessResult, error) {
	if store.Pool == nil || store.Payloads == nil || store.StoreEpoch == "" || store.StoreEpoch != command.StoreEpoch || len(store.IDKey) < 32 || command.CommandType != AccountExportPrepareCommand || command.AggregateKind != "data_export_request" || command.AggregateID != work.RequestID || !validAccountExportWork(work) {
		return AccountExportProcessResult{}, ErrAccountExportInvalid
	}
	createdAt, storedVersion, ready, err := store.validateRequest(ctx, command.TenantID, work)
	if err != nil {
		return AccountExportProcessResult{}, err
	}
	if ready {
		if err = store.Inbox.Complete(ctx, claim); err != nil {
			return AccountExportProcessResult{}, err
		}
		return AccountExportProcessResult{Completed: true, Replayed: true, Version: storedVersion, Status: "ready"}, nil
	}
	document, err := store.buildDocument(ctx, command.TenantID, work, createdAt)
	if err != nil {
		return AccountExportProcessResult{}, err
	}
	body, err := encodeAccountExport(document, work.Format)
	if err != nil {
		return AccountExportProcessResult{}, err
	}
	manifest, err := store.Payloads.Put(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: work.RequestID, Class: "account-export", ContentType: accountExportContentType(work.Format)}, body)
	if err != nil {
		return AccountExportProcessResult{}, err
	}
	now := store.now()
	expiresAt := now.Add(store.ttl())
	identifiers, err := store.identifiers(work.RequestID)
	if err != nil {
		return AccountExportProcessResult{}, err
	}
	eventPayload, err := store.Payloads.Put(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: identifiers.event, Class: "event-payload", ContentType: "application/json"}, mustJSON(map[string]any{"subject_id": work.RequestID, "subject_version": storedVersion + 1, "status": "ready", "expires_at": expiresAt, "archive_manifest_hash": manifest.Hash}))
	if err != nil {
		return AccountExportProcessResult{}, err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return AccountExportProcessResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return AccountExportProcessResult{}, err
	}
	if err = store.Inbox.LockClaimTx(ctx, tx, claim, now); err != nil {
		return AccountExportProcessResult{}, err
	}
	var version uint64
	var status string
	err = tx.QueryRow(ctx, `SELECT version,status FROM product.data_export_requests WHERE tenant_id=$1 AND user_id=$2 AND id=$3 FOR UPDATE`, command.TenantID, work.UserID, work.RequestID).Scan(&version, &status)
	if err != nil {
		return AccountExportProcessResult{}, err
	}
	if status == "ready" {
		if err = store.Inbox.CompleteTx(ctx, tx, claim, now); err != nil {
			return AccountExportProcessResult{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return AccountExportProcessResult{}, err
		}
		return AccountExportProcessResult{Completed: true, Replayed: true, Version: version, Status: status}, nil
	}
	if status != "requested" || version != storedVersion {
		return AccountExportProcessResult{}, ErrAccountExportInvalid
	}
	nextVersion := version + 1
	tag, err := tx.Exec(ctx, `UPDATE product.data_export_requests SET version=$1,status='ready',object_ref=$2,content_hash=$3,expires_at=$4,completed_at=$5,updated_at=$5 WHERE tenant_id=$6 AND user_id=$7 AND id=$8 AND version=$9 AND status='requested'`, nextVersion, manifest.Ref, manifest.Hash, expiresAt, now, command.TenantID, work.UserID, work.RequestID, version)
	if err != nil || tag.RowsAffected() != 1 {
		return AccountExportProcessResult{}, eventpostgres.ErrDeliveryConflict
	}
	actor := json.RawMessage(`{"kind":"service","name":"account-erasure-worker"}`)
	_, err = store.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: identifiers.event, TenantID: command.TenantID, UserID: work.UserID, EventType: "AccountExportReady", SchemaVersion: 1, AggregateKind: "data_export_request", AggregateID: work.RequestID, AggregateVersion: nextVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: actor, CausationID: &command.CommandID, CorrelationID: work.RequestID, PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: identifiers.outbox, CommandID: identifiers.publish, CommandType: "events.publish", PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}}})
	if err != nil {
		return AccountExportProcessResult{}, err
	}
	if err = store.Inbox.CompleteTx(ctx, tx, claim, now); err != nil {
		return AccountExportProcessResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return AccountExportProcessResult{}, err
	}
	return AccountExportProcessResult{Completed: true, Version: nextVersion, Status: "ready"}, nil
}

// Fail moves a deterministic, non-retryable export error to a durable failed
// terminal state. The request update, failure event and Inbox completion share
// one transaction so a crash cannot leave a completed command behind a
// permanently requested export.
func (store AccountExportStore) Fail(ctx context.Context, command eventpostgres.DeliveredCommand, claim eventpostgres.InboxClaim, reasonCode string) (AccountExportProcessResult, error) {
	if store.Pool == nil || store.Payloads == nil || store.StoreEpoch == "" || store.StoreEpoch != command.StoreEpoch || len(store.IDKey) < 32 || command.CommandType != AccountExportPrepareCommand || command.AggregateKind != "data_export_request" || command.AggregateID == "" || reasonCode != accountExportFailureInvalidCommand && reasonCode != accountExportFailureTooLarge {
		return AccountExportProcessResult{}, ErrAccountExportInvalid
	}
	request, err := store.failureRequest(ctx, command.TenantID, command.AggregateID)
	if err != nil {
		return AccountExportProcessResult{}, err
	}
	if request.status == "ready" || request.status == "failed" {
		if err = store.Inbox.Complete(ctx, claim); err != nil {
			return AccountExportProcessResult{}, err
		}
		return AccountExportProcessResult{Completed: true, Replayed: true, Version: request.version, Status: request.status}, nil
	}
	identifiers, err := store.failureIdentifiers(command.AggregateID)
	if err != nil {
		return AccountExportProcessResult{}, err
	}
	now := store.now()
	nextVersion := request.version + 1
	eventPayload, err := store.Payloads.Put(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: identifiers.event, Class: "event-payload", ContentType: "application/json"}, mustJSON(map[string]any{"subject_id": command.AggregateID, "subject_version": nextVersion, "previous_state": "requested", "new_state": "failed", "status": "failed", "reason_code": reasonCode}))
	if err != nil {
		return AccountExportProcessResult{}, err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return AccountExportProcessResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return AccountExportProcessResult{}, err
	}
	if err = store.Inbox.LockClaimTx(ctx, tx, claim, now); err != nil {
		return AccountExportProcessResult{}, err
	}
	var version uint64
	var status, userID string
	err = tx.QueryRow(ctx, `SELECT version,status,user_id::text FROM product.data_export_requests WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, command.AggregateID).Scan(&version, &status, &userID)
	if err != nil {
		return AccountExportProcessResult{}, err
	}
	if status == "ready" || status == "failed" {
		if err = store.Inbox.CompleteTx(ctx, tx, claim, now); err != nil {
			return AccountExportProcessResult{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return AccountExportProcessResult{}, err
		}
		return AccountExportProcessResult{Completed: true, Replayed: true, Version: version, Status: status}, nil
	}
	if status != "requested" || version != request.version || userID != request.userID {
		return AccountExportProcessResult{}, eventpostgres.ErrDeliveryConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE product.data_export_requests SET version=$1,status='failed',failure_code=$2,completed_at=$3,updated_at=$3 WHERE tenant_id=$4 AND user_id=$5 AND id=$6 AND version=$7 AND status='requested'`, nextVersion, reasonCode, now, command.TenantID, userID, command.AggregateID, version)
	if err != nil || tag.RowsAffected() != 1 {
		return AccountExportProcessResult{}, eventpostgres.ErrDeliveryConflict
	}
	actor := json.RawMessage(`{"kind":"service","name":"account-erasure-worker"}`)
	_, err = store.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: identifiers.event, TenantID: command.TenantID, UserID: userID, EventType: "AccountExportFailed", SchemaVersion: 1, AggregateKind: "data_export_request", AggregateID: command.AggregateID, AggregateVersion: nextVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: actor, CausationID: &command.CommandID, CorrelationID: command.AggregateID, PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: identifiers.outbox, CommandID: identifiers.publish, CommandType: "events.publish", PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}}})
	if err != nil {
		return AccountExportProcessResult{}, err
	}
	if err = store.Inbox.CompleteTx(ctx, tx, claim, now); err != nil {
		return AccountExportProcessResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return AccountExportProcessResult{}, err
	}
	return AccountExportProcessResult{Completed: true, Version: nextVersion, Status: "failed"}, nil
}

type accountExportFailureRequest struct {
	userID  string
	version uint64
	status  string
}

func (store AccountExportStore) failureRequest(ctx context.Context, tenantID, requestID string) (accountExportFailureRequest, error) {
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return accountExportFailureRequest{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return accountExportFailureRequest{}, err
	}
	var request accountExportFailureRequest
	if err = tx.QueryRow(ctx, `SELECT user_id::text,version,status FROM product.data_export_requests WHERE tenant_id=$1 AND id=$2`, tenantID, requestID).Scan(&request.userID, &request.version, &request.status); err != nil {
		return accountExportFailureRequest{}, err
	}
	if request.userID == "" || request.version == 0 || request.status != "requested" && request.status != "ready" && request.status != "failed" {
		return accountExportFailureRequest{}, ErrAccountExportInvalid
	}
	if err = tx.Commit(ctx); err != nil {
		return accountExportFailureRequest{}, err
	}
	return request, nil
}

func (store AccountExportStore) validateRequest(ctx context.Context, tenantID string, work AccountExportWork) (time.Time, uint64, bool, error) {
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return time.Time{}, 0, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return time.Time{}, 0, false, err
	}
	var createdAt time.Time
	var version uint64
	var status string
	var scope []byte
	err = tx.QueryRow(ctx, `SELECT created_at,version,status,scope FROM product.data_export_requests WHERE tenant_id=$1 AND user_id=$2 AND id=$3`, tenantID, work.UserID, work.RequestID).Scan(&createdAt, &version, &status, &scope)
	if err != nil {
		return time.Time{}, 0, false, err
	}
	var stored struct {
		Categories []string `json:"categories"`
		Format     string   `json:"format"`
	}
	if json.Unmarshal(scope, &stored) != nil || !equalStrings(stored.Categories, work.Scope) || stored.Format != work.Format || status != "requested" && status != "ready" {
		return time.Time{}, 0, false, ErrAccountExportInvalid
	}
	if err = tx.Commit(ctx); err != nil {
		return time.Time{}, 0, false, err
	}
	return createdAt.UTC(), version, status == "ready", nil
}

type accountExportDocument struct {
	SchemaVersion int            `json:"schema_version"`
	ExportID      string         `json:"export_id"`
	TenantID      string         `json:"tenant_id"`
	UserID        string         `json:"user_id"`
	GeneratedAt   time.Time      `json:"generated_at"`
	Scope         []string       `json:"scope"`
	Data          map[string]any `json:"data"`
	Notices       []string       `json:"notices,omitempty"`
}

func (store AccountExportStore) buildDocument(ctx context.Context, tenantID string, work AccountExportWork, generatedAt time.Time) (accountExportDocument, error) {
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return accountExportDocument{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return accountExportDocument{}, err
	}
	document := accountExportDocument{SchemaVersion: 1, ExportID: work.RequestID, TenantID: tenantID, UserID: work.UserID, GeneratedAt: generatedAt, Scope: append([]string(nil), work.Scope...), Data: map[string]any{}}
	for _, scope := range work.Scope {
		switch scope {
		case "account":
			document.Data[scope], err = exportAccount(ctx, tx, tenantID, work.UserID)
		case "missions":
			document.Data[scope], err = store.exportMissions(ctx, tx, tenantID, work.UserID)
		case "evidence":
			document.Data[scope], err = store.exportEvidence(ctx, tx, tenantID, work.UserID)
		case "projects":
			document.Data[scope], err = store.exportProjects(ctx, tx, tenantID, work.UserID)
			document.Notices = append(document.Notices, "Project artifact binaries are represented by immutable metadata; use portfolio exports for packaged binary artifacts.")
		case "conversations":
			document.Data[scope], err = store.exportConversations(ctx, tx, tenantID, work.UserID)
		case "memory":
			document.Data[scope], err = exportMemoryMetadata(ctx, tx, tenantID, work.UserID)
			document.Notices = append(document.Notices, "Memory content remains protected by its subject-key boundary; this archive contains governed revision metadata and provenance, not internal key or object references.")
		case "audit":
			document.Data[scope], err = exportAudit(ctx, tx, tenantID, work.UserID)
		default:
			err = ErrAccountExportInvalid
		}
		if err != nil {
			return accountExportDocument{}, fmt.Errorf("export %s: %w", scope, err)
		}
	}
	sort.Strings(document.Notices)
	if err = tx.Commit(ctx); err != nil {
		return accountExportDocument{}, err
	}
	return document, nil
}

func exportAccount(ctx context.Context, tx pgx.Tx, tenantID, userID string) (any, error) {
	var profile []byte
	err := tx.QueryRow(ctx, `SELECT to_jsonb(x) FROM (SELECT id::text,version,created_at,updated_at,normalized_email,email_verified_at,locale,timezone,display_name,status FROM identity.users WHERE id=$1) x`, userID).Scan(&profile)
	if err != nil {
		return nil, err
	}
	memberships, err := queryJSONRows(ctx, tx, `SELECT to_jsonb(x) FROM (SELECT m.id::text,m.version,m.role,m.status,m.joined_at,m.deactivated_at,t.id::text AS tenant_id,t.name AS tenant_name FROM identity.memberships m JOIN identity.tenants t ON t.id=m.tenant_id WHERE m.tenant_id=$1 AND m.user_id=$2 ORDER BY m.created_at,m.id) x`, tenantID, userID)
	if err != nil {
		return nil, err
	}
	var decoded any
	if json.Unmarshal(profile, &decoded) != nil {
		return nil, ErrAccountExportInvalid
	}
	return map[string]any{"profile": decoded, "memberships": memberships}, nil
}

func (store AccountExportStore) exportMissions(ctx context.Context, tx pgx.Tx, tenantID, userID string) (any, error) {
	missions, err := store.queryPayloadRows(ctx, tx, `SELECT to_jsonb(m)-'tenant_id'-'user_id'-'goal_payload_ref'-'goal_payload_hash',m.id::text,COALESCE(m.goal_payload_ref,''),COALESCE(m.goal_payload_hash,'') FROM product.missions m WHERE m.tenant_id=$1 AND m.user_id=$2 ORDER BY m.created_at,m.id`, tenantID, userID, "mission-goal", "application/json", "goal")
	if err != nil {
		return nil, err
	}
	routes, err := store.queryPayloadRows(ctx, tx, `SELECT to_jsonb(r)-'tenant_id'-'user_id'-'route_payload_ref'-'route_payload_hash',r.id::text,COALESCE(r.route_payload_ref,''),COALESCE(r.route_payload_hash,'') FROM product.route_revisions r WHERE r.tenant_id=$1 AND r.user_id=$2 ORDER BY r.created_at,r.id`, tenantID, userID, "route-revision", "application/json", "route")
	if err != nil {
		return nil, err
	}
	tasks, err := store.queryPayloadRows(ctx, tx, `SELECT to_jsonb(d)-'tenant_id'-'user_id'-'task_payload_ref'-'task_payload_hash',d.id::text,d.task_payload_ref,d.task_payload_hash FROM product.daily_tasks d WHERE d.tenant_id=$1 AND d.user_id=$2 ORDER BY d.scheduled_for,d.id`, tenantID, userID, "daily-task", "application/json", "task")
	if err != nil {
		return nil, err
	}
	submissions, err := store.exportSubmissions(ctx, tx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	reviews, err := store.queryPayloadRows(ctx, tx, `SELECT to_jsonb(r)-'tenant_id'-'user_id'-'review_payload_ref'-'review_payload_hash',r.id::text,r.review_payload_ref,r.review_payload_hash FROM product.reviews r WHERE r.tenant_id=$1 AND r.user_id=$2 ORDER BY r.created_at,r.id`, tenantID, userID, "product-review", "application/json", "review")
	if err != nil {
		return nil, err
	}
	claims, err := store.exportCapabilityClaims(ctx, tx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	focuses, err := queryJSONRows(ctx, tx, `SELECT to_jsonb(f)-'tenant_id'-'user_id' FROM product.mission_focuses f WHERE f.tenant_id=$1 AND f.user_id=$2 ORDER BY f.updated_at`, tenantID, userID)
	if err != nil {
		return nil, err
	}
	links, err := queryJSONRows(ctx, tx, `SELECT to_jsonb(l)-'tenant_id' FROM product.claim_evidence_links l JOIN product.capability_claims c ON c.tenant_id=l.tenant_id AND c.id=l.claim_id WHERE c.tenant_id=$1 AND c.user_id=$2 ORDER BY l.recorded_at,l.id`, tenantID, userID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"missions": missions, "mission_focuses": focuses, "route_revisions": routes, "capability_claim_revisions": claims, "claim_evidence_links": links, "daily_tasks": tasks, "submissions": submissions, "reviews": reviews}, nil
}

func (store AccountExportStore) exportCapabilityClaims(ctx context.Context, tx pgx.Tx, tenantID, userID string) ([]map[string]any, error) {
	rows, err := tx.Query(ctx, `SELECT to_jsonb(c)-'tenant_id'-'user_id'-'statement_ref'-'reason_ref',c.statement_ref,COALESCE(c.reason_ref,'') FROM product.capability_claims c WHERE c.tenant_id=$1 AND c.user_id=$2 ORDER BY c.recorded_at,c.id`, tenantID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var encoded []byte
		var statementReference, reasonReference string
		if err = rows.Scan(&encoded, &statementReference, &reasonReference); err != nil {
			return nil, err
		}
		item, err := decodeObject(encoded)
		if err != nil {
			return nil, err
		}
		item["statement"], err = store.readExportClaimText(ctx, tenantID, "capability-claim-statement", statementReference)
		if err != nil {
			return nil, err
		}
		if reasonReference != "" {
			item["reason"], err = store.readExportClaimText(ctx, tenantID, "capability-claim-reason", reasonReference)
			if err != nil {
				return nil, err
			}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (store AccountExportStore) readExportClaimText(ctx context.Context, tenantID, class, reference string) (string, error) {
	var stored struct {
		ObjectID string           `json:"object_id"`
		Manifest payload.Manifest `json:"manifest"`
	}
	decoder := json.NewDecoder(strings.NewReader(reference))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&stored) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || stored.ObjectID == "" || stored.Manifest.Ref == "" || stored.Manifest.Hash == "" {
		return "", ErrAccountExportInvalid
	}
	body, err := store.Payloads.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: stored.ObjectID, Class: class, ContentType: "application/json"}, stored.Manifest)
	if err != nil {
		return "", err
	}
	var document struct {
		SchemaVersion int    `json:"schema_version"`
		Text          string `json:"text"`
	}
	decoder = json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&document) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || document.SchemaVersion != 1 || document.Text == "" {
		return "", ErrAccountExportInvalid
	}
	return document.Text, nil
}

func (store AccountExportStore) exportSubmissions(ctx context.Context, tx pgx.Tx, tenantID, userID string) ([]map[string]any, error) {
	rows, err := tx.Query(ctx, `SELECT to_jsonb(s)-'tenant_id'-'user_id'-'payload_ref'-'payload_manifest_hash'-'understanding_ref'-'understanding_manifest_hash',s.id::text,s.payload_ref,s.payload_manifest_hash,s.understanding_ref,s.understanding_manifest_hash FROM product.submissions s WHERE s.tenant_id=$1 AND s.user_id=$2 ORDER BY s.created_at,s.id`, tenantID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var encoded []byte
		var id, contentRef, contentHash, understandingRef, understandingHash string
		if err = rows.Scan(&encoded, &id, &contentRef, &contentHash, &understandingRef, &understandingHash); err != nil {
			return nil, err
		}
		item, err := decodeObject(encoded)
		if err != nil {
			return nil, err
		}
		content, err := store.Payloads.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: id, Class: "product-submission-content", ContentType: "text/plain; charset=utf-8"}, payload.Manifest{Ref: contentRef, Hash: contentHash})
		if err != nil {
			return nil, err
		}
		understanding, err := store.Payloads.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: id, Class: "product-submission-understanding", ContentType: "text/plain; charset=utf-8"}, payload.Manifest{Ref: understandingRef, Hash: understandingHash})
		if err != nil {
			return nil, err
		}
		item["content"] = string(content)
		item["understanding"] = string(understanding)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (store AccountExportStore) exportEvidence(ctx context.Context, tx pgx.Tx, tenantID, userID string) (any, error) {
	return store.queryPayloadRows(ctx, tx, `SELECT to_jsonb(e)-'tenant_id'-'user_id'-'payload_ref'-'content_hash',e.id::text,e.payload_ref,e.content_hash FROM product.evidence e WHERE e.tenant_id=$1 AND e.user_id=$2 ORDER BY e.recorded_at,e.id`, tenantID, userID, "product-evidence", "application/json", "evidence")
}

func (store AccountExportStore) exportProjects(ctx context.Context, tx pgx.Tx, tenantID, userID string) (any, error) {
	rows, err := tx.Query(ctx, `SELECT to_jsonb(p)-'tenant_id'-'user_id'-'brief_ref'-'brief_manifest_hash'-'reflection_ref'-'reflection_manifest_hash',p.id::text,p.brief_ref,p.brief_manifest_hash,COALESCE(p.reflection_ref,''),COALESCE(p.reflection_manifest_hash,'') FROM product.projects p WHERE p.tenant_id=$1 AND p.user_id=$2 ORDER BY p.created_at,p.id`, tenantID, userID)
	if err != nil {
		return nil, err
	}
	projects := []map[string]any{}
	for rows.Next() {
		var encoded []byte
		var id, briefRef, briefHash, reflectionRef, reflectionHash string
		if err = rows.Scan(&encoded, &id, &briefRef, &briefHash, &reflectionRef, &reflectionHash); err != nil {
			rows.Close()
			return nil, err
		}
		item, err := decodeObject(encoded)
		if err != nil {
			rows.Close()
			return nil, err
		}
		brief, err := store.Payloads.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: id, Class: "project-brief", ContentType: "application/json"}, payload.Manifest{Ref: briefRef, Hash: briefHash})
		if err != nil {
			rows.Close()
			return nil, err
		}
		item["brief"], err = decodeAny(brief)
		if err != nil {
			rows.Close()
			return nil, err
		}
		if reflectionRef != "" {
			reflection, getErr := store.Payloads.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: id, Class: "project-reflection", ContentType: "application/json"}, payload.Manifest{Ref: reflectionRef, Hash: reflectionHash})
			if getErr != nil {
				rows.Close()
				return nil, getErr
			}
			item["reflection"], err = decodeAny(reflection)
			if err != nil {
				rows.Close()
				return nil, err
			}
		}
		projects = append(projects, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	artifacts, err := queryJSONRows(ctx, tx, `SELECT to_jsonb(x) FROM (SELECT a.id::text,a.project_id::text,a.artifact_kind,a.title,a.status,a.version,a.created_at,a.updated_at,COALESCE((SELECT jsonb_agg(to_jsonb(r)-'tenant_id'-'user_id'-'object_ref' ORDER BY r.revision,r.id) FROM product.artifact_revisions r WHERE r.tenant_id=a.tenant_id AND r.user_id=a.user_id AND r.artifact_id=a.id),'[]'::jsonb) AS revisions FROM product.artifacts a WHERE a.tenant_id=$1 AND a.user_id=$2 ORDER BY a.created_at,a.id) x`, tenantID, userID)
	if err != nil {
		return nil, err
	}
	portfolios, err := queryJSONRows(ctx, tx, `SELECT to_jsonb(p)-'tenant_id'-'user_id'-'object_ref' FROM product.portfolio_exports p WHERE p.tenant_id=$1 AND p.user_id=$2 ORDER BY p.created_at,p.id`, tenantID, userID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"projects": projects, "artifacts": artifacts, "portfolio_exports": portfolios}, nil
}

func (store AccountExportStore) exportConversations(ctx context.Context, tx pgx.Tx, tenantID, userID string) (any, error) {
	conversations, err := queryJSONRows(ctx, tx, `SELECT to_jsonb(c)-'tenant_id'-'user_id' FROM agent.conversations c WHERE c.tenant_id=$1 AND c.user_id=$2 ORDER BY c.created_at,c.id`, tenantID, userID)
	if err != nil {
		return nil, err
	}
	messages, err := store.queryPayloadRows(ctx, tx, `SELECT to_jsonb(m)-'tenant_id'-'user_id'-'payload_ref'-'payload_hash',m.id::text,m.payload_ref,m.payload_hash FROM agent.run_messages m WHERE m.tenant_id=$1 AND m.user_id=$2 ORDER BY m.created_at,m.run_id,m.message_index,m.id`, tenantID, userID, "run-message", "application/json", "message")
	if err != nil {
		return nil, err
	}
	return map[string]any{"conversations": conversations, "messages": messages}, nil
}

func exportMemoryMetadata(ctx context.Context, tx pgx.Tx, tenantID, userID string) (any, error) {
	documents, err := queryJSONRows(ctx, tx, `SELECT to_jsonb(d)-'tenant_id'-'user_id'-'upserted_event_id'-'deleted_event_id' FROM agent.memory_documents d WHERE d.tenant_id=$1 AND d.user_id=$2 ORDER BY d.created_at,d.id`, tenantID, userID)
	if err != nil {
		return nil, err
	}
	revisions, err := queryJSONRows(ctx, tx, `SELECT to_jsonb(r)-'tenant_id'-'user_id'-'content_ref'-'content_hmac'-'summary_ref'-'key_ref'-'upserted_event_id' FROM agent.memory_document_revisions r WHERE r.tenant_id=$1 AND r.user_id=$2 ORDER BY r.created_at,r.memory_id,r.memory_version`, tenantID, userID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"documents": documents, "revisions": revisions, "content_included": false}, nil
}

func exportAudit(ctx context.Context, tx pgx.Tx, tenantID, userID string) (any, error) {
	return queryJSONRows(ctx, tx, `SELECT to_jsonb(x) FROM (SELECT id::text,event_type,request_id,occurred_at,CASE WHEN details ? 'changed_fields' THEN jsonb_build_object('changed_fields',details->'changed_fields') ELSE '{}'::jsonb END AS details FROM identity.security_events WHERE tenant_id=$1 AND (subject_user_id=$2 OR actor_user_id=$2) ORDER BY occurred_at,id) x`, tenantID, userID)
}

func (store AccountExportStore) queryPayloadRows(ctx context.Context, tx pgx.Tx, query, tenantID, userID, class, contentType, field string) ([]map[string]any, error) {
	rows, err := tx.Query(ctx, query, tenantID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var encoded []byte
		var objectID, ref, hash string
		if err = rows.Scan(&encoded, &objectID, &ref, &hash); err != nil {
			return nil, err
		}
		item, err := decodeObject(encoded)
		if err != nil {
			return nil, err
		}
		if ref != "" {
			body, getErr := store.Payloads.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: objectID, Class: class, ContentType: contentType}, payload.Manifest{Ref: ref, Hash: hash})
			if getErr != nil {
				return nil, getErr
			}
			if strings.HasPrefix(contentType, "application/json") {
				item[field], err = decodeAny(body)
				if err != nil {
					return nil, err
				}
			} else {
				item[field] = string(body)
			}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func queryJSONRows(ctx context.Context, tx pgx.Tx, query string, arguments ...any) ([]any, error) {
	rows, err := tx.Query(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []any{}
	for rows.Next() {
		var encoded []byte
		if err = rows.Scan(&encoded); err != nil {
			return nil, err
		}
		item, err := decodeAny(encoded)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func encodeAccountExport(document accountExportDocument, format string) ([]byte, error) {
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	encoded = append(encoded, '\n')
	// Bound the plaintext before compression as well as the resulting archive.
	// Checking only the compressed ZIP size would allow highly compressible
	// exports to consume unbounded worker memory and client disk after extraction.
	if len(encoded) > maxAccountExportBytes {
		return nil, ErrAccountExportTooLarge
	}
	if format == "json" {
		return encoded, nil
	}
	if format != "zip" {
		return nil, ErrAccountExportInvalid
	}
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	header := &zip.FileHeader{Name: "lites-account-export.json", Method: zip.Deflate}
	header.SetModTime(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC))
	entry, err := archive.CreateHeader(header)
	if err == nil {
		_, err = entry.Write(encoded)
	}
	if closeErr := archive.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	if buffer.Len() > maxAccountExportBytes {
		return nil, ErrAccountExportTooLarge
	}
	return buffer.Bytes(), nil
}

func DecodeAccountExportWork(body []byte) (AccountExportWork, error) {
	var work AccountExportWork
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&work); err != nil {
		return AccountExportWork{}, ErrAccountExportInvalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || !validAccountExportWork(work) {
		return AccountExportWork{}, ErrAccountExportInvalid
	}
	sort.Strings(work.Scope)
	return work, nil
}

func validAccountExportWork(work AccountExportWork) bool {
	if work.RequestID == "" || work.UserID == "" || work.Format != "json" && work.Format != "zip" || len(work.Scope) == 0 || len(work.Scope) > 7 {
		return false
	}
	if _, err := uuid.Parse(work.RequestID); err != nil {
		return false
	}
	if _, err := uuid.Parse(work.UserID); err != nil {
		return false
	}
	allowed := map[string]bool{"account": true, "missions": true, "evidence": true, "projects": true, "conversations": true, "memory": true, "audit": true}
	seen := map[string]bool{}
	for _, scope := range work.Scope {
		if !allowed[scope] || seen[scope] {
			return false
		}
		seen[scope] = true
	}
	return true
}

type accountExportIDs struct{ event, outbox, publish string }

func (store AccountExportStore) identifiers(requestID string) (accountExportIDs, error) {
	values := make([]string, 3)
	for index, domain := range []string{"account-export-ready-event", "account-export-ready-outbox", "account-export-ready-publish"} {
		value, err := ids.DeterministicUUID(store.IDKey, domain, requestID)
		if err != nil {
			return accountExportIDs{}, err
		}
		values[index] = value
	}
	return accountExportIDs{event: values[0], outbox: values[1], publish: values[2]}, nil
}

func (store AccountExportStore) failureIdentifiers(requestID string) (accountExportIDs, error) {
	values := make([]string, 3)
	for index, domain := range []string{"account-export-failed-event", "account-export-failed-outbox", "account-export-failed-publish"} {
		value, err := ids.DeterministicUUID(store.IDKey, domain, requestID)
		if err != nil {
			return accountExportIDs{}, err
		}
		values[index] = value
	}
	return accountExportIDs{event: values[0], outbox: values[1], publish: values[2]}, nil
}

func (store AccountExportStore) ttl() time.Duration {
	if store.TTL > 0 {
		return store.TTL
	}
	return defaultAccountExportTTL
}
func (store AccountExportStore) now() time.Time {
	if store.Now != nil {
		return store.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func decodeObject(encoded []byte) (map[string]any, error) {
	var value map[string]any
	if json.Unmarshal(encoded, &value) != nil || value == nil {
		return nil, ErrAccountExportInvalid
	}
	return value, nil
}
func decodeAny(encoded []byte) (any, error) {
	var value any
	if json.Unmarshal(encoded, &value) != nil {
		return nil, ErrAccountExportInvalid
	}
	return value, nil
}
func mustJSON(value any) []byte { encoded, _ := json.Marshal(value); return encoded }
func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
func accountExportPlaintextHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
