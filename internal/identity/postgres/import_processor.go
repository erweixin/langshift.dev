package postgres

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/identity/api"
	identityemail "github.com/langshift/lites/internal/identity/email"
	"github.com/langshift/lites/internal/payload"
)

const (
	maximumImportBytes = 5 << 20
	maximumImportRows  = 10_000
)

var (
	ErrImportInvalid = errors.New("identity import source is invalid")
	ErrImportFailed  = errors.New("identity import has failed")
)

// ImportSource retrieves the immutable object referenced by an import command.
// Implementations must not interpret object references as local file paths.
type ImportSource interface {
	Get(context.Context, string) ([]byte, error)
}

type ImportProcessResult struct {
	Status       string
	Version      uint64
	AcceptedRows int
	RejectedRows int
}

type invitationImportRecord struct {
	ID, TenantID, InitiatedBy, Status, ObjectRef, ContentHash, ImportKey, DefaultRole string
	Version                                                                           uint64
	AcceptedRows, RejectedRows                                                        int
}

type invitationCSVRow struct {
	Number int
	Email  string
	Role   string
}

type importedInvitation struct {
	row invitationCSVRow
	invitationPrepared
}

type importCompletionPrepared struct {
	SecurityEventID, EventID, OutboxID, CommandID string
	EventPayload                                  payload.Manifest
}

// ProcessInvitationImport is the durable consumer for
// identity.invitation_import.process. The tenant and import identifiers come
// from the authenticated, encrypted work command; all other fields are loaded
// from PostgreSQL and treated as authoritative.
func (service AuthService) ProcessInvitationImport(ctx context.Context, tenantID, importID string) (ImportProcessResult, error) {
	if service.Pool == nil || service.ImportSources == nil || service.Payloads == nil || tenantID == "" || importID == "" || service.StoreEpoch == "" || service.InvitationTokens.Purpose == "" || len(service.InvitationTokens.Pepper) < 32 {
		return ImportProcessResult{}, apiDependencyUnavailable()
	}
	record, terminal, err := service.claimInvitationImport(ctx, tenantID, importID)
	if err != nil || terminal {
		return importResult(record), err
	}
	contents, err := service.ImportSources.Get(ctx, record.ObjectRef)
	if err != nil {
		return ImportProcessResult{}, apiDependencyUnavailable()
	}
	if len(contents) == 0 || len(contents) > maximumImportBytes || !matchesSHA256(record.ContentHash, contents) {
		_ = service.failInvitationImport(ctx, record, "source_integrity_failed")
		return ImportProcessResult{Status: "failed", Version: record.Version}, ErrImportInvalid
	}
	rows, rejected, err := parseInvitationCSV(contents, record.DefaultRole)
	if err != nil {
		_ = service.failInvitationImport(ctx, record, "csv_contract_failed")
		return ImportProcessResult{Status: "failed", Version: record.Version}, ErrImportInvalid
	}
	now := service.now()
	prepared := make([]importedInvitation, 0, len(rows))
	for _, row := range rows {
		candidate, prepareErr := service.prepareImportedInvitation(ctx, record, row, now)
		if prepareErr != nil {
			return ImportProcessResult{}, prepareErr
		}
		prepared = append(prepared, importedInvitation{row: row, invitationPrepared: candidate})
	}
	completion, err := service.prepareImportCompletion(ctx, record.TenantID, record.ID, record.Version+1)
	if err != nil {
		return ImportProcessResult{}, err
	}
	return service.commitInvitationImport(ctx, record, prepared, rejected, completion, now)
}

func (service AuthService) claimInvitationImport(ctx context.Context, tenantID, importID string) (invitationImportRecord, bool, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return invitationImportRecord{}, false, apiDependencyUnavailable()
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return invitationImportRecord{}, false, apiDependencyUnavailable()
	}
	var record invitationImportRecord
	err = tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,initiated_by::text,status,object_ref,content_hash,import_key,default_role,version FROM identity.invitation_imports WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, importID, tenantID).Scan(&record.ID, &record.TenantID, &record.InitiatedBy, &record.Status, &record.ObjectRef, &record.ContentHash, &record.ImportKey, &record.DefaultRole, &record.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return invitationImportRecord{}, false, ErrImportInvalid
	}
	if err != nil {
		return invitationImportRecord{}, false, apiDependencyUnavailable()
	}
	switch record.Status {
	case "completed":
		var accepted, rejected int
		if err = tx.QueryRow(ctx, `SELECT accepted_rows,rejected_rows FROM identity.invitation_imports WHERE id=$1`, record.ID).Scan(&accepted, &rejected); err != nil {
			return invitationImportRecord{}, false, apiDependencyUnavailable()
		}
		if err = tx.Commit(ctx); err != nil {
			return invitationImportRecord{}, false, apiDependencyUnavailable()
		}
		record.AcceptedRows = accepted
		record.RejectedRows = rejected
		return record, true, nil
	case "failed":
		return record, true, ErrImportFailed
	case "queued", "processing":
	default:
		return record, false, ErrImportInvalid
	}
	if record.Status == "queued" {
		if _, err = tx.Exec(ctx, `UPDATE identity.invitation_imports SET status='processing',updated_at=$1 WHERE id=$2 AND status='queued'`, service.now(), record.ID); err != nil {
			return invitationImportRecord{}, false, apiDependencyUnavailable()
		}
		record.Status = "processing"
	}
	if err = tx.Commit(ctx); err != nil {
		return invitationImportRecord{}, false, apiDependencyUnavailable()
	}
	return record, false, nil
}

func importResult(record invitationImportRecord) ImportProcessResult {
	return ImportProcessResult{Status: record.Status, Version: record.Version, AcceptedRows: record.AcceptedRows, RejectedRows: record.RejectedRows}
}

func (service AuthService) commitInvitationImport(ctx context.Context, record invitationImportRecord, candidates []importedInvitation, initiallyRejected int, completion importCompletionPrepared, now time.Time) (ImportProcessResult, error) {
	// The import row is the aggregate lock. Read committed avoids surfacing
	// serialization failures to duplicate workers while still allowing exactly
	// one worker to transition processing -> completed.
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ImportProcessResult{}, apiDependencyUnavailable()
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, record.TenantID); err != nil {
		return ImportProcessResult{}, apiDependencyUnavailable()
	}
	var status string
	var currentVersion uint64
	var accepted, rejected int
	if err = tx.QueryRow(ctx, `SELECT status,version,accepted_rows,rejected_rows FROM identity.invitation_imports WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, record.ID, record.TenantID).Scan(&status, &currentVersion, &accepted, &rejected); err != nil {
		return ImportProcessResult{}, apiDependencyUnavailable()
	}
	if status == "completed" {
		if err = tx.Commit(ctx); err != nil {
			return ImportProcessResult{}, apiDependencyUnavailable()
		}
		return ImportProcessResult{Status: status, Version: currentVersion, AcceptedRows: accepted, RejectedRows: rejected}, nil
	}
	if status != "processing" || currentVersion != record.Version {
		return ImportProcessResult{}, ErrImportFailed
	}
	var tenantActive bool
	if err = tx.QueryRow(ctx, `SELECT identity.lock_active_tenant($1)`, record.TenantID).Scan(&tenantActive); err != nil || !tenantActive {
		return ImportProcessResult{}, ErrImportFailed
	}
	accepted = 0
	rejected = initiallyRejected
	actor, _ := json.Marshal(map[string]string{"kind": "user", "id": record.InitiatedBy})
	for _, candidate := range candidates {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, record.TenantID+"|"+candidate.row.Email); err != nil {
			return ImportProcessResult{}, apiDependencyUnavailable()
		}
		var conflict bool
		err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM identity.memberships m JOIN identity.users u ON u.id=m.user_id WHERE m.tenant_id=$1 AND u.normalized_email=$2 AND m.status='active') OR EXISTS (SELECT 1 FROM identity.invitations i WHERE i.tenant_id=$1 AND i.normalized_email=$2 AND i.accepted_at IS NULL AND i.rejected_at IS NULL AND i.revoked_at IS NULL AND i.expires_at>$3)`, record.TenantID, candidate.row.Email, now).Scan(&conflict)
		if err != nil {
			return ImportProcessResult{}, apiDependencyUnavailable()
		}
		if conflict {
			rejected++
			continue
		}
		if _, err = tx.Exec(ctx, `INSERT INTO identity.invitations (id,tenant_id,normalized_email,role,token_hash,invited_by,expires_at,batch_key) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, candidate.InvitationID, record.TenantID, candidate.row.Email, candidate.row.Role, candidate.TokenDigest, record.InitiatedBy, candidate.ExpiresAt, record.ImportKey); err != nil {
			return ImportProcessResult{}, err
		}
		details, _ := json.Marshal(map[string]any{"role_assigned": true, "import_id": record.ID, "row_number": candidate.row.Number})
		if _, err = tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,actor_user_id,event_type,request_id,details,occurred_at) VALUES ($1,$2,$3,'invitation_created',$4,$5,$6)`, candidate.SecurityEventID, record.TenantID, record.InitiatedBy, "import:"+record.ID, details, now); err != nil {
			return ImportProcessResult{}, err
		}
		if _, err = service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: candidate.EventID, TenantID: record.TenantID, UserID: record.InitiatedBy, EventType: "InvitationCreated", SchemaVersion: 1, AggregateKind: "invitation", AggregateID: candidate.InvitationID, AggregateVersion: 1, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: candidate.SecurityEventID, PayloadRef: candidate.EventPayload.Ref, PayloadHash: candidate.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: candidate.PublishOutboxID, CommandID: candidate.PublishCommandID, CommandType: "events.publish", PayloadRef: candidate.EventPayload.Ref, PayloadHash: candidate.EventPayload.Hash}, {ID: candidate.MailOutboxID, CommandID: candidate.MailCommandID, CommandType: "identity.email.invitation", PayloadRef: candidate.MailCommand.Ref, PayloadHash: candidate.MailCommand.Hash}}}); err != nil {
			return ImportProcessResult{}, err
		}
		accepted++
	}
	nextVersion := record.Version + 1
	if _, err = tx.Exec(ctx, `UPDATE identity.invitation_imports SET status='completed',version=$1,accepted_rows=$2,rejected_rows=$3,completed_at=$4,updated_at=$4 WHERE id=$5 AND version=$6 AND status='processing'`, nextVersion, accepted, rejected, now, record.ID, record.Version); err != nil {
		return ImportProcessResult{}, err
	}
	details, _ := json.Marshal(map[string]any{"accepted_rows": accepted, "rejected_rows": rejected, "content_hash": record.ContentHash})
	if _, err = tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,actor_user_id,event_type,request_id,details,occurred_at) VALUES ($1,$2,$3,'invitation_import_completed',$4,$5,$6)`, completion.SecurityEventID, record.TenantID, record.InitiatedBy, "import:"+record.ID, details, now); err != nil {
		return ImportProcessResult{}, err
	}
	if _, err = service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: completion.EventID, TenantID: record.TenantID, UserID: record.InitiatedBy, EventType: "InvitationImportCompleted", SchemaVersion: 1, AggregateKind: "invitation_import", AggregateID: record.ID, AggregateVersion: nextVersion, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: completion.SecurityEventID, PayloadRef: completion.EventPayload.Ref, PayloadHash: completion.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: completion.OutboxID, CommandID: completion.CommandID, CommandType: "events.publish", PayloadRef: completion.EventPayload.Ref, PayloadHash: completion.EventPayload.Hash}}}); err != nil {
		return ImportProcessResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ImportProcessResult{}, apiDependencyUnavailable()
	}
	return ImportProcessResult{Status: "completed", Version: nextVersion, AcceptedRows: accepted, RejectedRows: rejected}, nil
}

func (service AuthService) prepareImportedInvitation(ctx context.Context, record invitationImportRecord, row invitationCSVRow, now time.Time) (invitationPrepared, error) {
	var prepared invitationPrepared
	for _, target := range []*string{&prepared.InvitationID, &prepared.SecurityEventID, &prepared.EventID, &prepared.PublishOutboxID, &prepared.PublishCommandID, &prepared.MailOutboxID, &prepared.MailCommandID} {
		id, err := service.newID()
		if err != nil {
			return invitationPrepared{}, apiDependencyUnavailable()
		}
		*target = id
	}
	manager := service.InvitationTokens
	if manager.Random == nil {
		manager.Random = service.random()
	}
	token, err := manager.Issue()
	if err != nil {
		return invitationPrepared{}, apiDependencyUnavailable()
	}
	prepared.TokenDigest = append([]byte(nil), token.Digest[:]...)
	prepared.ExpiresAt = now.Add(7 * 24 * time.Hour)
	prepared.EventPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: record.TenantID, ObjectID: prepared.EventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": prepared.InvitationID, "subject_version": 1})
	if err == nil {
		prepared.MailCommand, err = service.putJSON(ctx, payload.Descriptor{TenantID: record.TenantID, ObjectID: prepared.MailCommandID, Class: "mail-command", ContentType: "application/json"}, map[string]any{"template": "tenant-invitation-v1", "recipient": row.Email, "invitation_id": prepared.InvitationID, "token": token.Raw, "expires_at": prepared.ExpiresAt})
	}
	if err != nil {
		return invitationPrepared{}, apiDependencyUnavailable()
	}
	return prepared, nil
}

func (service AuthService) prepareImportCompletion(ctx context.Context, tenantID, importID string, version uint64) (importCompletionPrepared, error) {
	var prepared importCompletionPrepared
	for _, target := range []*string{&prepared.SecurityEventID, &prepared.EventID, &prepared.OutboxID, &prepared.CommandID} {
		id, err := service.newID()
		if err != nil {
			return importCompletionPrepared{}, apiDependencyUnavailable()
		}
		*target = id
	}
	manifest, err := service.putJSON(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: prepared.EventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": importID, "subject_version": version})
	if err != nil {
		return importCompletionPrepared{}, apiDependencyUnavailable()
	}
	prepared.EventPayload = manifest
	return prepared, nil
}

func (service AuthService) failInvitationImport(ctx context.Context, record invitationImportRecord, reasonCode string) error {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, record.TenantID); err != nil {
		return err
	}
	securityEventID, err := service.newID()
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE identity.invitation_imports SET status='failed',updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND status='processing'`, service.now(), record.ID, record.TenantID)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	details, _ := json.Marshal(map[string]string{"reason_code": reasonCode})
	if _, err = tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,actor_user_id,event_type,request_id,details,occurred_at) VALUES ($1,$2,$3,'invitation_import_failed',$4,$5,$6)`, securityEventID, record.TenantID, record.InitiatedBy, "import:"+record.ID, details, service.now()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func parseInvitationCSV(contents []byte, defaultRole string) ([]invitationCSVRow, int, error) {
	if !utf8.Valid(contents) {
		return nil, 0, ErrImportInvalid
	}
	reader := csv.NewReader(strings.NewReader(string(contents)))
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil || (len(header) != 1 && len(header) != 2) || header[0] != "email" || (len(header) == 2 && header[1] != "role") {
		return nil, 0, ErrImportInvalid
	}
	seen := map[string]struct{}{}
	rows := make([]invitationCSVRow, 0)
	rejected := 0
	for number := 2; ; number++ {
		record, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, 0, ErrImportInvalid
		}
		if number-1 > maximumImportRows {
			return nil, 0, ErrImportInvalid
		}
		if len(record) != len(header) {
			rejected++
			continue
		}
		email, normalizeErr := identityemail.Normalize(record[0])
		role := defaultRole
		if len(record) == 2 && record[1] != "" {
			role = record[1]
		}
		_, duplicate := seen[email]
		if normalizeErr != nil || !validInvitationRole(role) || duplicate {
			rejected++
			continue
		}
		seen[email] = struct{}{}
		rows = append(rows, invitationCSVRow{Number: number, Email: email, Role: role})
	}
	return rows, rejected, nil
}

func matchesSHA256(expected string, contents []byte) bool {
	digest := sha256.Sum256(contents)
	actual := "sha256:" + hex.EncodeToString(digest[:])
	return len(expected) == len(actual) && hmac.Equal([]byte(expected), []byte(actual))
}

func apiDependencyUnavailable() error {
	// Kept behind a helper so worker code does not expose storage errors or
	// accidentally couple retry decisions to PostgreSQL error strings.
	return api.ErrDependencyUnavailable
}
