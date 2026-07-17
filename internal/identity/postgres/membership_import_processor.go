package postgres

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/identity/api"
	identityemail "github.com/langshift/lites/internal/identity/email"
	"github.com/langshift/lites/internal/payload"
)

type membershipImportRecord struct {
	ID, TenantID, InitiatedBy, Status, ObjectRef, ContentHash, ImportKey, Mode string
	Version                                                                    uint64
	AcceptedRows, RejectedRows, DeactivatedRows                                int
}

type membershipCSVRow struct {
	Number int
	Email  string
	Role   string
}

type membershipImportSnapshot struct {
	Memberships map[string]membershipRow
	Users       map[string]membershipImportUser
	Sessions    map[string][]sessionRow
}

type membershipImportUser struct {
	ID, Email, Status string
	Verified          bool
}

type membershipImportAction struct {
	Kind, EventType, Email, UserID, MembershipID, PreviousRole, Role, PreviousStatus, Status string
	RowNumber                                                                                int
	PreviousVersion, Version                                                                 uint64
	SecurityEventID, EventID, OutboxID, CommandID                                            string
	EventPayload                                                                             payload.Manifest
	Sessions                                                                                 []sessionRevocationPrepared
}

type plannedMembershipImport struct {
	Actions                    []membershipImportAction
	AcceptedRows, RejectedRows int
	DeactivatedRows            int
	DeactivateMissingApplied   bool
}

func (service AuthService) ProcessMembershipImport(ctx context.Context, tenantID, importID string) (ImportProcessResult, error) {
	return service.processMembershipImport(ctx, tenantID, importID, nil)
}

func (service AuthService) ProcessMembershipImportDelivery(ctx context.Context, store eventpostgres.InboxStore, claim eventpostgres.InboxClaim, importID string) (ImportProcessResult, error) {
	if claim.Command.CommandType != "identity.membership_import.process" || claim.Command.AggregateKind != "membership_import" || claim.Command.TenantID == "" || importID == "" || importID != claim.Command.AggregateID {
		return ImportProcessResult{}, ErrImportInvalid
	}
	return service.processMembershipImport(ctx, claim.Command.TenantID, importID, &importInboxCompletion{Store: store, Claim: claim})
}

func (service AuthService) processMembershipImport(ctx context.Context, tenantID, importID string, inbox *importInboxCompletion) (ImportProcessResult, error) {
	if service.Pool == nil || service.ImportSources == nil || service.Payloads == nil || tenantID == "" || importID == "" || service.StoreEpoch == "" {
		return ImportProcessResult{}, apiDependencyUnavailable()
	}
	record, terminal, err := service.claimMembershipImport(ctx, tenantID, importID)
	if err != nil || terminal {
		if terminal && inbox != nil && (err == nil || errors.Is(err, ErrImportFailed)) {
			if completeErr := inbox.Store.Complete(ctx, inbox.Claim); completeErr != nil {
				return ImportProcessResult{}, completeErr
			}
		}
		return membershipImportResult(record), err
	}
	contents, err := service.ImportSources.Get(ctx, record.ObjectRef)
	if err != nil {
		return ImportProcessResult{}, apiDependencyUnavailable()
	}
	if len(contents) == 0 || len(contents) > maximumImportBytes || !matchesSHA256(record.ContentHash, contents) {
		_ = service.failMembershipImport(ctx, record, "source_integrity_failed")
		return ImportProcessResult{Status: "failed", Version: record.Version}, ErrImportInvalid
	}
	rows, rejected, err := parseMembershipCSV(contents)
	if err != nil {
		_ = service.failMembershipImport(ctx, record, "csv_contract_failed")
		return ImportProcessResult{Status: "failed", Version: record.Version}, ErrImportInvalid
	}
	snapshot, err := service.loadMembershipImportSnapshot(ctx, record, rows)
	if err != nil {
		return ImportProcessResult{}, err
	}
	plan, err := service.planMembershipImport(ctx, record, rows, rejected, snapshot)
	if err != nil {
		return ImportProcessResult{}, err
	}
	completion, err := service.prepareImportCompletion(ctx, record.TenantID, record.ID, record.Version+1)
	if err != nil {
		return ImportProcessResult{}, err
	}
	if inbox != nil {
		refreshed, heartbeatErr := inbox.Store.Heartbeat(ctx, inbox.Claim)
		if heartbeatErr != nil {
			return ImportProcessResult{}, heartbeatErr
		}
		inbox.Claim = refreshed
	}
	return service.commitMembershipImport(ctx, record, snapshot, plan, completion, inbox)
}

func (service AuthService) claimMembershipImport(ctx context.Context, tenantID, importID string) (membershipImportRecord, bool, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return membershipImportRecord{}, false, apiDependencyUnavailable()
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return membershipImportRecord{}, false, apiDependencyUnavailable()
	}
	var record membershipImportRecord
	err = tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,initiated_by::text,status,object_ref,content_hash,import_key,mode,version,accepted_rows,rejected_rows,deactivated_rows FROM identity.membership_imports WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, importID, tenantID).Scan(&record.ID, &record.TenantID, &record.InitiatedBy, &record.Status, &record.ObjectRef, &record.ContentHash, &record.ImportKey, &record.Mode, &record.Version, &record.AcceptedRows, &record.RejectedRows, &record.DeactivatedRows)
	if errors.Is(err, pgx.ErrNoRows) {
		return membershipImportRecord{}, false, ErrImportInvalid
	}
	if err != nil {
		return membershipImportRecord{}, false, apiDependencyUnavailable()
	}
	switch record.Status {
	case "completed":
		if err = tx.Commit(ctx); err != nil {
			return membershipImportRecord{}, false, apiDependencyUnavailable()
		}
		return record, true, nil
	case "failed":
		return record, true, ErrImportFailed
	case "queued", "processing":
	default:
		return record, false, ErrImportInvalid
	}
	if record.Status == "queued" {
		if _, err = tx.Exec(ctx, `UPDATE identity.membership_imports SET status='processing',updated_at=$1 WHERE id=$2 AND status='queued'`, service.now(), record.ID); err != nil {
			return membershipImportRecord{}, false, apiDependencyUnavailable()
		}
		record.Status = "processing"
	}
	if err = tx.Commit(ctx); err != nil {
		return membershipImportRecord{}, false, apiDependencyUnavailable()
	}
	return record, false, nil
}

func membershipImportResult(record membershipImportRecord) ImportProcessResult {
	return ImportProcessResult{Status: record.Status, Version: record.Version, AcceptedRows: record.AcceptedRows, RejectedRows: record.RejectedRows, DeactivatedRows: record.DeactivatedRows}
}

func (service AuthService) loadMembershipImportSnapshot(ctx context.Context, record membershipImportRecord, csvRows []membershipCSVRow) (membershipImportSnapshot, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return membershipImportSnapshot{}, apiDependencyUnavailable()
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, record.TenantID); err != nil {
		return membershipImportSnapshot{}, apiDependencyUnavailable()
	}
	var tenantKind, tenantStatus string
	if err = tx.QueryRow(ctx, `SELECT kind,status FROM identity.tenants WHERE id=$1`, record.TenantID).Scan(&tenantKind, &tenantStatus); err != nil || tenantKind != "enterprise" || tenantStatus != "active" {
		return membershipImportSnapshot{}, ErrImportFailed
	}
	snapshot := membershipImportSnapshot{Memberships: map[string]membershipRow{}, Users: map[string]membershipImportUser{}, Sessions: map[string][]sessionRow{}}
	rows, err := tx.Query(ctx, `SELECT m.id::text,m.tenant_id::text,m.user_id::text,u.normalized_email,m.role,m.status,m.version,m.joined_at,m.updated_at,m.deactivated_at,t.kind FROM identity.memberships m JOIN identity.users u ON u.id=m.user_id JOIN identity.tenants t ON t.id=m.tenant_id WHERE m.tenant_id=$1 ORDER BY m.id`, record.TenantID)
	if err != nil {
		return membershipImportSnapshot{}, apiDependencyUnavailable()
	}
	for rows.Next() {
		var row membershipRow
		if err = rows.Scan(&row.ID, &row.TenantID, &row.UserID, &row.Email, &row.Role, &row.Status, &row.Version, &row.JoinedAt, &row.UpdatedAt, &row.DeactivatedAt, &row.TenantKind); err != nil {
			rows.Close()
			return membershipImportSnapshot{}, apiDependencyUnavailable()
		}
		snapshot.Memberships[row.Email] = row
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return membershipImportSnapshot{}, apiDependencyUnavailable()
	}
	rows.Close()
	emails := make([]string, 0, len(csvRows))
	for _, row := range csvRows {
		emails = append(emails, row.Email)
	}
	if len(emails) > 0 {
		userRows, queryErr := tx.Query(ctx, `SELECT id::text,normalized_email,status,email_verified_at IS NOT NULL FROM identity.users WHERE normalized_email=ANY($1::text[]) ORDER BY id`, emails)
		if queryErr != nil {
			return membershipImportSnapshot{}, apiDependencyUnavailable()
		}
		for userRows.Next() {
			var user membershipImportUser
			if err = userRows.Scan(&user.ID, &user.Email, &user.Status, &user.Verified); err != nil {
				userRows.Close()
				return membershipImportSnapshot{}, apiDependencyUnavailable()
			}
			snapshot.Users[user.Email] = user
		}
		if err = userRows.Err(); err != nil {
			userRows.Close()
			return membershipImportSnapshot{}, apiDependencyUnavailable()
		}
		userRows.Close()
	}
	sessionRows, err := tx.Query(ctx, `SELECT s.id::text,s.active_tenant_id::text,s.version,s.created_at,s.updated_at,s.last_seen_at,s.expires_at,s.revoked_at,s.device_label,m.user_id::text FROM identity.sessions s JOIN identity.memberships m ON m.user_id=s.user_id AND m.tenant_id=s.active_tenant_id WHERE s.active_tenant_id=$1 AND s.revoked_at IS NULL ORDER BY s.user_id,s.id`, record.TenantID)
	if err != nil {
		return membershipImportSnapshot{}, apiDependencyUnavailable()
	}
	for sessionRows.Next() {
		var row sessionRow
		var userID string
		if err = sessionRows.Scan(&row.ID, &row.ActiveTenantID, &row.Version, &row.CreatedAt, &row.UpdatedAt, &row.LastSeenAt, &row.ExpiresAt, &row.RevokedAt, &row.DeviceLabel, &userID); err != nil {
			sessionRows.Close()
			return membershipImportSnapshot{}, apiDependencyUnavailable()
		}
		snapshot.Sessions[userID] = append(snapshot.Sessions[userID], row)
	}
	if err = sessionRows.Err(); err != nil {
		sessionRows.Close()
		return membershipImportSnapshot{}, apiDependencyUnavailable()
	}
	sessionRows.Close()
	if err = tx.Commit(ctx); err != nil {
		return membershipImportSnapshot{}, apiDependencyUnavailable()
	}
	return snapshot, nil
}

func (service AuthService) planMembershipImport(ctx context.Context, record membershipImportRecord, rows []membershipCSVRow, initialRejected int, snapshot membershipImportSnapshot) (plannedMembershipImport, error) {
	plan := plannedMembershipImport{RejectedRows: initialRejected}
	roster := map[string]struct{}{}
	for _, row := range rows {
		current, exists := snapshot.Memberships[row.Email]
		user, userExists := snapshot.Users[row.Email]
		if !userExists || !user.Verified || user.Status != "active" || (exists && current.Status == "left") || (row.Role == "owner" && (!exists || current.Role != "owner")) || (exists && current.Role == "owner" && row.Role != "owner") {
			plan.RejectedRows++
			continue
		}
		roster[row.Email] = struct{}{}
		plan.AcceptedRows++
		if !exists {
			membershipID, err := service.newID()
			if err != nil {
				return plannedMembershipImport{}, apiDependencyUnavailable()
			}
			action := membershipImportAction{Kind: "create", EventType: "MembershipCreated", Email: row.Email, UserID: user.ID, MembershipID: membershipID, Role: row.Role, Status: "active", RowNumber: row.Number, Version: 1}
			prepared, err := service.prepareMembershipImportAction(ctx, record, action)
			if err != nil {
				return plannedMembershipImport{}, err
			}
			plan.Actions = append(plan.Actions, prepared)
			continue
		}
		if current.Status != "active" {
			action := membershipImportAction{Kind: "reactivate", EventType: "MembershipReactivated", Email: row.Email, UserID: current.UserID, MembershipID: current.ID, PreviousRole: current.Role, Role: row.Role, PreviousStatus: current.Status, Status: "active", RowNumber: row.Number, PreviousVersion: current.Version, Version: current.Version + 1}
			prepared, err := service.prepareMembershipImportAction(ctx, record, action)
			if err != nil {
				return plannedMembershipImport{}, err
			}
			plan.Actions = append(plan.Actions, prepared)
		} else if current.Role != row.Role {
			action := membershipImportAction{Kind: "role_change", EventType: "MembershipRoleChanged", Email: row.Email, UserID: current.UserID, MembershipID: current.ID, PreviousRole: current.Role, Role: row.Role, PreviousStatus: current.Status, Status: current.Status, RowNumber: row.Number, PreviousVersion: current.Version, Version: current.Version + 1}
			prepared, err := service.prepareMembershipImportAction(ctx, record, action)
			if err != nil {
				return plannedMembershipImport{}, err
			}
			plan.Actions = append(plan.Actions, prepared)
		}
	}
	plan.DeactivateMissingApplied = record.Mode == "deactivate_missing" && plan.RejectedRows == 0
	if plan.DeactivateMissingApplied {
		emails := make([]string, 0, len(snapshot.Memberships))
		for email := range snapshot.Memberships {
			emails = append(emails, email)
		}
		sort.Strings(emails)
		for _, email := range emails {
			current := snapshot.Memberships[email]
			if current.Status != "active" || current.Role == "owner" {
				continue
			}
			if _, listed := roster[email]; listed {
				continue
			}
			action := membershipImportAction{Kind: "deactivate", EventType: "MembershipDeactivated", Email: email, UserID: current.UserID, MembershipID: current.ID, PreviousRole: current.Role, Role: current.Role, PreviousStatus: current.Status, Status: "suspended", PreviousVersion: current.Version, Version: current.Version + 1}
			prepared, err := service.prepareMembershipImportAction(ctx, record, action)
			if err != nil {
				return plannedMembershipImport{}, err
			}
			for _, session := range snapshot.Sessions[current.UserID] {
				revocation, err := service.prepareSessionRevocation(ctx, session, "membership_import_deactivated")
				if err != nil {
					return plannedMembershipImport{}, err
				}
				prepared.Sessions = append(prepared.Sessions, revocation)
			}
			plan.Actions = append(plan.Actions, prepared)
			plan.DeactivatedRows++
		}
	}
	return plan, nil
}

func (service AuthService) prepareMembershipImportAction(ctx context.Context, record membershipImportRecord, action membershipImportAction) (membershipImportAction, error) {
	for _, target := range []*string{&action.SecurityEventID, &action.EventID, &action.OutboxID, &action.CommandID} {
		id, err := service.newID()
		if err != nil {
			return membershipImportAction{}, apiDependencyUnavailable()
		}
		*target = id
	}
	payloadValue := map[string]any{"subject_id": action.MembershipID, "subject_version": action.Version}
	if action.EventType == "MembershipRoleChanged" {
		payloadValue["previous_state"] = action.PreviousRole
		payloadValue["new_state"] = action.Role
		payloadValue["reason_code"] = "membership_import"
	}
	manifest, err := service.putJSON(ctx, payload.Descriptor{TenantID: record.TenantID, ObjectID: action.EventID, Class: "event-payload", ContentType: "application/json"}, payloadValue)
	if err != nil {
		return membershipImportAction{}, apiDependencyUnavailable()
	}
	action.EventPayload = manifest
	return action, nil
}

func (service AuthService) commitMembershipImport(ctx context.Context, record membershipImportRecord, snapshot membershipImportSnapshot, plan plannedMembershipImport, completion importCompletionPrepared, inbox *importInboxCompletion) (ImportProcessResult, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ImportProcessResult{}, apiDependencyUnavailable()
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, record.TenantID); err != nil {
		return ImportProcessResult{}, apiDependencyUnavailable()
	}
	now := service.now()
	if inbox != nil {
		if err = inbox.Store.LockClaimTx(ctx, tx, inbox.Claim, now); err != nil {
			return ImportProcessResult{}, err
		}
	}
	var status string
	var version uint64
	var accepted, rejected, deactivated int
	if err = tx.QueryRow(ctx, `SELECT status,version,accepted_rows,rejected_rows,deactivated_rows FROM identity.membership_imports WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, record.ID, record.TenantID).Scan(&status, &version, &accepted, &rejected, &deactivated); err != nil {
		return ImportProcessResult{}, apiDependencyUnavailable()
	}
	if status == "completed" {
		if inbox != nil {
			if err = inbox.Store.CompleteTx(ctx, tx, inbox.Claim, service.now()); err != nil {
				return ImportProcessResult{}, err
			}
		}
		if err = tx.Commit(ctx); err != nil {
			return ImportProcessResult{}, apiDependencyUnavailable()
		}
		return ImportProcessResult{Status: status, Version: version, AcceptedRows: accepted, RejectedRows: rejected, DeactivatedRows: deactivated}, nil
	}
	if status != "processing" || version != record.Version {
		return ImportProcessResult{}, ErrImportFailed
	}
	var tenantActive bool
	if err = tx.QueryRow(ctx, `SELECT identity.lock_active_tenant($1)`, record.TenantID).Scan(&tenantActive); err != nil || !tenantActive {
		return ImportProcessResult{}, ErrImportFailed
	}
	currentMemberships, err := lockMembershipImportSnapshot(ctx, tx, record.TenantID)
	if err != nil || !sameMembershipImportSnapshot(snapshot.Memberships, currentMemberships) {
		return ImportProcessResult{}, api.ErrStateConflict
	}
	actor, _ := json.Marshal(map[string]string{"kind": "user", "id": record.InitiatedBy})
	for _, action := range plan.Actions {
		if err = service.applyMembershipImportAction(ctx, tx, record, action, actor, now); err != nil {
			return ImportProcessResult{}, err
		}
	}
	nextVersion := record.Version + 1
	if _, err = tx.Exec(ctx, `UPDATE identity.membership_imports SET status='completed',version=$1,accepted_rows=$2,rejected_rows=$3,deactivated_rows=$4,completed_at=$5,updated_at=$5 WHERE id=$6 AND version=$7 AND status='processing'`, nextVersion, plan.AcceptedRows, plan.RejectedRows, plan.DeactivatedRows, now, record.ID, record.Version); err != nil {
		return ImportProcessResult{}, err
	}
	details, _ := json.Marshal(map[string]any{"accepted_rows": plan.AcceptedRows, "rejected_rows": plan.RejectedRows, "deactivated_rows": plan.DeactivatedRows, "deactivate_missing_applied": plan.DeactivateMissingApplied, "content_hash": record.ContentHash})
	if _, err = tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,actor_user_id,event_type,request_id,details,occurred_at) VALUES ($1,$2,$3,'membership_import_completed',$4,$5,$6)`, completion.SecurityEventID, record.TenantID, record.InitiatedBy, "import:"+record.ID, details, now); err != nil {
		return ImportProcessResult{}, err
	}
	if _, err = service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: completion.EventID, TenantID: record.TenantID, UserID: record.InitiatedBy, EventType: "MembershipImportCompleted", SchemaVersion: 1, AggregateKind: "membership_import", AggregateID: record.ID, AggregateVersion: nextVersion, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: completion.SecurityEventID, PayloadRef: completion.EventPayload.Ref, PayloadHash: completion.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: completion.OutboxID, CommandID: completion.CommandID, CommandType: "events.publish", PayloadRef: completion.EventPayload.Ref, PayloadHash: completion.EventPayload.Hash}}}); err != nil {
		return ImportProcessResult{}, err
	}
	if inbox != nil {
		if err = inbox.Store.CompleteTx(ctx, tx, inbox.Claim, now); err != nil {
			return ImportProcessResult{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return ImportProcessResult{}, apiDependencyUnavailable()
	}
	return ImportProcessResult{Status: "completed", Version: nextVersion, AcceptedRows: plan.AcceptedRows, RejectedRows: plan.RejectedRows, DeactivatedRows: plan.DeactivatedRows}, nil
}

func lockMembershipImportSnapshot(ctx context.Context, tx pgx.Tx, tenantID string) (map[string]membershipRow, error) {
	rows, err := tx.Query(ctx, `SELECT m.id::text,m.tenant_id::text,m.user_id::text,u.normalized_email,m.role,m.status,m.version,m.joined_at,m.updated_at,m.deactivated_at,t.kind FROM identity.memberships m JOIN identity.users u ON u.id=m.user_id JOIN identity.tenants t ON t.id=m.tenant_id WHERE m.tenant_id=$1 ORDER BY m.id FOR UPDATE OF m`, tenantID)
	if err != nil {
		return nil, apiDependencyUnavailable()
	}
	defer rows.Close()
	result := map[string]membershipRow{}
	for rows.Next() {
		var row membershipRow
		if err = rows.Scan(&row.ID, &row.TenantID, &row.UserID, &row.Email, &row.Role, &row.Status, &row.Version, &row.JoinedAt, &row.UpdatedAt, &row.DeactivatedAt, &row.TenantKind); err != nil {
			return nil, apiDependencyUnavailable()
		}
		result[row.Email] = row
	}
	if rows.Err() != nil {
		return nil, apiDependencyUnavailable()
	}
	return result, nil
}

func sameMembershipImportSnapshot(expected, actual map[string]membershipRow) bool {
	if len(expected) != len(actual) {
		return false
	}
	for email, left := range expected {
		right, ok := actual[email]
		if !ok || left.ID != right.ID || left.UserID != right.UserID || left.Role != right.Role || left.Status != right.Status || left.Version != right.Version {
			return false
		}
	}
	return true
}

func (service AuthService) applyMembershipImportAction(ctx context.Context, tx pgx.Tx, record membershipImportRecord, action membershipImportAction, actor json.RawMessage, now time.Time) error {
	switch action.Kind {
	case "create":
		var userStatus string
		var verified bool
		if err := tx.QueryRow(ctx, `SELECT status,email_verified_at IS NOT NULL FROM identity.users WHERE id=$1 AND normalized_email=$2 FOR UPDATE`, action.UserID, action.Email).Scan(&userStatus, &verified); err != nil || userStatus != "active" || !verified {
			return api.ErrStateConflict
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM identity.memberships WHERE tenant_id=$1 AND user_id=$2)`, record.TenantID, action.UserID).Scan(&exists); err != nil || exists {
			return api.ErrStateConflict
		}
		if _, err := tx.Exec(ctx, `INSERT INTO identity.memberships (id,tenant_id,user_id,role,status,joined_at) VALUES ($1,$2,$3,$4,'active',$5)`, action.MembershipID, record.TenantID, action.UserID, action.Role, now); err != nil {
			return err
		}
	case "reactivate", "role_change":
		var userStatus string
		var verified bool
		if err := tx.QueryRow(ctx, `SELECT status,email_verified_at IS NOT NULL FROM identity.users WHERE id=$1 AND normalized_email=$2 FOR UPDATE`, action.UserID, action.Email).Scan(&userStatus, &verified); err != nil || userStatus != "active" || !verified {
			return api.ErrStateConflict
		}
		deactivatedAt := any(nil)
		if action.Status != "active" {
			deactivatedAt = now
		}
		tag, err := tx.Exec(ctx, `UPDATE identity.memberships SET role=$1,status=$2,deactivated_at=$3,version=version+1,updated_at=$4 WHERE id=$5 AND tenant_id=$6 AND version=$7 AND role=$8 AND status=$9`, action.Role, action.Status, deactivatedAt, now, action.MembershipID, record.TenantID, action.PreviousVersion, action.PreviousRole, action.PreviousStatus)
		if err != nil || tag.RowsAffected() != 1 {
			return api.ErrStateConflict
		}
	case "deactivate":
		actualSessions, sessionErr := service.lockActiveOwnedSessions(ctx, tx, action.UserID)
		if sessionErr != nil || !sameSessionSnapshot(action.Sessions, sessionsForTenant(actualSessions, record.TenantID)) {
			return api.ErrStateConflict
		}
		tag, err := tx.Exec(ctx, `UPDATE identity.memberships SET status='suspended',deactivated_at=$1,version=version+1,updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND version=$4 AND role=$5 AND status='active'`, now, action.MembershipID, record.TenantID, action.PreviousVersion, action.PreviousRole)
		if err != nil || tag.RowsAffected() != 1 {
			return api.ErrStateConflict
		}
	default:
		return ErrImportInvalid
	}
	details, _ := json.Marshal(map[string]any{"import_id": record.ID, "row_number": action.RowNumber, "action": action.Kind, "previous_role": action.PreviousRole, "role": action.Role, "previous_status": action.PreviousStatus, "status": action.Status})
	if _, err := tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,subject_user_id,actor_user_id,event_type,request_id,details,occurred_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, action.SecurityEventID, record.TenantID, action.UserID, record.InitiatedBy, "membership_"+action.Kind, "import:"+record.ID, details, now); err != nil {
		return err
	}
	if _, err := service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: action.EventID, TenantID: record.TenantID, UserID: action.UserID, EventType: action.EventType, SchemaVersion: 1, AggregateKind: "membership", AggregateID: action.MembershipID, AggregateVersion: action.Version, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: action.SecurityEventID, PayloadRef: action.EventPayload.Ref, PayloadHash: action.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: action.OutboxID, CommandID: action.CommandID, CommandType: "events.publish", PayloadRef: action.EventPayload.Ref, PayloadHash: action.EventPayload.Hash}}}); err != nil {
		return err
	}
	for _, session := range action.Sessions {
		tag, err := tx.Exec(ctx, `UPDATE identity.sessions SET revoked_at=$1,version=version+1,updated_at=$1 WHERE id=$2 AND user_id=$3 AND active_tenant_id=$4 AND version=$5 AND revoked_at IS NULL`, now, session.Session.ID, action.UserID, record.TenantID, session.Session.Version)
		if err != nil || tag.RowsAffected() != 1 {
			return api.ErrStateConflict
		}
		if _, err = service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: session.EventID, TenantID: record.TenantID, UserID: action.UserID, EventType: "SessionRevoked", SchemaVersion: 1, AggregateKind: "session", AggregateID: session.Session.ID, AggregateVersion: session.NextVersion, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: action.SecurityEventID, PayloadRef: session.EventPayload.Ref, PayloadHash: session.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: session.OutboxID, CommandID: session.CommandID, CommandType: "events.publish", PayloadRef: session.EventPayload.Ref, PayloadHash: session.EventPayload.Hash}}}); err != nil {
			return err
		}
	}
	return nil
}

func (service AuthService) failMembershipImport(ctx context.Context, record membershipImportRecord, reasonCode string) error {
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
	tag, err := tx.Exec(ctx, `UPDATE identity.membership_imports SET status='failed',updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND status='processing'`, service.now(), record.ID, record.TenantID)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	details, _ := json.Marshal(map[string]string{"reason_code": reasonCode})
	if _, err = tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,actor_user_id,event_type,request_id,details,occurred_at) VALUES ($1,$2,$3,'membership_import_failed',$4,$5,$6)`, securityEventID, record.TenantID, record.InitiatedBy, "import:"+record.ID, details, service.now()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func parseMembershipCSV(contents []byte) ([]membershipCSVRow, int, error) {
	if !utf8.Valid(contents) {
		return nil, 0, ErrImportInvalid
	}
	reader := csv.NewReader(strings.NewReader(string(contents)))
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil || len(header) != 2 || header[0] != "email" || header[1] != "role" {
		return nil, 0, ErrImportInvalid
	}
	seen := map[string]struct{}{}
	rows := make([]membershipCSVRow, 0)
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
		if len(record) != 2 {
			rejected++
			continue
		}
		email, normalizeErr := identityemail.Normalize(record[0])
		_, duplicate := seen[email]
		if normalizeErr != nil || !validMembershipImportRole(record[1]) || duplicate {
			rejected++
			continue
		}
		seen[email] = struct{}{}
		rows = append(rows, membershipCSVRow{Number: number, Email: email, Role: record[1]})
	}
	return rows, rejected, nil
}

func validMembershipImportRole(value string) bool {
	return value == "owner" || validInvitationRole(value)
}
