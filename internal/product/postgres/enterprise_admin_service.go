package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
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
	"github.com/langshift/lites/internal/platform/ids"
	productapi "github.com/langshift/lites/internal/product/api"
)

const (
	programCreateOperation   = "admin.programs.create.v2"
	programUpdateOperation   = "admin.programs.update.v2"
	cohortCreateOperation    = "admin.cohorts.create.v2"
	cohortEnrollOperation    = "admin.cohorts.enroll.v2"
	cohortUnenrollOperation  = "admin.cohorts.unenroll.v2"
	rolePackPublishOperation = "admin.role-packs.publish.v2"
	taskPackPublishOperation = "admin.task-packs.publish.v2"
)

type EnterpriseAdminService struct {
	Pool                                             *pgxpool.Pool
	Appender                                         eventpostgres.Appender
	Payloads                                         payload.Store
	IDKey, IdempotencyKeyPepper, RequestDigestPepper []byte
	StoreEpoch                                       string
	IdempotencyTTL                                   time.Duration
	Now                                              func() time.Time
}

type enterpriseMutation struct {
	Metadata      productapi.CommandMetadata
	Operation     string
	Request       any
	EventType     string
	AggregateKind string
	EventBody     any
	Mutate        func(context.Context, pgx.Tx, string, time.Time) (productapi.EnterpriseResource, error)
}

func (service EnterpriseAdminService) CreateProgram(ctx context.Context, command productapi.CreateProgramCommand) (productapi.EnterpriseResource, error) {
	if !service.validCommand(command.CommandMetadata) || !validEnterpriseServiceName(command.Name) || !validEnterpriseServiceJSON(command.Settings) {
		return productapi.EnterpriseResource{}, productapi.ErrValidation
	}
	recordID, err := service.recordID(command.CommandMetadata, programCreateOperation)
	if err != nil {
		return productapi.EnterpriseResource{}, service.mapError(err)
	}
	programID, _ := ids.DeterministicUUID(service.IDKey, "enterprise-program", recordID)
	return service.execute(ctx, enterpriseMutation{Metadata: command.CommandMetadata, Operation: programCreateOperation, Request: command, EventType: "ProgramCreated", AggregateKind: "program", EventBody: subjectEventBody(programID, 1), Mutate: func(ctx context.Context, tx pgx.Tx, _ string, now time.Time) (productapi.EnterpriseResource, error) {
		_, err := tx.Exec(ctx, `INSERT INTO product.programs(id,tenant_id,owner_user_id,name,status,settings,created_at,updated_at) VALUES($1,$2,$3,$4,'active',$5,$6,$6)`, programID, command.TenantID, command.UserID, command.Name, command.Settings, now)
		return productapi.EnterpriseResource{ID: programID, Version: 1, Status: "active", UpdatedAt: now}, err
	}})
}

func (service EnterpriseAdminService) UpdateProgram(ctx context.Context, command productapi.UpdateProgramCommand) (productapi.EnterpriseResource, error) {
	if !service.validCommand(command.CommandMetadata) || uuid.Validate(command.ProgramID) != nil || !validEnterpriseServiceName(command.Name) || !validEnterpriseServiceJSON(command.Settings) || command.ExpectedVersion < 1 || (command.Status != "active" && command.Status != "paused" && command.Status != "archived") {
		return productapi.EnterpriseResource{}, productapi.ErrValidation
	}
	return service.execute(ctx, enterpriseMutation{Metadata: command.CommandMetadata, Operation: programUpdateOperation, Request: command, EventType: "ProgramUpdated", AggregateKind: "program", EventBody: subjectEventBody(command.ProgramID, command.ExpectedVersion+1), Mutate: func(ctx context.Context, tx pgx.Tx, _ string, now time.Time) (productapi.EnterpriseResource, error) {
		var current uint64
		if err := tx.QueryRow(ctx, `SELECT version FROM product.programs WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, command.ProgramID).Scan(&current); err != nil {
			return productapi.EnterpriseResource{}, err
		}
		if current != command.ExpectedVersion {
			return productapi.EnterpriseResource{}, ErrRouteConflict
		}
		next := current + 1
		tag, err := tx.Exec(ctx, `UPDATE product.programs SET version=$1,name=$2,status=$3,settings=$4,updated_at=$5 WHERE tenant_id=$6 AND id=$7 AND version=$8`, next, command.Name, command.Status, command.Settings, now, command.TenantID, command.ProgramID, current)
		if err == nil && tag.RowsAffected() != 1 {
			err = ErrRouteConflict
		}
		return productapi.EnterpriseResource{ID: command.ProgramID, Version: next, Status: command.Status, UpdatedAt: now}, err
	}})
}

func (service EnterpriseAdminService) CreateCohort(ctx context.Context, command productapi.CreateCohortCommand) (productapi.EnterpriseResource, error) {
	now := service.now()
	if !service.validCommand(command.CommandMetadata) || uuid.Validate(command.ProgramID) != nil || !validEnterpriseServiceName(command.Name) || (command.StartsAt != nil && command.EndsAt != nil && !command.EndsAt.After(*command.StartsAt)) || (command.EndsAt != nil && !command.EndsAt.After(now)) {
		return productapi.EnterpriseResource{}, productapi.ErrValidation
	}
	recordID, err := service.recordID(command.CommandMetadata, cohortCreateOperation)
	if err != nil {
		return productapi.EnterpriseResource{}, service.mapError(err)
	}
	cohortID, _ := ids.DeterministicUUID(service.IDKey, "enterprise-cohort", recordID)
	status := "active"
	if command.StartsAt != nil && command.StartsAt.After(now) {
		status = "scheduled"
	}
	return service.execute(ctx, enterpriseMutation{Metadata: command.CommandMetadata, Operation: cohortCreateOperation, Request: command, EventType: "CohortCreated", AggregateKind: "cohort", EventBody: subjectEventBody(cohortID, 1), Mutate: func(ctx context.Context, tx pgx.Tx, _ string, at time.Time) (productapi.EnterpriseResource, error) {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product.programs WHERE tenant_id=$1 AND id=$2 AND status='active')`, command.TenantID, command.ProgramID).Scan(&exists); err != nil {
			return productapi.EnterpriseResource{}, err
		}
		if !exists {
			return productapi.EnterpriseResource{}, productapi.ErrResourceNotFound
		}
		_, err := tx.Exec(ctx, `INSERT INTO product.cohorts(id,tenant_id,program_id,name,starts_at,ends_at,status,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8)`, cohortID, command.TenantID, command.ProgramID, command.Name, command.StartsAt, command.EndsAt, status, at)
		return productapi.EnterpriseResource{ID: cohortID, Version: 1, Status: status, UpdatedAt: at}, err
	}})
}

func (service EnterpriseAdminService) EnrollCohort(ctx context.Context, command productapi.EnrollCohortCommand) (productapi.EnterpriseResource, error) {
	if !service.validCommand(command.CommandMetadata) || uuid.Validate(command.CohortID) != nil || command.ExpectedVersion < 1 || !validEnterpriseServiceUUIDs(command.UserIDs, 1, 1000) {
		return productapi.EnterpriseResource{}, productapi.ErrValidation
	}
	command.UserIDs = append([]string(nil), command.UserIDs...)
	sort.Strings(command.UserIDs)
	return service.execute(ctx, enterpriseMutation{Metadata: command.CommandMetadata, Operation: cohortEnrollOperation, Request: command, EventType: "EnrollmentChanged", AggregateKind: "cohort", EventBody: stateEventBody(command.CohortID, command.ExpectedVersion, "members_enrolled"), Mutate: func(ctx context.Context, tx pgx.Tx, _ string, now time.Time) (productapi.EnterpriseResource, error) {
		var current uint64
		var status string
		if err := tx.QueryRow(ctx, `SELECT version,status FROM product.cohorts WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, command.CohortID).Scan(&current, &status); err != nil {
			return productapi.EnterpriseResource{}, err
		}
		if current != command.ExpectedVersion || status != "active" && status != "scheduled" {
			return productapi.EnterpriseResource{}, ErrRouteConflict
		}
		var active, existing int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM identity.memberships WHERE tenant_id=$1 AND user_id=ANY($2::uuid[]) AND status='active'`, command.TenantID, command.UserIDs).Scan(&active); err != nil {
			return productapi.EnterpriseResource{}, err
		}
		if active != len(command.UserIDs) {
			return productapi.EnterpriseResource{}, errSharePermission
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM product.enrollments WHERE tenant_id=$1 AND cohort_id=$2 AND user_id=ANY($3::uuid[])`, command.TenantID, command.CohortID, command.UserIDs).Scan(&existing); err != nil {
			return productapi.EnterpriseResource{}, err
		}
		if existing != 0 {
			return productapi.EnterpriseResource{}, ErrRouteConflict
		}
		for _, userID := range command.UserIDs {
			enrollmentID, idErr := ids.DeterministicUUID(service.IDKey, "enterprise-enrollment", command.TenantID+"\x00"+command.CohortID+"\x00"+userID)
			if idErr != nil {
				return productapi.EnterpriseResource{}, idErr
			}
			if _, err := tx.Exec(ctx, `INSERT INTO product.enrollments(id,tenant_id,cohort_id,user_id,status,enrolled_at,created_at,updated_at) VALUES($1,$2,$3,$4,'active',$5,$5,$5)`, enrollmentID, command.TenantID, command.CohortID, userID, now); err != nil {
				return productapi.EnterpriseResource{}, err
			}
		}
		next := current + 1
		tag, err := tx.Exec(ctx, `UPDATE product.cohorts SET version=$1,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND version=$5`, next, now, command.TenantID, command.CohortID, current)
		if err == nil && tag.RowsAffected() != 1 {
			err = ErrRouteConflict
		}
		return productapi.EnterpriseResource{ID: command.CohortID, Version: next, Status: status, UpdatedAt: now}, err
	}})
}

func (service EnterpriseAdminService) UnenrollCohort(ctx context.Context, command productapi.UnenrollCohortCommand) (productapi.EnterpriseResource, error) {
	if !service.validCommand(command.CommandMetadata) || uuid.Validate(command.CohortID) != nil || uuid.Validate(command.TargetUserID) != nil || strings.TrimSpace(command.Reason) == "" || len(command.Reason) > 1000 || command.ExpectedVersion < 1 {
		return productapi.EnterpriseResource{}, productapi.ErrValidation
	}
	return service.execute(ctx, enterpriseMutation{Metadata: command.CommandMetadata, Operation: cohortUnenrollOperation, Request: command, EventType: "EnrollmentChanged", AggregateKind: "cohort", EventBody: stateEventBody(command.CohortID, command.ExpectedVersion, "member_unenrolled"), Mutate: func(ctx context.Context, tx pgx.Tx, _ string, now time.Time) (productapi.EnterpriseResource, error) {
		var current uint64
		var status string
		if err := tx.QueryRow(ctx, `SELECT version,status FROM product.cohorts WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, command.CohortID).Scan(&current, &status); err != nil {
			return productapi.EnterpriseResource{}, err
		}
		if current != command.ExpectedVersion || status != "active" && status != "scheduled" {
			return productapi.EnterpriseResource{}, ErrRouteConflict
		}
		var enrollmentID string
		var enrollmentVersion uint64
		if err := tx.QueryRow(ctx, `SELECT id::text,version FROM product.enrollments WHERE tenant_id=$1 AND cohort_id=$2 AND user_id=$3 AND status='active' FOR UPDATE`, command.TenantID, command.CohortID, command.TargetUserID).Scan(&enrollmentID, &enrollmentVersion); err != nil {
			return productapi.EnterpriseResource{}, err
		}
		tag, err := tx.Exec(ctx, `UPDATE product.enrollments SET version=$1,status='left',left_at=$2,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND version=$5 AND status='active'`, enrollmentVersion+1, now, command.TenantID, enrollmentID, enrollmentVersion)
		if err != nil || tag.RowsAffected() != 1 {
			if err == nil {
				err = ErrRouteConflict
			}
			return productapi.EnterpriseResource{}, err
		}
		next := current + 1
		tag, err = tx.Exec(ctx, `UPDATE product.cohorts SET version=$1,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND version=$5`, next, now, command.TenantID, command.CohortID, current)
		if err == nil && tag.RowsAffected() != 1 {
			err = ErrRouteConflict
		}
		return productapi.EnterpriseResource{ID: command.CohortID, Version: next, Status: status, UpdatedAt: now}, err
	}})
}

func (service EnterpriseAdminService) PublishRolePack(ctx context.Context, command productapi.PublishRolePackCommand) (productapi.EnterpriseResource, error) {
	if !service.validCommand(command.CommandMetadata) || uuid.Validate(command.ProgramID) != nil || command.Revision < 1 || !validEnterpriseServiceUUIDs(command.RoleProfileIDs, 1, 100) || !validEnterpriseServiceUUIDs(command.TaskTemplateIDs, 1, 500) {
		return productapi.EnterpriseResource{}, productapi.ErrValidation
	}
	command.RoleProfileIDs = append([]string(nil), command.RoleProfileIDs...)
	command.TaskTemplateIDs = append([]string(nil), command.TaskTemplateIDs...)
	sort.Strings(command.RoleProfileIDs)
	sort.Strings(command.TaskTemplateIDs)
	rolePackID, _ := ids.DeterministicUUID(service.IDKey, "enterprise-role-pack", command.TenantID+"\x00"+command.ProgramID+"\x00"+strconv.Itoa(command.Revision))
	return service.execute(ctx, enterpriseMutation{Metadata: command.CommandMetadata, Operation: rolePackPublishOperation, Request: command, EventType: "RolePackPublished", AggregateKind: "role_pack", EventBody: subjectEventBody(rolePackID, 1), Mutate: func(ctx context.Context, tx pgx.Tx, _ string, now time.Time) (productapi.EnterpriseResource, error) {
		var program bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product.programs WHERE tenant_id=$1 AND id=$2 AND status='active')`, command.TenantID, command.ProgramID).Scan(&program); err != nil {
			return productapi.EnterpriseResource{}, err
		}
		if !program {
			return productapi.EnterpriseResource{}, productapi.ErrResourceNotFound
		}
		var profiles, tasks int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM product.role_profiles WHERE tenant_id=$1 AND id=ANY($2::uuid[]) AND status='active'`, command.TenantID, command.RoleProfileIDs).Scan(&profiles); err != nil {
			return productapi.EnterpriseResource{}, err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM product.task_templates WHERE tenant_id=$1 AND id=ANY($2::uuid[]) AND status='active'`, command.TenantID, command.TaskTemplateIDs).Scan(&tasks); err != nil {
			return productapi.EnterpriseResource{}, err
		}
		if profiles != len(command.RoleProfileIDs) || tasks != len(command.TaskTemplateIDs) {
			return productapi.EnterpriseResource{}, productapi.ErrValidation
		}
		profileJSON, _ := json.Marshal(command.RoleProfileIDs)
		taskJSON, _ := json.Marshal(command.TaskTemplateIDs)
		_, err := tx.Exec(ctx, `INSERT INTO product.role_packs(id,tenant_id,program_id,revision,status,role_profile_ids,task_template_ids,published_at,created_at,updated_at) VALUES($1,$2,$3,$4,'published',$5,$6,$7,$7,$7)`, rolePackID, command.TenantID, command.ProgramID, command.Revision, profileJSON, taskJSON, now)
		return productapi.EnterpriseResource{ID: rolePackID, Version: 1, Status: "published", UpdatedAt: now}, err
	}})
}

func (service EnterpriseAdminService) PublishTaskPack(ctx context.Context, command productapi.PublishTaskPackCommand) (productapi.EnterpriseResource, error) {
	if !service.validCommand(command.CommandMetadata) || uuid.Validate(command.ProgramID) != nil || command.Revision < 1 || !validEnterpriseServiceName(command.Name) || !validEnterpriseServiceUUIDs(command.TaskTemplateIDs, 1, 500) || !validEnterpriseServiceJSON(command.Assignment) {
		return productapi.EnterpriseResource{}, productapi.ErrValidation
	}
	command.TaskTemplateIDs = append([]string(nil), command.TaskTemplateIDs...)
	sort.Strings(command.TaskTemplateIDs)
	taskPackID, _ := ids.DeterministicUUID(service.IDKey, "enterprise-task-pack", command.TenantID+"\x00"+command.ProgramID+"\x00"+strconv.Itoa(command.Revision))
	return service.execute(ctx, enterpriseMutation{Metadata: command.CommandMetadata, Operation: taskPackPublishOperation, Request: command, EventType: "TaskPackPublished", AggregateKind: "task_pack", EventBody: subjectEventBody(taskPackID, 1), Mutate: func(ctx context.Context, tx pgx.Tx, _ string, now time.Time) (productapi.EnterpriseResource, error) {
		var program bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product.programs WHERE tenant_id=$1 AND id=$2 AND status='active')`, command.TenantID, command.ProgramID).Scan(&program); err != nil {
			return productapi.EnterpriseResource{}, err
		}
		if !program {
			return productapi.EnterpriseResource{}, productapi.ErrResourceNotFound
		}
		var tasks int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM product.task_templates WHERE tenant_id=$1 AND id=ANY($2::uuid[]) AND status='active'`, command.TenantID, command.TaskTemplateIDs).Scan(&tasks); err != nil {
			return productapi.EnterpriseResource{}, err
		}
		if tasks != len(command.TaskTemplateIDs) {
			return productapi.EnterpriseResource{}, productapi.ErrValidation
		}
		taskJSON, _ := json.Marshal(command.TaskTemplateIDs)
		_, err := tx.Exec(ctx, `INSERT INTO product.task_packs(id,tenant_id,program_id,revision,name,status,task_template_ids,assignment,published_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,'published',$6,$7,$8,$8,$8)`, taskPackID, command.TenantID, command.ProgramID, command.Revision, command.Name, taskJSON, command.Assignment, now)
		return productapi.EnterpriseResource{ID: taskPackID, Version: 1, Status: "published", UpdatedAt: now}, err
	}})
}

func (service EnterpriseAdminService) execute(ctx context.Context, spec enterpriseMutation) (productapi.EnterpriseResource, error) {
	canonical, err := json.Marshal(spec.Request)
	if err != nil {
		return productapi.EnterpriseResource{}, productapi.ErrValidation
	}
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return productapi.EnterpriseResource{}, service.mapError(err)
	}
	recordID, err := service.recordID(spec.Metadata, spec.Operation)
	if err != nil {
		return productapi.EnterpriseResource{}, service.mapError(err)
	}
	descriptor := payload.Descriptor{TenantID: spec.Metadata.TenantID, ObjectID: recordID, Class: "product-enterprise-admin-idempotency", ContentType: "application/json"}
	input := idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: spec.Metadata.TenantID, UserID: spec.Metadata.UserID, OperationID: spec.Operation}, RawKey: spec.Metadata.IdempotencyKey, RequestHash: requestHash, RequestID: spec.Metadata.RequestID}
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	if response, found, loadErr := executor.LoadCompleted(ctx, input); loadErr != nil {
		return productapi.EnterpriseResource{}, service.mapError(loadErr)
	} else if found {
		result, readErr := service.read(ctx, descriptor, response)
		result.Replayed = readErr == nil
		return result, service.mapError(readErr)
	}
	eventID, _ := ids.DeterministicUUID(service.IDKey, "enterprise-admin:event:"+spec.EventType, recordID)
	eventManifest, err := service.putJSON(ctx, payload.Descriptor{TenantID: spec.Metadata.TenantID, ObjectID: eventID, Class: "event-payload", ContentType: "application/json"}, spec.EventBody)
	if err != nil {
		return productapi.EnterpriseResource{}, service.mapError(err)
	}
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if _, innerErr := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, spec.Metadata.TenantID); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if innerErr := service.requireAdminRole(ctx, tx, spec.Metadata.TenantID, spec.Metadata.UserID); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		now := service.now()
		result, innerErr := spec.Mutate(ctx, tx, recordID, now)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		outboxID, _ := ids.DeterministicUUID(service.IDKey, "enterprise-admin:outbox:"+spec.EventType, recordID)
		publishID, _ := ids.DeterministicUUID(service.IDKey, "enterprise-admin:publish:"+spec.EventType, recordID)
		_, innerErr = service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: spec.Metadata.TenantID, UserID: spec.Metadata.UserID, EventType: spec.EventType, SchemaVersion: 1, AggregateKind: spec.AggregateKind, AggregateID: result.ID, AggregateVersion: result.Version, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: missionActor(spec.Metadata.UserID, spec.Metadata.SessionID), CorrelationID: spec.Metadata.RequestID, PayloadRef: eventManifest.Ref, PayloadHash: eventManifest.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishID, CommandType: "events.publish", PayloadRef: eventManifest.Ref, PayloadHash: eventManifest.Hash}}})
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		stored, innerErr := service.putJSON(ctx, descriptor, result)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		return idempotency.Response{Status: http.StatusOK, ContentType: "application/vnd.lites.enterprise-resource.v2+json", PayloadRef: stored.Ref, Hash: stored.Hash, ResourceVersion: result.Version}, nil
	})
	if err != nil {
		return productapi.EnterpriseResource{}, service.mapError(err)
	}
	result, err := service.read(ctx, descriptor, response)
	result.Replayed = replayed && err == nil
	return result, service.mapError(err)
}

func (service EnterpriseAdminService) requireAdminRole(ctx context.Context, tx pgx.Tx, tenantID, userID string) error {
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM identity.memberships m JOIN identity.tenants t ON t.id=m.tenant_id WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND m.role IN ('owner','admin','program_manager') AND t.kind='enterprise' AND t.status='active')`, tenantID, userID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return errSharePermission
	}
	return nil
}
func (service EnterpriseAdminService) recordID(metadata productapi.CommandMetadata, operation string) (string, error) {
	return ids.DeterministicUUID(service.IDKey, "product-idempotency:"+operation, metadata.TenantID+"\x00"+metadata.UserID+"\x00"+metadata.IdempotencyKey)
}
func (service EnterpriseAdminService) putJSON(ctx context.Context, d payload.Descriptor, value any) (payload.Manifest, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return payload.Manifest{}, err
	}
	return service.Payloads.Put(ctx, d, encoded)
}
func (service EnterpriseAdminService) read(ctx context.Context, d payload.Descriptor, response idempotency.Response) (productapi.EnterpriseResource, error) {
	encoded, err := service.Payloads.Get(ctx, d, payload.Manifest{Ref: response.PayloadRef, Hash: response.Hash})
	if err != nil {
		return productapi.EnterpriseResource{}, err
	}
	var result productapi.EnterpriseResource
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return result, payload.ErrIntegrity
	}
	return result, nil
}
func (service EnterpriseAdminService) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}
func (service EnterpriseAdminService) valid() bool {
	return service.Pool != nil && service.Payloads != nil && len(service.IDKey) >= 32 && len(service.IdempotencyKeyPepper) >= 32 && len(service.RequestDigestPepper) >= 32 && service.StoreEpoch != "" && service.IdempotencyTTL > 0
}
func (service EnterpriseAdminService) validCommand(metadata productapi.CommandMetadata) bool {
	return service.valid() && validMissionMetadata(metadata)
}
func (service EnterpriseAdminService) mapError(err error) error {
	var pgErr *pgconn.PgError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, productapi.ErrValidation), errors.Is(err, productapi.ErrPermissionDenied), errors.Is(err, productapi.ErrResourceNotFound), errors.Is(err, productapi.ErrStateConflict), errors.Is(err, productapi.ErrIdempotencyConflict):
		return err
	case errors.Is(err, errSharePermission):
		return productapi.ErrPermissionDenied
	case errors.Is(err, pgx.ErrNoRows):
		return productapi.ErrResourceNotFound
	case errors.Is(err, idempotency.ErrKeyConflict), errors.Is(err, idempotency.ErrInProgress):
		return productapi.ErrIdempotencyConflict
	case errors.Is(err, ErrRouteConflict):
		return productapi.ErrStateConflict
	case errors.As(err, &pgErr) && pgErr.Code == "23505":
		return productapi.ErrStateConflict
	case errors.As(err, &pgErr) && (pgErr.Code == "23503" || pgErr.Code == "23514"):
		return productapi.ErrValidation
	default:
		return errors.Join(productapi.ErrDependencyUnavailable, err)
	}
}
func validEnterpriseServiceName(value string) bool {
	value = strings.TrimSpace(value)
	return len(value) >= 1 && len(value) <= 200
}
func validEnterpriseServiceJSON(value json.RawMessage) bool {
	var object map[string]any
	return len(value) > 0 && len(value) <= 64<<10 && json.Unmarshal(value, &object) == nil && object != nil && len(object) <= 100
}
func validEnterpriseServiceUUIDs(values []string, min, max int) bool {
	if len(values) < min || len(values) > max {
		return false
	}
	seen := map[string]bool{}
	for _, value := range values {
		if uuid.Validate(value) != nil || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}
func subjectEventBody(subjectID string, subjectVersion uint64) map[string]any {
	return map[string]any{"subject_id": subjectID, "subject_version": subjectVersion}
}

func stateEventBody(subjectID string, previousVersion uint64, reason string) map[string]any {
	return map[string]any{
		"subject_id":      subjectID,
		"subject_version": previousVersion + 1,
		"previous_state":  strconv.FormatUint(previousVersion, 10),
		"new_state":       strconv.FormatUint(previousVersion+1, 10),
		"reason_code":     reason,
	}
}

var _ productapi.EnterpriseAdminService = EnterpriseAdminService{}
