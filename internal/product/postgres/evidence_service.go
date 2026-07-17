package postgres

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/idempotency"
	idempotencypostgres "github.com/langshift/lites/internal/idempotency/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	productapi "github.com/langshift/lites/internal/product/api"
	"io"
	"net/http"
	"time"
)

const evidenceRecordOperation = "evidence.record.v2"
const evidencePageSize = 50

type EvidenceService struct {
	Pool                                                        *pgxpool.Pool
	Appender                                                    eventpostgres.Appender
	Payloads                                                    payload.Store
	IDKey, CursorKey, IdempotencyKeyPepper, RequestDigestPepper []byte
	StoreEpoch                                                  string
	IdempotencyTTL                                              time.Duration
	Now                                                         func() time.Time
}
type evidenceCursor struct {
	TenantID   string    `json:"tenant_id"`
	UserID     string    `json:"user_id"`
	RecordedAt time.Time `json:"recorded_at"`
	ID         string    `json:"id"`
}

func (s EvidenceService) List(ctx context.Context, q productapi.EvidenceListQuery) (productapi.EvidenceListResult, error) {
	if !s.valid() || q.TenantID == "" || q.UserID == "" {
		return productapi.EvidenceListResult{}, productapi.ErrValidation
	}
	var cursor *evidenceCursor
	if q.Cursor != "" {
		c, e := s.decodeCursor(q.Cursor)
		if e != nil || c.TenantID != q.TenantID || c.UserID != q.UserID {
			return productapi.EvidenceListResult{}, productapi.ErrValidation
		}
		cursor = &c
	}
	tx, e := s.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if e != nil {
		return productapi.EvidenceListResult{}, s.mapError(e)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, e = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, q.TenantID); e != nil {
		return productapi.EvidenceListResult{}, s.mapError(e)
	}
	args := []any{q.TenantID, q.UserID, evidencePageSize + 1}
	sql := `SELECT id::text,version,mission_id::text,evidence_type,status,source_kind,source_id::text,payload_ref,content_hash,COALESCE(plaintext_hash,''),recorded_at FROM product.evidence WHERE tenant_id=$1 AND user_id=$2`
	if cursor != nil {
		sql += ` AND (recorded_at<$4 OR (recorded_at=$4 AND id<$5::uuid))`
		args = append(args, cursor.RecordedAt, cursor.ID)
	}
	sql += ` ORDER BY recorded_at DESC,id DESC LIMIT $3`
	rows, e := tx.Query(ctx, sql, args...)
	if e != nil {
		return productapi.EvidenceListResult{}, s.mapError(e)
	}
	defer rows.Close()
	items := []productapi.EvidenceResource{}
	for rows.Next() {
		var item productapi.EvidenceResource
		var ref, manifestHash string
		if e = rows.Scan(&item.ID, &item.Version, &item.MissionID, &item.EvidenceType, &item.Status, &item.SourceKind, &item.SourceID, &ref, &manifestHash, &item.PlaintextHash, &item.RecordedAt); e != nil {
			return productapi.EvidenceListResult{}, s.mapError(e)
		}
		encoded, getErr := s.Payloads.Get(ctx, payload.Descriptor{TenantID: q.TenantID, ObjectID: item.ID, Class: "product-evidence", ContentType: "application/json"}, payload.Manifest{Ref: ref, Hash: manifestHash})
		if getErr != nil || !validJSONObject(encoded) {
			return productapi.EvidenceListResult{}, s.mapError(errors.Join(getErr, payload.ErrIntegrity))
		}
		item.Content = encoded
		items = append(items, item)
	}
	if e = rows.Err(); e != nil {
		return productapi.EvidenceListResult{}, s.mapError(e)
	}
	var next *string
	if len(items) > evidencePageSize {
		last := items[evidencePageSize-1]
		v, er := s.encodeCursor(evidenceCursor{TenantID: q.TenantID, UserID: q.UserID, RecordedAt: last.RecordedAt, ID: last.ID})
		if er != nil {
			return productapi.EvidenceListResult{}, s.mapError(er)
		}
		next = &v
		items = items[:evidencePageSize]
	}
	if e = tx.Commit(ctx); e != nil {
		return productapi.EvidenceListResult{}, s.mapError(e)
	}
	return productapi.EvidenceListResult{Items: items, NextCursor: next}, nil
}
func (s EvidenceService) Record(ctx context.Context, c productapi.RecordEvidenceCommand) (productapi.EvidenceMutationResult, error) {
	if !s.valid() || !validMissionMetadata(c.CommandMetadata) || c.MissionID == "" || c.EvidenceType == "" || c.SourceKind != "user_note" && c.SourceKind != "artifact" && c.SourceKind != "test_result" || c.Content == "" || len(c.ContentHash) != 64 {
		return productapi.EvidenceMutationResult{}, productapi.ErrValidation
	}
	plain := sha256.Sum256([]byte(c.Content))
	if hex.EncodeToString(plain[:]) != c.ContentHash {
		return productapi.EvidenceMutationResult{}, productapi.ErrValidation
	}
	canonical, _ := json.Marshal(c)
	requestHash, e := idempotency.RequestDigest(canonical, s.RequestDigestPepper)
	if e != nil {
		return productapi.EvidenceMutationResult{}, productapi.ErrDependencyUnavailable
	}
	recordID, e := ids.DeterministicUUID(s.IDKey, "product-idempotency:"+evidenceRecordOperation, c.TenantID+"\x00"+c.UserID+"\x00"+c.IdempotencyKey)
	if e != nil {
		return productapi.EvidenceMutationResult{}, productapi.ErrDependencyUnavailable
	}
	evidenceID, _ := ids.DeterministicUUID(s.IDKey, "manual-evidence", recordID)
	eventID, _ := ids.DeterministicUUID(s.IDKey, "manual-evidence-event", recordID)
	descriptor := payload.Descriptor{TenantID: c.TenantID, ObjectID: recordID, Class: "product-evidence-idempotency", ContentType: "application/json"}
	input := idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: c.TenantID, UserID: c.UserID, OperationID: evidenceRecordOperation}, RawKey: c.IdempotencyKey, RequestHash: requestHash, RequestID: c.RequestID}
	executor := idempotencypostgres.Executor{Pool: s.Pool, KeyPepper: s.IdempotencyKeyPepper, TTL: s.IdempotencyTTL, Now: s.Now}
	if response, found, er := executor.LoadCompleted(ctx, input); er != nil {
		return productapi.EvidenceMutationResult{}, s.mapError(er)
	} else if found {
		out, re := s.read(ctx, descriptor, response)
		out.Replayed = re == nil
		return out, s.mapError(re)
	}
	body := map[string]any{"schema_version": 1, "evidence_type": c.EvidenceType, "source_kind": c.SourceKind, "source_id": nullableString(c.SourceID), "content": c.Content, "plaintext_hash": c.ContentHash}
	manifest, e := s.putJSON(ctx, payload.Descriptor{TenantID: c.TenantID, ObjectID: evidenceID, Class: "product-evidence", ContentType: "application/json"}, body)
	if e != nil {
		return productapi.EvidenceMutationResult{}, s.mapError(e)
	}
	eventManifest, e := s.putJSON(ctx, payload.Descriptor{TenantID: c.TenantID, ObjectID: eventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": evidenceID, "subject_version": 1, "mission_id": c.MissionID, "evidence_type": c.EvidenceType, "plaintext_hash": c.ContentHash, "payload_hash": manifest.Hash})
	if e != nil {
		return productapi.EvidenceMutationResult{}, s.mapError(e)
	}
	response, replayed, e := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if _, er := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, c.TenantID); er != nil {
			return idempotency.Response{}, er
		}
		var missionStatus string
		if er := tx.QueryRow(ctx, `SELECT status FROM product.missions WHERE tenant_id=$1 AND user_id=$2 AND id=$3 FOR SHARE`, c.TenantID, c.UserID, c.MissionID).Scan(&missionStatus); er != nil {
			return idempotency.Response{}, er
		}
		if missionStatus == "archived" {
			return idempotency.Response{}, ErrRouteConflict
		}
		now := s.now()
		var source any
		if c.SourceID != "" {
			source = c.SourceID
		}
		_, er := tx.Exec(ctx, `INSERT INTO product.evidence(id,tenant_id,user_id,version,mission_id,evidence_type,status,source_kind,source_id,payload_ref,content_hash,plaintext_hash,recorded_at,created_at,updated_at) VALUES($1,$2,$3,1,$4,$5,'recorded',$6,$7,$8,$9,$10,$11,$11,$11)`, evidenceID, c.TenantID, c.UserID, c.MissionID, c.EvidenceType, c.SourceKind, source, manifest.Ref, manifest.Hash, c.ContentHash, now)
		if er != nil {
			return idempotency.Response{}, er
		}
		outbox, _ := ids.DeterministicUUID(s.IDKey, "manual-evidence-outbox", recordID)
		publish, _ := ids.DeterministicUUID(s.IDKey, "manual-evidence-publish", recordID)
		_, er = s.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: c.TenantID, UserID: c.UserID, EventType: "CapabilityEvidenceRecorded", SchemaVersion: 1, AggregateKind: "evidence", AggregateID: evidenceID, AggregateVersion: 1, StoreEpoch: s.StoreEpoch, OccurredAt: now, Actor: missionActor(c.UserID, c.SessionID), CorrelationID: c.RequestID, PayloadRef: eventManifest.Ref, PayloadHash: eventManifest.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outbox, CommandID: publish, CommandType: "events.publish", PayloadRef: eventManifest.Ref, PayloadHash: eventManifest.Hash}}})
		if er != nil {
			return idempotency.Response{}, er
		}
		result := productapi.EvidenceMutationResult{ID: evidenceID, Version: 1, Status: "recorded", UpdatedAt: now, EventID: eventID}
		stored, er := s.putJSON(ctx, descriptor, result)
		if er != nil {
			return idempotency.Response{}, er
		}
		return idempotency.Response{Status: http.StatusOK, ContentType: "application/vnd.lites.evidence.v2+json", PayloadRef: stored.Ref, Hash: stored.Hash, ResourceVersion: 1}, nil
	})
	if e != nil {
		return productapi.EvidenceMutationResult{}, s.mapError(e)
	}
	out, e := s.read(ctx, descriptor, response)
	if e != nil {
		return out, s.mapError(e)
	}
	out.Replayed = replayed
	return out, nil
}
func (s EvidenceService) encodeCursor(c evidenceCursor) (string, error) {
	b, e := json.Marshal(c)
	if e != nil {
		return "", e
	}
	m := hmac.New(sha256.New, s.CursorKey)
	_, _ = m.Write(b)
	return base64.RawURLEncoding.EncodeToString(append(b, m.Sum(nil)...)), nil
}
func (s EvidenceService) decodeCursor(v string) (evidenceCursor, error) {
	if len(v) > 2048 {
		return evidenceCursor{}, productapi.ErrValidation
	}
	b, e := base64.RawURLEncoding.DecodeString(v)
	if e != nil || len(b) <= sha256.Size {
		return evidenceCursor{}, productapi.ErrValidation
	}
	body, sig := b[:len(b)-sha256.Size], b[len(b)-sha256.Size:]
	m := hmac.New(sha256.New, s.CursorKey)
	_, _ = m.Write(body)
	if !hmac.Equal(sig, m.Sum(nil)) {
		return evidenceCursor{}, productapi.ErrValidation
	}
	var c evidenceCursor
	if json.Unmarshal(body, &c) != nil || c.TenantID == "" || c.UserID == "" || c.ID == "" || c.RecordedAt.IsZero() {
		return c, productapi.ErrValidation
	}
	return c, nil
}
func (s EvidenceService) putJSON(ctx context.Context, d payload.Descriptor, v any) (payload.Manifest, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return payload.Manifest{}, e
	}
	return s.Payloads.Put(ctx, d, b)
}
func (s EvidenceService) read(ctx context.Context, d payload.Descriptor, r idempotency.Response) (productapi.EvidenceMutationResult, error) {
	b, e := s.Payloads.Get(ctx, d, payload.Manifest{Ref: r.PayloadRef, Hash: r.Hash})
	if e != nil {
		return productapi.EvidenceMutationResult{}, e
	}
	var out productapi.EvidenceMutationResult
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if dec.Decode(&out) != nil || !errors.Is(dec.Decode(&struct{}{}), io.EOF) {
		return out, payload.ErrIntegrity
	}
	return out, nil
}
func (s EvidenceService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}
func (s EvidenceService) valid() bool {
	return s.Pool != nil && s.Payloads != nil && len(s.IDKey) >= 32 && len(s.CursorKey) >= 32 && len(s.IdempotencyKeyPepper) >= 32 && len(s.RequestDigestPepper) >= 32 && s.StoreEpoch != "" && s.IdempotencyTTL > 0
}
func (s EvidenceService) mapError(e error) error {
	switch {
	case e == nil:
		return nil
	case errors.Is(e, pgx.ErrNoRows):
		return productapi.ErrResourceNotFound
	case errors.Is(e, idempotency.ErrKeyConflict), errors.Is(e, idempotency.ErrInProgress):
		return productapi.ErrIdempotencyConflict
	case errors.Is(e, ErrRouteConflict):
		return productapi.ErrStateConflict
	default:
		return errors.Join(productapi.ErrDependencyUnavailable, e)
	}
}

var _ productapi.EvidenceService = EvidenceService{}
