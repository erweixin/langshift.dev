package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/idempotency"
	idempotencypostgres "github.com/langshift/lites/internal/idempotency/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	productapi "github.com/langshift/lites/internal/product/api"
)

const aggregateSnapshotOperation = "admin.aggregates.create.v2"

type AggregateSnapshotService struct {
	Pool                                             *pgxpool.Pool
	Appender                                         eventpostgres.Appender
	Payloads                                         payload.Store
	IDKey, IdempotencyKeyPepper, RequestDigestPepper []byte
	StoreEpoch                                       string
	IdempotencyTTL                                   time.Duration
	Now                                              func() time.Time
}

func (service AggregateSnapshotService) Create(ctx context.Context, command productapi.AggregateSnapshotCommand) (productapi.AggregateSnapshotResult, error) {
	dimension, dimensionValue := snapshotDimension(command.Dimensions)
	if !service.valid() || !validMissionMetadata(command.CommandMetadata) || !validSnapshotMetric(command.MetricKey) || !validSnapshotBucket(command.TimeBucket) || !validSnapshotDimension(dimension, dimensionValue) || !validSnapshotPeriod(command.TimeBucket, command.PeriodStart, command.PeriodEnd) {
		return productapi.AggregateSnapshotResult{}, productapi.ErrValidation
	}
	command.PeriodStart = command.PeriodStart.UTC()
	command.PeriodEnd = command.PeriodEnd.UTC()
	canonical, err := json.Marshal(command)
	if err != nil {
		return productapi.AggregateSnapshotResult{}, productapi.ErrValidation
	}
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return productapi.AggregateSnapshotResult{}, productapi.ErrDependencyUnavailable
	}
	recordID, err := ids.DeterministicUUID(service.IDKey, "product-idempotency:"+aggregateSnapshotOperation, command.TenantID+"\x00"+command.UserID+"\x00"+command.IdempotencyKey)
	if err != nil {
		return productapi.AggregateSnapshotResult{}, productapi.ErrDependencyUnavailable
	}
	snapshotID, _ := ids.DeterministicUUID(service.IDKey, "aggregate-snapshot", recordID)
	responseDescriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "product-aggregate-snapshot-idempotency", ContentType: "application/json"}
	input := idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: aggregateSnapshotOperation}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	if response, found, loadErr := executor.LoadCompleted(ctx, input); loadErr != nil {
		return productapi.AggregateSnapshotResult{}, service.mapError(loadErr)
	} else if found {
		result, readErr := service.read(ctx, responseDescriptor, response)
		result.Replayed = readErr == nil
		return result, service.mapError(readErr)
	}
	now := service.now()
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if _, innerErr := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		queries := AggregateQueryService{Pool: service.Pool}
		if innerErr := queries.requireAggregateRole(ctx, tx, command.TenantID, command.UserID); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		metricID, innerErr := service.ensureMetric(ctx, tx, command.TenantID, command.MetricKey)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		total, cellCount, value, watermark, innerErr := service.computeCell(ctx, tx, command, dimension, dimensionValue)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		snapshotPayload := aggregateSnapshotPayload{SchemaVersion: 1, MetricKey: command.MetricKey, Total: total, Cells: []aggregatePayloadCell{{TimeBucket: command.TimeBucket, Dimensions: command.Dimensions, Count: cellCount, Value: value}}}
		encodedSnapshot, innerErr := json.Marshal(snapshotPayload)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		manifest, innerErr := service.Payloads.Put(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: snapshotID, Class: aggregateSnapshotPayloadClass, ContentType: "application/json"}, encodedSnapshot)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		tag, innerErr := tx.Exec(ctx, `INSERT INTO product.aggregate_snapshots(id,tenant_id,metric_id,snapshot_key,source_high_watermark,result_ref,result_hash,frozen_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8,$8) ON CONFLICT (tenant_id,snapshot_key) DO NOTHING`, snapshotID, command.TenantID, metricID, "request:"+recordID, watermark, manifest.Ref, manifest.Hash, now)
		if innerErr != nil || tag.RowsAffected() != 1 {
			if innerErr == nil {
				innerErr = ErrRouteConflict
			}
			return idempotency.Response{}, innerErr
		}
		eventID, _ := ids.DeterministicUUID(service.IDKey, "aggregate-snapshot:event", recordID)
		outboxID, _ := ids.DeterministicUUID(service.IDKey, "aggregate-snapshot:outbox", recordID)
		publishID, _ := ids.DeterministicUUID(service.IDKey, "aggregate-snapshot:publish", recordID)
		eventPayload, innerErr := service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: eventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": snapshotID, "subject_version": 1})
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if _, innerErr = service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: command.TenantID, UserID: command.UserID, EventType: "AggregateSnapshotCreated", SchemaVersion: 1, AggregateKind: "aggregate_snapshot", AggregateID: snapshotID, AggregateVersion: 1, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: missionActor(command.UserID, command.SessionID), CorrelationID: command.RequestID, PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishID, CommandType: "events.publish", PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}}}); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		result := productapi.AggregateSnapshotResult{ID: snapshotID, Version: 1, Status: "frozen", MetricKey: command.MetricKey, Dimensions: command.Dimensions, TimeBucket: command.TimeBucket, PeriodStart: command.PeriodStart.Format(time.DateOnly), PeriodEnd: command.PeriodEnd.Format(time.DateOnly), PopulationCount: total, CellCount: cellCount, SourceHighWatermark: watermark, FrozenAt: now}
		return service.store(ctx, responseDescriptor, result)
	})
	if err != nil {
		return productapi.AggregateSnapshotResult{}, service.mapError(err)
	}
	result, err := service.read(ctx, responseDescriptor, response)
	result.Replayed = replayed && err == nil
	return result, service.mapError(err)
}

func (service AggregateSnapshotService) ensureMetric(ctx context.Context, tx pgx.Tx, tenantID, metricKey string) (string, error) {
	var metricID string
	err := tx.QueryRow(ctx, `SELECT id::text FROM product.aggregate_metrics WHERE tenant_id=$1 AND metric_key=$2`, tenantID, metricKey).Scan(&metricID)
	if err == nil {
		return metricID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	metricID, err = ids.DeterministicUUID(service.IDKey, "aggregate-metric", tenantID+"\x00"+metricKey)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO product.aggregate_metrics(id,tenant_id,metric_key,allowed_dimensions,allowed_time_buckets,minimum_cell_size,minimum_complement_size) VALUES($1,$2,$3,'["program","cohort","role_pack","locale","coarse_week"]','["week","month","quarter"]',5,5) ON CONFLICT (tenant_id,metric_key) DO NOTHING`, metricID, tenantID, metricKey)
	if err != nil {
		return "", err
	}
	if err = tx.QueryRow(ctx, `SELECT id::text FROM product.aggregate_metrics WHERE tenant_id=$1 AND metric_key=$2`, tenantID, metricKey).Scan(&metricID); err != nil {
		return "", err
	}
	return metricID, nil
}

func (service AggregateSnapshotService) computeCell(ctx context.Context, tx pgx.Tx, command productapi.AggregateSnapshotCommand, dimension, dimensionValue string) (int, int, float64, string, error) {
	population, err := aggregatePopulationSQL(dimension)
	if err != nil {
		return 0, 0, 0, "", err
	}
	endExclusive := command.PeriodEnd.AddDate(0, 0, 1)
	// Bind every parameter even for count-only metrics. PostgreSQL cannot infer
	// the types of the period placeholders when active_members does not otherwise
	// reference them, while dimension filters still use the shared $4 slot.
	base := `WITH parameters AS (SELECT $2::timestamptz period_start,$3::timestamptz period_end,$4::text dimension_value), population AS (` + population + `), totals AS (SELECT count(*)::integer total FROM identity.memberships WHERE tenant_id=$1 AND status='active'), cell AS (SELECT count(*)::integer count FROM population)`
	var total, count int
	var value float64
	switch command.MetricKey {
	case "active_members":
		err = tx.QueryRow(ctx, base+` SELECT totals.total,cell.count,cell.count::double precision FROM totals,cell`, command.TenantID, command.PeriodStart, endExclusive, dimensionValue).Scan(&total, &count, &value)
	case "task_completion_rate":
		err = tx.QueryRow(ctx, base+`, measure AS (SELECT COALESCE(count(*) FILTER (WHERE d.status='completed')::double precision/NULLIF(count(*),0),0) value FROM product.daily_tasks d JOIN population p ON p.user_id=d.user_id WHERE d.tenant_id=$1 AND d.scheduled_for>=$2::date AND d.scheduled_for<$3::date) SELECT totals.total,cell.count,measure.value FROM totals,cell,measure`, command.TenantID, command.PeriodStart, endExclusive, dimensionValue).Scan(&total, &count, &value)
	case "weekly_loop_completion_rate":
		err = tx.QueryRow(ctx, base+`, measure AS (SELECT COALESCE(count(DISTINCT d.user_id) FILTER (WHERE d.status='completed')::double precision/NULLIF((SELECT count FROM cell),0),0) value FROM product.daily_tasks d JOIN population p ON p.user_id=d.user_id WHERE d.tenant_id=$1 AND d.scheduled_for>=$2::date AND d.scheduled_for<$3::date) SELECT totals.total,cell.count,measure.value FROM totals,cell,measure`, command.TenantID, command.PeriodStart, endExclusive, dimensionValue).Scan(&total, &count, &value)
	case "project_completion_rate":
		err = tx.QueryRow(ctx, base+`, measure AS (SELECT COALESCE(count(*) FILTER (WHERE p.status='completed')::double precision/NULLIF(count(*),0),0) value FROM product.projects p JOIN population population ON population.user_id=p.user_id WHERE p.tenant_id=$1 AND p.created_at>=$2 AND p.created_at<$3) SELECT totals.total,cell.count,measure.value FROM totals,cell,measure`, command.TenantID, command.PeriodStart, endExclusive, dimensionValue).Scan(&total, &count, &value)
	case "aggregate_credit_usage":
		err = tx.QueryRow(ctx, base+`, measure AS (SELECT COALESCE(sum(l.units),0)::double precision value FROM contracts.usage_ledger l JOIN population p ON p.user_id=l.user_id WHERE l.tenant_id=$1 AND l.recorded_at>=$2 AND l.recorded_at<$3) SELECT totals.total,cell.count,measure.value FROM totals,cell,measure`, command.TenantID, command.PeriodStart, endExclusive, dimensionValue).Scan(&total, &count, &value)
	default:
		return 0, 0, 0, "", productapi.ErrValidation
	}
	if err != nil {
		return 0, 0, 0, "", err
	}
	watermarkInput, _ := json.Marshal([]any{command.TenantID, command.MetricKey, command.Dimensions, command.TimeBucket, command.PeriodStart.Format(time.DateOnly), command.PeriodEnd.Format(time.DateOnly), total, count, value})
	digest := sha256.Sum256(watermarkInput)
	return total, count, value, "sha256:" + hex.EncodeToString(digest[:]), nil
}

func aggregatePopulationSQL(dimension string) (string, error) {
	base := `SELECT m.user_id FROM identity.memberships m JOIN identity.users u ON u.id=m.user_id WHERE m.tenant_id=$1 AND m.status='active' AND `
	switch dimension {
	case "cohort":
		return base + `EXISTS (SELECT 1 FROM product.enrollments e WHERE e.tenant_id=m.tenant_id AND e.user_id=m.user_id AND e.cohort_id=$4::uuid AND e.status='active')`, nil
	case "program":
		return base + `EXISTS (SELECT 1 FROM product.enrollments e JOIN product.cohorts c ON c.tenant_id=e.tenant_id AND c.id=e.cohort_id WHERE e.tenant_id=m.tenant_id AND e.user_id=m.user_id AND e.status='active' AND c.program_id=$4::uuid)`, nil
	case "role_pack":
		return base + `EXISTS (SELECT 1 FROM product.role_packs r JOIN product.cohorts c ON c.tenant_id=r.tenant_id AND c.program_id=r.program_id JOIN product.enrollments e ON e.tenant_id=c.tenant_id AND e.cohort_id=c.id WHERE r.tenant_id=m.tenant_id AND r.id=$4::uuid AND r.status='published' AND e.user_id=m.user_id AND e.status='active')`, nil
	case "locale":
		return base + `u.locale=$4`, nil
	case "coarse_week":
		return base + `m.joined_at>=$4::date AND m.joined_at<$4::date+interval '7 days'`, nil
	default:
		return "", productapi.ErrValidation
	}
}

func snapshotDimension(values map[string]string) (string, string) {
	if len(values) != 1 {
		return "", ""
	}
	for name, value := range values {
		return name, value
	}
	return "", ""
}

func validSnapshotMetric(value string) bool {
	return value == "active_members" || value == "task_completion_rate" || value == "weekly_loop_completion_rate" || value == "project_completion_rate" || value == "aggregate_credit_usage"
}
func validSnapshotBucket(value string) bool {
	return value == "week" || value == "month" || value == "quarter"
}
func validSnapshotDimension(name, value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	switch name {
	case "program", "cohort", "role_pack":
		return uuid.Validate(value) == nil
	case "locale":
		return value == "en" || value == "zh-CN"
	case "coarse_week":
		_, err := time.Parse(time.DateOnly, value)
		return err == nil
	default:
		return false
	}
}
func validSnapshotPeriod(bucket string, start, end time.Time) bool {
	if start.IsZero() || end.IsZero() || end.Before(start) || start.Hour() != 0 || end.Hour() != 0 {
		return false
	}
	days := int(end.Sub(start).Hours()/24) + 1
	return bucket == "week" && days <= 7 || bucket == "month" && days <= 31 || bucket == "quarter" && days <= 92
}

func (service AggregateSnapshotService) store(ctx context.Context, descriptor payload.Descriptor, result productapi.AggregateSnapshotResult) (idempotency.Response, error) {
	manifest, err := service.putJSON(ctx, descriptor, result)
	if err != nil {
		return idempotency.Response{}, err
	}
	return idempotency.Response{Status: http.StatusOK, ContentType: "application/json", PayloadRef: manifest.Ref, Hash: manifest.Hash, ResourceVersion: 1}, nil
}
func (service AggregateSnapshotService) read(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (productapi.AggregateSnapshotResult, error) {
	encoded, err := service.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: response.PayloadRef, Hash: response.Hash})
	if err != nil {
		return productapi.AggregateSnapshotResult{}, err
	}
	var result productapi.AggregateSnapshotResult
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return result, payload.ErrIntegrity
	}
	return result, nil
}
func (service AggregateSnapshotService) putJSON(ctx context.Context, descriptor payload.Descriptor, value any) (payload.Manifest, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return payload.Manifest{}, err
	}
	return service.Payloads.Put(ctx, descriptor, encoded)
}
func (service AggregateSnapshotService) valid() bool {
	return service.Pool != nil && service.Payloads != nil && len(service.IDKey) >= 32 && len(service.IdempotencyKeyPepper) >= 32 && len(service.RequestDigestPepper) >= 32 && service.StoreEpoch != "" && service.IdempotencyTTL > 0
}
func (service AggregateSnapshotService) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}
func (service AggregateSnapshotService) mapError(err error) error {
	return (AggregateQueryService{}).mapError(err)
}

var _ productapi.AggregateSnapshotService = AggregateSnapshotService{}
