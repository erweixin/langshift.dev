package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	enterpriseprivacy "github.com/langshift/lites/internal/enterprise/privacy"
	"github.com/langshift/lites/internal/idempotency"
	idempotencypostgres "github.com/langshift/lites/internal/idempotency/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	productapi "github.com/langshift/lites/internal/product/api"
)

const (
	aggregateQueryOperation       = "admin.aggregates.query.v2"
	aggregateSnapshotPayloadClass = "enterprise-aggregate-snapshot"
)

type AggregateQueryService struct {
	Pool                                             *pgxpool.Pool
	Payloads                                         payload.Store
	IDKey, IdempotencyKeyPepper, RequestDigestPepper []byte
	IdempotencyTTL                                   time.Duration
	Now                                              func() time.Time
}

type aggregateSnapshotRecord struct {
	ID, MetricID, MetricKey, ResultRef, ResultHash string
	Version                                        uint64
	AllowedDimensions, AllowedBuckets              []string
	MinimumCellSize, MinimumComplementSize         int
	Total                                          int
	Cells                                          map[string]enterpriseprivacy.Cell
}

type aggregateSnapshotPayload struct {
	SchemaVersion int                    `json:"schema_version"`
	MetricKey     string                 `json:"metric_key"`
	Total         int                    `json:"total"`
	Cells         []aggregatePayloadCell `json:"cells"`
}

type aggregatePayloadCell struct {
	TimeBucket string            `json:"time_bucket"`
	Dimensions map[string]string `json:"dimensions"`
	Count      int               `json:"count"`
	Value      float64           `json:"value"`
}

func (service AggregateQueryService) Query(ctx context.Context, command productapi.AggregateQueryCommand) (productapi.AggregateQueryResult, error) {
	if !service.valid() || !validMissionMetadata(command.CommandMetadata) || uuid.Validate(command.SnapshotID) != nil || command.MetricKey == "" || command.TimeBucket == "" || len(command.Dimensions) != 1 {
		return productapi.AggregateQueryResult{}, productapi.ErrValidation
	}
	canonical, err := json.Marshal(command)
	if err != nil {
		return productapi.AggregateQueryResult{}, productapi.ErrValidation
	}
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return productapi.AggregateQueryResult{}, productapi.ErrDependencyUnavailable
	}
	recordID, err := ids.DeterministicUUID(service.IDKey, "product-idempotency:"+aggregateQueryOperation, command.TenantID+"\x00"+command.UserID+"\x00"+command.IdempotencyKey)
	if err != nil {
		return productapi.AggregateQueryResult{}, productapi.ErrDependencyUnavailable
	}
	responseDescriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "product-aggregate-query-idempotency", ContentType: "application/json"}
	input := idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: aggregateQueryOperation}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	if response, found, loadErr := executor.LoadCompleted(ctx, input); loadErr != nil {
		return productapi.AggregateQueryResult{}, service.mapError(loadErr)
	} else if found {
		result, readErr := service.readResult(ctx, responseDescriptor, response)
		result.Replayed = readErr == nil
		return result, service.mapError(readErr)
	}
	snapshot, err := service.loadSnapshot(ctx, command.TenantID, command.SnapshotID)
	if err != nil {
		return productapi.AggregateQueryResult{}, service.mapError(err)
	}
	metric := snapshot.metric()
	privacyQuery := enterpriseprivacy.Query{MetricKey: command.MetricKey, TimeBucket: command.TimeBucket, Dimensions: command.Dimensions}
	engine, _ := enterpriseprivacy.NewEngine(1)
	decision, err := engine.Evaluate(metric, enterpriseprivacy.Snapshot{ID: snapshot.ID, MetricKey: snapshot.MetricKey, Total: snapshot.Total, Cells: snapshot.Cells}, command.UserID, privacyQuery)
	if err != nil {
		return productapi.AggregateQueryResult{}, service.mapError(err)
	}
	auditID, _ := ids.DeterministicUUID(service.IDKey, "aggregate-query-audit", recordID)
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if _, innerErr := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if innerErr := service.requireAggregateRole(ctx, tx, command.TenantID, command.UserID); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		var pinnedVersion uint64
		var pinnedMetricID, pinnedRef, pinnedHash string
		innerErr := tx.QueryRow(ctx, `SELECT version,metric_id::text,result_ref,result_hash FROM product.aggregate_snapshots WHERE tenant_id=$1 AND id=$2`, command.TenantID, command.SnapshotID).Scan(&pinnedVersion, &pinnedMetricID, &pinnedRef, &pinnedHash)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if pinnedVersion != snapshot.Version || pinnedMetricID != snapshot.MetricID || pinnedRef != snapshot.ResultRef || pinnedHash != snapshot.ResultHash {
			return idempotency.Response{}, enterpriseprivacy.ErrSnapshotConflict
		}
		if _, innerErr = tx.Exec(ctx, `INSERT INTO product.aggregate_query_budgets(tenant_id,snapshot_id,consumed,limit_value,version) VALUES($1,$2,0,100,1) ON CONFLICT DO NOTHING`, command.TenantID, command.SnapshotID); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		var consumed, limitValue int
		var budgetVersion uint64
		if innerErr = tx.QueryRow(ctx, `SELECT consumed,limit_value,version FROM product.aggregate_query_budgets WHERE tenant_id=$1 AND snapshot_id=$2 FOR UPDATE`, command.TenantID, command.SnapshotID).Scan(&consumed, &limitValue, &budgetVersion); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		now := service.now()
		if consumed >= limitValue {
			if _, innerErr = tx.Exec(ctx, `INSERT INTO product.aggregate_query_audit(id,tenant_id,snapshot_id,actor_user_id,query_hash,cell_key_hash,decision,reason,budget_before,budget_after,occurred_at) VALUES($1,$2,$3,$4,$5,$6,'rejected','query_budget_exhausted',$7,$7,$8)`, auditID, command.TenantID, command.SnapshotID, command.UserID, decision.QueryHash, decision.CellKeyHash, consumed, now); innerErr != nil {
				return idempotency.Response{}, innerErr
			}
			result := productapi.AggregateQueryResult{ID: auditID, Version: budgetVersion, Status: "query_budget_exhausted", UpdatedAt: now, SnapshotID: command.SnapshotID, MetricKey: command.MetricKey, Value: nil, SuppressionReason: stringPointer("query_budget_exhausted"), BudgetRemaining: 0}
			return service.storeResponse(ctx, responseDescriptor, result, budgetVersion)
		}
		if innerErr = service.freezeSuppression(ctx, tx, command.TenantID, command.SnapshotID, decision); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		nextConsumed, nextVersion := consumed+1, budgetVersion+1
		tag, innerErr := tx.Exec(ctx, `UPDATE product.aggregate_query_budgets SET consumed=$1,version=$2 WHERE tenant_id=$3 AND snapshot_id=$4 AND consumed=$5 AND version=$6`, nextConsumed, nextVersion, command.TenantID, command.SnapshotID, consumed, budgetVersion)
		if innerErr != nil || tag.RowsAffected() != 1 {
			if innerErr == nil {
				innerErr = ErrRouteConflict
			}
			return idempotency.Response{}, innerErr
		}
		status := "returned"
		var value *float64
		var suppressionReason *string
		if decision.Suppressed {
			status, suppressionReason = "suppressed", stringPointer(decision.Reason)
		} else {
			value = floatPointer(decision.Value)
		}
		if _, innerErr = tx.Exec(ctx, `INSERT INTO product.aggregate_query_audit(id,tenant_id,snapshot_id,actor_user_id,query_hash,cell_key_hash,decision,reason,cell_size,complement_size,budget_before,budget_after,occurred_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, auditID, command.TenantID, command.SnapshotID, command.UserID, decision.QueryHash, decision.CellKeyHash, status, decision.Reason, decision.CellSize, decision.ComplementSize, consumed, nextConsumed, now); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		result := productapi.AggregateQueryResult{ID: auditID, Version: nextVersion, Status: status, UpdatedAt: now, SnapshotID: command.SnapshotID, MetricKey: command.MetricKey, Value: value, SuppressionReason: suppressionReason, BudgetRemaining: limitValue - nextConsumed}
		return service.storeResponse(ctx, responseDescriptor, result, nextVersion)
	})
	if err != nil {
		return productapi.AggregateQueryResult{}, service.mapError(err)
	}
	result, err := service.readResult(ctx, responseDescriptor, response)
	result.Replayed = replayed && err == nil
	return result, service.mapError(err)
}

func (service AggregateQueryService) loadSnapshot(ctx context.Context, tenantID, snapshotID string) (aggregateSnapshotRecord, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return aggregateSnapshotRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return aggregateSnapshotRecord{}, err
	}
	var record aggregateSnapshotRecord
	var dimensionsJSON, bucketsJSON []byte
	err = tx.QueryRow(ctx, `SELECT s.id::text,s.version,s.metric_id::text,m.metric_key,m.allowed_dimensions,m.allowed_time_buckets,m.minimum_cell_size,m.minimum_complement_size,s.result_ref,s.result_hash FROM product.aggregate_snapshots s JOIN product.aggregate_metrics m ON m.tenant_id=s.tenant_id AND m.id=s.metric_id WHERE s.tenant_id=$1 AND s.id=$2`, tenantID, snapshotID).Scan(&record.ID, &record.Version, &record.MetricID, &record.MetricKey, &dimensionsJSON, &bucketsJSON, &record.MinimumCellSize, &record.MinimumComplementSize, &record.ResultRef, &record.ResultHash)
	if err != nil {
		return aggregateSnapshotRecord{}, err
	}
	if json.Unmarshal(dimensionsJSON, &record.AllowedDimensions) != nil || json.Unmarshal(bucketsJSON, &record.AllowedBuckets) != nil {
		return aggregateSnapshotRecord{}, payload.ErrIntegrity
	}
	if err = tx.Commit(ctx); err != nil {
		return aggregateSnapshotRecord{}, err
	}
	encoded, err := service.Payloads.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: snapshotID, Class: aggregateSnapshotPayloadClass, ContentType: "application/json"}, payload.Manifest{Ref: record.ResultRef, Hash: record.ResultHash})
	if err != nil {
		return aggregateSnapshotRecord{}, err
	}
	var snapshotPayload aggregateSnapshotPayload
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&snapshotPayload) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || snapshotPayload.SchemaVersion != 1 || snapshotPayload.MetricKey != record.MetricKey || snapshotPayload.Total < 0 || len(snapshotPayload.Cells) < 1 || len(snapshotPayload.Cells) > 10000 {
		return aggregateSnapshotRecord{}, payload.ErrIntegrity
	}
	record.Total, record.Cells = snapshotPayload.Total, make(map[string]enterpriseprivacy.Cell, len(snapshotPayload.Cells))
	metric := record.metric()
	for _, cell := range snapshotPayload.Cells {
		if cell.Count < 0 || cell.Count > record.Total {
			return aggregateSnapshotRecord{}, payload.ErrIntegrity
		}
		query := enterpriseprivacy.Query{MetricKey: record.MetricKey, TimeBucket: cell.TimeBucket, Dimensions: cell.Dimensions}
		key, keyErr := enterpriseprivacy.CellKey(metric, enterpriseprivacy.Snapshot{ID: record.ID, MetricKey: record.MetricKey, Total: record.Total, Cells: map[string]enterpriseprivacy.Cell{}}, query)
		if keyErr != nil {
			return aggregateSnapshotRecord{}, payload.ErrIntegrity
		}
		if _, duplicate := record.Cells[key]; duplicate {
			return aggregateSnapshotRecord{}, payload.ErrIntegrity
		}
		record.Cells[key] = enterpriseprivacy.Cell{Count: cell.Count, Value: cell.Value}
	}
	return record, nil
}

func (record aggregateSnapshotRecord) metric() enterpriseprivacy.Metric {
	sets := make([][]string, 0, len(record.AllowedDimensions))
	for _, dimension := range record.AllowedDimensions {
		sets = append(sets, []string{dimension})
	}
	return enterpriseprivacy.Metric{Key: record.MetricKey, DimensionSets: sets, TimeBuckets: record.AllowedBuckets, MinimumCellSize: record.MinimumCellSize, MinimumComplementSize: record.MinimumComplementSize}
}

func (service AggregateQueryService) requireAggregateRole(ctx context.Context, tx pgx.Tx, tenantID, userID string) error {
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM identity.memberships WHERE tenant_id=$1 AND user_id=$2 AND status='active' AND role IN ('owner','admin','program_manager'))`, tenantID, userID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return errSharePermission
	}
	return nil
}

func (service AggregateQueryService) freezeSuppression(ctx context.Context, tx pgx.Tx, tenantID, snapshotID string, decision enterpriseprivacy.Decision) error {
	var suppressed bool
	var reason string
	err := tx.QueryRow(ctx, `SELECT suppressed,reason FROM product.aggregate_suppressions WHERE tenant_id=$1 AND snapshot_id=$2 AND cell_key_hash=$3`, tenantID, snapshotID, decision.CellKeyHash).Scan(&suppressed, &reason)
	if err == nil {
		if suppressed != decision.Suppressed || reason != decision.Reason {
			return enterpriseprivacy.ErrSnapshotConflict
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	id, idErr := ids.DeterministicUUID(service.IDKey, "aggregate-suppression", tenantID+"\x00"+snapshotID+"\x00"+decision.CellKeyHash)
	if idErr != nil {
		return idErr
	}
	_, err = tx.Exec(ctx, `INSERT INTO product.aggregate_suppressions(id,tenant_id,snapshot_id,cell_key_hash,suppressed,reason,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$7)`, id, tenantID, snapshotID, decision.CellKeyHash, decision.Suppressed, decision.Reason, service.now())
	return err
}

func (service AggregateQueryService) storeResponse(ctx context.Context, descriptor payload.Descriptor, result productapi.AggregateQueryResult, version uint64) (idempotency.Response, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return idempotency.Response{}, err
	}
	manifest, err := service.Payloads.Put(ctx, descriptor, encoded)
	if err != nil {
		return idempotency.Response{}, err
	}
	return idempotency.Response{Status: http.StatusOK, ContentType: "application/vnd.lites.aggregate-query.v2+json", PayloadRef: manifest.Ref, Hash: manifest.Hash, ResourceVersion: version}, nil
}

func (service AggregateQueryService) readResult(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (productapi.AggregateQueryResult, error) {
	encoded, err := service.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: response.PayloadRef, Hash: response.Hash})
	if err != nil {
		return productapi.AggregateQueryResult{}, err
	}
	var result productapi.AggregateQueryResult
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return result, payload.ErrIntegrity
	}
	return result, nil
}

func (service AggregateQueryService) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func (service AggregateQueryService) valid() bool {
	return service.Pool != nil && service.Payloads != nil && len(service.IDKey) >= 32 && len(service.IdempotencyKeyPepper) >= 32 && len(service.RequestDigestPepper) >= 32 && service.IdempotencyTTL > 0
}

func (service AggregateQueryService) mapError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errSharePermission):
		return productapi.ErrPermissionDenied
	case errors.Is(err, pgx.ErrNoRows):
		return productapi.ErrResourceNotFound
	case errors.Is(err, enterpriseprivacy.ErrInvalidQuery), errors.Is(err, enterpriseprivacy.ErrCellNotFound), errors.Is(err, enterpriseprivacy.ErrInvalidCatalog):
		return productapi.ErrValidation
	case errors.Is(err, enterpriseprivacy.ErrSnapshotConflict), errors.Is(err, ErrRouteConflict):
		return productapi.ErrStateConflict
	case errors.Is(err, idempotency.ErrKeyConflict), errors.Is(err, idempotency.ErrInProgress):
		return productapi.ErrIdempotencyConflict
	default:
		return errors.Join(productapi.ErrDependencyUnavailable, err)
	}
}

func stringPointer(value string) *string  { return &value }
func floatPointer(value float64) *float64 { return &value }

var _ productapi.AggregateQueryService = AggregateQueryService{}
