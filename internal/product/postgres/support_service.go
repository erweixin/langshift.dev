package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/idempotency"
	idempotencypostgres "github.com/langshift/lites/internal/idempotency/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/cursor"
	"github.com/langshift/lites/internal/platform/ids"
	productapi "github.com/langshift/lites/internal/product/api"
)

const (
	supportCreateOperation = "support.cases.create.v2"
	supportReplyOperation  = "support.cases.reply.v2"
	supportSubjectClass    = "product-support-case-subject"
	supportMessageClass    = "product-support-case-message"
	supportResponseClass   = "product-support-idempotency"
)

var errSupportPermission = errors.New("support case permission denied")

type SupportService struct {
	Pool                                             *pgxpool.Pool
	Appender                                         eventpostgres.Appender
	Payloads                                         payload.Store
	IDKey, IdempotencyKeyPepper, RequestDigestPepper []byte
	CursorKey                                        []byte
	StoreEpoch                                       string
	IdempotencyTTL                                   time.Duration
	Now                                              func() time.Time
}

type supportCursor struct {
	TenantID, UserID, ID string
	TenantWide           bool
	UpdatedAt            time.Time
}

func (service SupportService) Create(ctx context.Context, command productapi.CreateSupportCaseCommand) (productapi.SupportCase, error) {
	if !service.valid() || !validMissionMetadata(command.CommandMetadata) || uuid.Validate(command.MembershipID) != nil || !validSupportServiceCategory(command.Category) || !validSupportServicePriority(command.Priority) || !validSupportServiceText(command.Subject, 1, 200) || !validSupportServiceText(command.Body, 1, 65536) {
		return productapi.SupportCase{}, productapi.ErrValidation
	}
	canonical, _ := json.Marshal(command)
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return productapi.SupportCase{}, service.mapError(err)
	}
	recordID, err := service.recordID(command.CommandMetadata, supportCreateOperation)
	if err != nil {
		return productapi.SupportCase{}, service.mapError(err)
	}
	caseID, _ := ids.DeterministicUUID(service.IDKey, "support-case", recordID)
	messageID, _ := ids.DeterministicUUID(service.IDKey, "support-case-initial-message", recordID)
	responseDescriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: supportResponseClass, ContentType: "application/json"}
	input := idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: supportCreateOperation}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	if response, found, loadErr := executor.LoadCompleted(ctx, input); loadErr != nil {
		return productapi.SupportCase{}, service.mapError(loadErr)
	} else if found {
		result, readErr := service.readResponse(ctx, responseDescriptor, response)
		result.Replayed = readErr == nil
		return result, service.mapError(readErr)
	}
	subjectManifest, err := service.Payloads.Put(ctx, service.subjectDescriptor(command.TenantID, caseID), []byte(command.Subject))
	if err != nil {
		return productapi.SupportCase{}, service.mapError(err)
	}
	messageManifest, err := service.Payloads.Put(ctx, service.messageDescriptor(command.TenantID, messageID), []byte(command.Body))
	if err != nil {
		return productapi.SupportCase{}, service.mapError(err)
	}
	eventID, _ := ids.DeterministicUUID(service.IDKey, "support-case-created:event", recordID)
	eventManifest, err := service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: eventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"case_id": caseID, "message_id": messageID, "category": command.Category, "priority": command.Priority, "subject_ref": subjectManifest.Ref, "subject_hash": subjectManifest.Hash})
	if err != nil {
		return productapi.SupportCase{}, service.mapError(err)
	}
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if err := service.setTenant(ctx, tx, command.TenantID); err != nil {
			return idempotency.Response{}, err
		}
		if err := service.requireMembership(ctx, tx, command.TenantID, command.UserID, command.MembershipID, false); err != nil {
			return idempotency.Response{}, err
		}
		now := service.now()
		var tier string
		var responseMinutes int
		var resolutionMinutes *int
		if err := tx.QueryRow(ctx, `SELECT support_tier,response_minutes,resolution_minutes FROM product.resolve_support_sla($1,$2)`, command.TenantID, command.Priority).Scan(&tier, &responseMinutes, &resolutionMinutes); err != nil {
			return idempotency.Response{}, err
		}
		responseDue := now.Add(time.Duration(responseMinutes) * time.Minute)
		var resolutionDue *time.Time
		if resolutionMinutes != nil {
			value := now.Add(time.Duration(*resolutionMinutes) * time.Minute)
			resolutionDue = &value
		}
		reference := supportReference(now, caseID)
		if _, err := tx.Exec(ctx, `INSERT INTO product.support_cases(id,tenant_id,requester_user_id,requester_membership_id,reference,version,category,priority,status,subject_ref,subject_hash,support_tier,response_due_at,resolution_due_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,1,$6,$7,'open',$8,$9,$10,$11,$12,$13,$13)`, caseID, command.TenantID, command.UserID, command.MembershipID, reference, command.Category, command.Priority, subjectManifest.Ref, subjectManifest.Hash, tier, responseDue, resolutionDue, now); err != nil {
			return idempotency.Response{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO product.support_case_messages(id,tenant_id,case_id,author_user_id,author_membership_id,author_kind,body_ref,body_hash,created_at) VALUES($1,$2,$3,$4,$5,'customer',$6,$7,$8)`, messageID, command.TenantID, caseID, command.UserID, command.MembershipID, messageManifest.Ref, messageManifest.Hash, now); err != nil {
			return idempotency.Response{}, err
		}
		outboxID, _ := ids.DeterministicUUID(service.IDKey, "support-case-created:outbox", recordID)
		publishID, _ := ids.DeterministicUUID(service.IDKey, "support-case-created:publish", recordID)
		if _, err := service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: command.TenantID, UserID: command.UserID, EventType: "SupportCaseCreated", SchemaVersion: 1, AggregateKind: "support_case", AggregateID: caseID, AggregateVersion: 1, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: missionActor(command.UserID, command.SessionID), CorrelationID: command.RequestID, PayloadRef: eventManifest.Ref, PayloadHash: eventManifest.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishID, CommandType: "events.publish", PayloadRef: eventManifest.Ref, PayloadHash: eventManifest.Hash}}}); err != nil {
			return idempotency.Response{}, err
		}
		result := productapi.SupportCase{ID: caseID, Reference: reference, RequesterUserID: command.UserID, Category: command.Category, Priority: command.Priority, Status: "open", Subject: command.Subject, SupportTier: tier, Version: 1, ResponseDueAt: &responseDue, ResolutionDueAt: resolutionDue, CreatedAt: now, UpdatedAt: now, Messages: []productapi.SupportMessage{{ID: messageID, AuthorUserID: command.UserID, AuthorKind: "customer", Body: command.Body, CreatedAt: now}}}
		manifest, err := service.putJSON(ctx, responseDescriptor, result)
		if err != nil {
			return idempotency.Response{}, err
		}
		return idempotency.Response{Status: http.StatusCreated, ContentType: "application/vnd.lites.support-case.v2+json", PayloadRef: manifest.Ref, Hash: manifest.Hash, ResourceVersion: 1}, nil
	})
	if err != nil {
		return productapi.SupportCase{}, service.mapError(err)
	}
	result, err := service.readResponse(ctx, responseDescriptor, response)
	result.Replayed = replayed && err == nil
	return result, service.mapError(err)
}

func (service SupportService) Reply(ctx context.Context, command productapi.ReplySupportCaseCommand) (productapi.SupportCase, error) {
	if !service.valid() || !validMissionMetadata(command.CommandMetadata) || uuid.Validate(command.MembershipID) != nil || uuid.Validate(command.CaseID) != nil || command.ExpectedCaseVersion < 1 || !validSupportServiceText(command.Body, 1, 65536) {
		return productapi.SupportCase{}, productapi.ErrValidation
	}
	canonical, _ := json.Marshal(command)
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return productapi.SupportCase{}, service.mapError(err)
	}
	recordID, err := service.recordID(command.CommandMetadata, supportReplyOperation)
	if err != nil {
		return productapi.SupportCase{}, service.mapError(err)
	}
	messageID, _ := ids.DeterministicUUID(service.IDKey, "support-case-reply", recordID)
	responseDescriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: supportResponseClass, ContentType: "application/json"}
	input := idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: supportReplyOperation}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	if response, found, loadErr := executor.LoadCompleted(ctx, input); loadErr != nil {
		return productapi.SupportCase{}, service.mapError(loadErr)
	} else if found {
		result, readErr := service.readResponse(ctx, responseDescriptor, response)
		result.Replayed = readErr == nil
		return result, service.mapError(readErr)
	}
	messageManifest, err := service.Payloads.Put(ctx, service.messageDescriptor(command.TenantID, messageID), []byte(command.Body))
	if err != nil {
		return productapi.SupportCase{}, service.mapError(err)
	}
	eventID, _ := ids.DeterministicUUID(service.IDKey, "support-case-replied:event", recordID)
	eventManifest, err := service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: eventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"case_id": command.CaseID, "message_id": messageID, "target_version": command.ExpectedCaseVersion + 1})
	if err != nil {
		return productapi.SupportCase{}, service.mapError(err)
	}
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if err := service.setTenant(ctx, tx, command.TenantID); err != nil {
			return idempotency.Response{}, err
		}
		if err := service.requireMembership(ctx, tx, command.TenantID, command.UserID, command.MembershipID, command.TenantWide); err != nil {
			return idempotency.Response{}, err
		}
		var requesterID, status string
		var current uint64
		if err := tx.QueryRow(ctx, `SELECT requester_user_id::text,status,version FROM product.support_cases WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, command.CaseID).Scan(&requesterID, &status, &current); err != nil {
			return idempotency.Response{}, err
		}
		if !command.TenantWide && requesterID != command.UserID {
			return idempotency.Response{}, errSupportPermission
		}
		if current != command.ExpectedCaseVersion || status == "closed" {
			return idempotency.Response{}, ErrRouteConflict
		}
		now, next := service.now(), current+1
		nextStatus := "waiting_on_support"
		if status == "resolved" {
			nextStatus = "open"
		}
		tag, err := tx.Exec(ctx, `UPDATE product.support_cases SET version=$1,status=$2,resolved_at=NULL,updated_at=$3 WHERE tenant_id=$4 AND id=$5 AND version=$6`, next, nextStatus, now, command.TenantID, command.CaseID, current)
		if err != nil || tag.RowsAffected() != 1 {
			if err == nil {
				err = ErrRouteConflict
			}
			return idempotency.Response{}, err
		}
		authorKind := "customer"
		if command.TenantWide && requesterID != command.UserID {
			authorKind = "tenant_admin"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO product.support_case_messages(id,tenant_id,case_id,author_user_id,author_membership_id,author_kind,body_ref,body_hash,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, messageID, command.TenantID, command.CaseID, command.UserID, command.MembershipID, authorKind, messageManifest.Ref, messageManifest.Hash, now); err != nil {
			return idempotency.Response{}, err
		}
		outboxID, _ := ids.DeterministicUUID(service.IDKey, "support-case-replied:outbox", recordID)
		publishID, _ := ids.DeterministicUUID(service.IDKey, "support-case-replied:publish", recordID)
		if _, err := service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: command.TenantID, UserID: command.UserID, EventType: "SupportCaseReplied", SchemaVersion: 1, AggregateKind: "support_case", AggregateID: command.CaseID, AggregateVersion: next, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: missionActor(command.UserID, command.SessionID), CorrelationID: command.RequestID, PayloadRef: eventManifest.Ref, PayloadHash: eventManifest.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishID, CommandType: "events.publish", PayloadRef: eventManifest.Ref, PayloadHash: eventManifest.Hash}}}); err != nil {
			return idempotency.Response{}, err
		}
		result, err := service.getInTransaction(ctx, tx, productapi.SupportQuery{TenantID: command.TenantID, UserID: command.UserID, MembershipID: command.MembershipID, TenantWide: command.TenantWide}, command.CaseID)
		if err != nil {
			return idempotency.Response{}, err
		}
		manifest, err := service.putJSON(ctx, responseDescriptor, result)
		if err != nil {
			return idempotency.Response{}, err
		}
		return idempotency.Response{Status: http.StatusOK, ContentType: "application/vnd.lites.support-case.v2+json", PayloadRef: manifest.Ref, Hash: manifest.Hash, ResourceVersion: next}, nil
	})
	if err != nil {
		return productapi.SupportCase{}, service.mapError(err)
	}
	result, err := service.readResponse(ctx, responseDescriptor, response)
	result.Replayed = replayed && err == nil
	return result, service.mapError(err)
}

func (service SupportService) List(ctx context.Context, query productapi.SupportQuery) (productapi.SupportPage, error) {
	if !service.valid() || !validSupportQuery(query) {
		return productapi.SupportPage{}, productapi.ErrValidation
	}
	var position *supportCursor
	if query.Cursor != "" {
		var decoded supportCursor
		if err := (cursor.Codec{Key: service.CursorKey}).Decode(query.Cursor, service.cursorScope(query), &decoded); err != nil || decoded.TenantID != query.TenantID || decoded.UserID != query.UserID || decoded.TenantWide != query.TenantWide || uuid.Validate(decoded.ID) != nil || decoded.UpdatedAt.IsZero() {
			return productapi.SupportPage{}, productapi.ErrValidation
		}
		position = &decoded
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return productapi.SupportPage{}, service.mapError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = service.setTenant(ctx, tx, query.TenantID); err != nil {
		return productapi.SupportPage{}, service.mapError(err)
	}
	if err = service.requireMembership(ctx, tx, query.TenantID, query.UserID, query.MembershipID, query.TenantWide); err != nil {
		return productapi.SupportPage{}, service.mapError(err)
	}
	args := []any{query.TenantID}
	filter := ""
	if !query.TenantWide {
		args = append(args, query.UserID)
		filter += fmt.Sprintf(" AND requester_user_id=$%d", len(args))
	}
	if position != nil {
		args = append(args, position.UpdatedAt, position.ID)
		filter += fmt.Sprintf(" AND (updated_at,id)<($%d,$%d)", len(args)-1, len(args))
	}
	args = append(args, query.Limit+1)
	rows, err := tx.Query(ctx, `SELECT id::text,reference,requester_user_id::text,category,priority,status,subject_ref,subject_hash,support_tier,version,response_due_at,resolution_due_at,first_responded_at,resolved_at,created_at,updated_at FROM product.support_cases WHERE tenant_id=$1`+filter+` ORDER BY updated_at DESC,id DESC LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return productapi.SupportPage{}, service.mapError(err)
	}
	defer rows.Close()
	items := make([]productapi.SupportCase, 0, query.Limit+1)
	for rows.Next() {
		item, subjectRef, subjectHash, scanErr := scanSupportCase(rows)
		if scanErr != nil {
			return productapi.SupportPage{}, service.mapError(scanErr)
		}
		item.Subject, scanErr = service.readText(ctx, service.subjectDescriptor(query.TenantID, item.ID), subjectRef, subjectHash)
		if scanErr != nil {
			return productapi.SupportPage{}, service.mapError(scanErr)
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return productapi.SupportPage{}, service.mapError(err)
	}
	result := productapi.SupportPage{Items: items}
	if len(items) > query.Limit {
		last := items[query.Limit-1]
		result.Items = items[:query.Limit]
		result.NextCursor, err = (cursor.Codec{Key: service.CursorKey}).Encode(service.cursorScope(query), supportCursor{TenantID: query.TenantID, UserID: query.UserID, TenantWide: query.TenantWide, UpdatedAt: last.UpdatedAt, ID: last.ID})
		if err != nil {
			return productapi.SupportPage{}, productapi.ErrDependencyUnavailable
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return productapi.SupportPage{}, service.mapError(err)
	}
	return result, nil
}

func (service SupportService) Get(ctx context.Context, query productapi.SupportQuery, caseID string) (productapi.SupportCase, error) {
	if !service.valid() || !validSupportQuery(query) || query.Cursor != "" || uuid.Validate(caseID) != nil {
		return productapi.SupportCase{}, productapi.ErrValidation
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return productapi.SupportCase{}, service.mapError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = service.setTenant(ctx, tx, query.TenantID); err != nil {
		return productapi.SupportCase{}, service.mapError(err)
	}
	if err = service.requireMembership(ctx, tx, query.TenantID, query.UserID, query.MembershipID, query.TenantWide); err != nil {
		return productapi.SupportCase{}, service.mapError(err)
	}
	result, err := service.getInTransaction(ctx, tx, query, caseID)
	if err != nil {
		return productapi.SupportCase{}, service.mapError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return productapi.SupportCase{}, service.mapError(err)
	}
	return result, nil
}

func (service SupportService) getInTransaction(ctx context.Context, tx pgx.Tx, query productapi.SupportQuery, caseID string) (productapi.SupportCase, error) {
	filter := ""
	args := []any{query.TenantID, caseID}
	if !query.TenantWide {
		args = append(args, query.UserID)
		filter = " AND requester_user_id=$3"
	}
	row := tx.QueryRow(ctx, `SELECT id::text,reference,requester_user_id::text,category,priority,status,subject_ref,subject_hash,support_tier,version,response_due_at,resolution_due_at,first_responded_at,resolved_at,created_at,updated_at FROM product.support_cases WHERE tenant_id=$1 AND id=$2`+filter, args...)
	result, subjectRef, subjectHash, err := scanSupportCase(row)
	if err != nil {
		return productapi.SupportCase{}, err
	}
	result.Subject, err = service.readText(ctx, service.subjectDescriptor(query.TenantID, caseID), subjectRef, subjectHash)
	if err != nil {
		return productapi.SupportCase{}, err
	}
	rows, err := tx.Query(ctx, `SELECT id::text,author_user_id::text,author_kind,body_ref,body_hash,created_at FROM product.support_case_messages WHERE tenant_id=$1 AND case_id=$2 ORDER BY created_at,id`, query.TenantID, caseID)
	if err != nil {
		return productapi.SupportCase{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var message productapi.SupportMessage
		var ref, hash string
		if err = rows.Scan(&message.ID, &message.AuthorUserID, &message.AuthorKind, &ref, &hash, &message.CreatedAt); err != nil {
			return productapi.SupportCase{}, err
		}
		message.Body, err = service.readText(ctx, service.messageDescriptor(query.TenantID, message.ID), ref, hash)
		if err != nil {
			return productapi.SupportCase{}, err
		}
		result.Messages = append(result.Messages, message)
	}
	return result, rows.Err()
}

type supportScanner interface{ Scan(...any) error }

func scanSupportCase(row supportScanner) (productapi.SupportCase, string, string, error) {
	var item productapi.SupportCase
	var subjectRef, subjectHash string
	err := row.Scan(&item.ID, &item.Reference, &item.RequesterUserID, &item.Category, &item.Priority, &item.Status, &subjectRef, &subjectHash, &item.SupportTier, &item.Version, &item.ResponseDueAt, &item.ResolutionDueAt, &item.FirstRespondedAt, &item.ResolvedAt, &item.CreatedAt, &item.UpdatedAt)
	return item, subjectRef, subjectHash, err
}

func (service SupportService) requireMembership(ctx context.Context, tx pgx.Tx, tenantID, userID, membershipID string, tenantWide bool) error {
	var role string
	if err := tx.QueryRow(ctx, `SELECT role FROM identity.memberships WHERE tenant_id=$1 AND user_id=$2 AND id=$3 AND status='active'`, tenantID, userID, membershipID).Scan(&role); errors.Is(err, pgx.ErrNoRows) {
		return errSupportPermission
	} else if err != nil {
		return err
	}
	if tenantWide && role != "owner" && role != "admin" && role != "contract_admin" {
		return errSupportPermission
	}
	return nil
}

func (service SupportService) setTenant(ctx context.Context, tx pgx.Tx, tenantID string) error {
	_, err := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID)
	return err
}

func (service SupportService) recordID(metadata productapi.CommandMetadata, operation string) (string, error) {
	return ids.DeterministicUUID(service.IDKey, "product-idempotency:"+operation, metadata.TenantID+"\x00"+metadata.UserID+"\x00"+metadata.IdempotencyKey)
}

func (service SupportService) putJSON(ctx context.Context, descriptor payload.Descriptor, value any) (payload.Manifest, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return payload.Manifest{}, err
	}
	return service.Payloads.Put(ctx, descriptor, encoded)
}

func (service SupportService) readResponse(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (productapi.SupportCase, error) {
	encoded, err := service.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: response.PayloadRef, Hash: response.Hash})
	if err != nil {
		return productapi.SupportCase{}, err
	}
	var result productapi.SupportCase
	if err = json.Unmarshal(encoded, &result); err != nil {
		return productapi.SupportCase{}, err
	}
	return result, nil
}

func (service SupportService) readText(ctx context.Context, descriptor payload.Descriptor, ref, hash string) (string, error) {
	encoded, err := service.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: ref, Hash: hash})
	return string(encoded), err
}

func (service SupportService) subjectDescriptor(tenantID, caseID string) payload.Descriptor {
	return payload.Descriptor{TenantID: tenantID, ObjectID: caseID, Class: supportSubjectClass, ContentType: "text/plain; charset=utf-8"}
}

func (service SupportService) messageDescriptor(tenantID, messageID string) payload.Descriptor {
	return payload.Descriptor{TenantID: tenantID, ObjectID: messageID, Class: supportMessageClass, ContentType: "text/plain; charset=utf-8"}
}

func (service SupportService) cursorScope(query productapi.SupportQuery) string {
	return fmt.Sprintf("support-cases:%s:%s:%t", query.TenantID, query.UserID, query.TenantWide)
}

func (service SupportService) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func (service SupportService) valid() bool {
	return service.Pool != nil && service.Payloads != nil && len(service.IDKey) >= 32 && len(service.IdempotencyKeyPepper) >= 32 && len(service.RequestDigestPepper) >= 32 && len(service.CursorKey) >= 32 && service.StoreEpoch != "" && service.IdempotencyTTL > 0
}

func (service SupportService) mapError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, errSupportPermission):
		return productapi.ErrPermissionDenied
	case errors.Is(err, pgx.ErrNoRows):
		return productapi.ErrResourceNotFound
	case errors.Is(err, idempotency.ErrKeyConflict), errors.Is(err, idempotency.ErrInProgress):
		return productapi.ErrIdempotencyConflict
	case errors.Is(err, ErrRouteConflict):
		return productapi.ErrStateConflict
	case errors.As(err, &pgErr) && pgErr.Code == "23505":
		return productapi.ErrStateConflict
	case errors.As(err, &pgErr) && (pgErr.Code == "23503" || pgErr.Code == "23514" || pgErr.Code == "22023"):
		return productapi.ErrValidation
	default:
		return errors.Join(productapi.ErrDependencyUnavailable, err)
	}
}

func supportReference(now time.Time, caseID string) string {
	compact := strings.ToUpper(strings.ReplaceAll(caseID, "-", ""))
	return "LTS-" + now.UTC().Format("20060102") + "-" + compact[:8]
}

func validSupportQuery(query productapi.SupportQuery) bool {
	return uuid.Validate(query.TenantID) == nil && uuid.Validate(query.UserID) == nil && uuid.Validate(query.MembershipID) == nil && query.Limit >= 1 && query.Limit <= 100
}

func validSupportServiceCategory(value string) bool {
	return value == "product" || value == "security" || value == "privacy" || value == "contract" || value == "availability"
}

func validSupportServicePriority(value string) bool {
	return value == "low" || value == "normal" || value == "high" || value == "urgent"
}

func validSupportServiceText(value string, minimum, maximum int) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed == value && len(value) >= minimum && len(value) <= maximum
}
