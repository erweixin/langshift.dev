//go:build integration

package postgres

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/payload"
	productapi "github.com/langshift/lites/internal/product/api"
)

func TestAggregateQueryStableSuppressionAndAtomicTenantBudget(t *testing.T) {
	ctx := context.Background()
	admin := artifactPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := artifactPool(t, ctx, "LITES_TEST_PRODUCT_DATABASE_URL")
	defer pool.Close()
	now := time.Date(2026, time.July, 17, 14, 0, 0, 0, time.UTC)
	userID := "fa000000-0000-4000-8000-000000000001"
	tenantID := "fa000000-0000-4000-8000-000000000002"
	metricID := "fa000000-0000-4000-8000-000000000003"
	snapshotID := "fa000000-0000-4000-8000-000000000004"
	budgetSnapshotID := "fa000000-0000-4000-8000-000000000005"
	setup := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'aggregate-manager@example.invalid','en','active')`, []any{userID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region) VALUES($1,'enterprise','Aggregate Tenant','active','US')`, []any{tenantID}},
		{`INSERT INTO identity.memberships(id,tenant_id,user_id,role,status,joined_at) VALUES('fa000000-0000-4000-8000-000000000006',$1,$2,'program_manager','active',$3)`, []any{tenantID, userID, now}},
		{`INSERT INTO product.aggregate_metrics(id,tenant_id,metric_key,allowed_dimensions,allowed_time_buckets,minimum_cell_size,minimum_complement_size) VALUES($1,$2,'task_completion_rate','["cohort"]','["week"]',5,5)`, []any{metricID, tenantID}},
	}
	for _, statement := range setup {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	payloads := &missionPayloadStore{values: map[string][]byte{}}
	insertAggregateSnapshot(t, ctx, admin, payloads, tenantID, metricID, snapshotID, now, `{"schema_version":1,"metric_key":"task_completion_rate","total":20,"cells":[{"time_bucket":"week","dimensions":{"cohort":"safe"},"count":10,"value":0.75},{"time_bucket":"week","dimensions":{"cohort":"small"},"count":4,"value":1},{"time_bucket":"week","dimensions":{"cohort":"complement"},"count":16,"value":1}]}`)
	insertAggregateSnapshot(t, ctx, admin, payloads, tenantID, metricID, budgetSnapshotID, now, `{"schema_version":1,"metric_key":"task_completion_rate","total":20,"cells":[{"time_bucket":"week","dimensions":{"cohort":"safe"},"count":10,"value":0.75}]}`)
	service := AggregateQueryService{Pool: pool, Payloads: payloads, IDKey: bytes.Repeat([]byte{0xfa}, 32), IdempotencyKeyPepper: bytes.Repeat([]byte{0xfb}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xfc}, 32), IdempotencyTTL: 24 * time.Hour, Now: func() time.Time { return now }}
	command := aggregateCommand(tenantID, userID, snapshotID, "safe", "aggregate-safe-key")
	returned, err := service.Query(ctx, command)
	if err != nil || returned.Status != "returned" || returned.Value == nil || *returned.Value != .75 || returned.SuppressionReason != nil || returned.BudgetRemaining != 99 || returned.Replayed {
		t.Fatalf("returned=%#v err=%v", returned, err)
	}
	replayed, err := service.Query(ctx, command)
	if err != nil || !replayed.Replayed || replayed.ID != returned.ID || replayed.BudgetRemaining != 99 {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	for index, cohort := range []string{"small", "complement"} {
		result, queryErr := service.Query(ctx, aggregateCommand(tenantID, userID, snapshotID, cohort, fmt.Sprintf("aggregate-suppress-key-%d", index)))
		if queryErr != nil || result.Status != "suppressed" || result.Value != nil || result.SuppressionReason == nil {
			t.Fatalf("cohort=%s result=%#v err=%v", cohort, result, queryErr)
		}
	}
	invalid := aggregateCommand(tenantID, userID, snapshotID, "safe", "aggregate-invalid-key")
	invalid.Dimensions = map[string]string{"member_id": userID}
	if _, err = service.Query(ctx, invalid); err != productapi.ErrValidation {
		t.Fatalf("invalid error=%v", err)
	}
	var returnedCount, exhaustedCount atomic.Int64
	var group sync.WaitGroup
	for index := range 200 {
		group.Add(1)
		go func() {
			defer group.Done()
			result, queryErr := service.Query(ctx, aggregateCommand(tenantID, userID, budgetSnapshotID, "safe", fmt.Sprintf("aggregate-budget-key-%03d", index)))
			if queryErr != nil {
				t.Errorf("index=%d err=%v", index, queryErr)
				return
			}
			switch result.Status {
			case "returned":
				returnedCount.Add(1)
			case "query_budget_exhausted":
				exhaustedCount.Add(1)
			default:
				t.Errorf("index=%d status=%s", index, result.Status)
			}
		}()
	}
	group.Wait()
	if returnedCount.Load() != 100 || exhaustedCount.Load() != 100 {
		t.Fatalf("returned=%d exhausted=%d", returnedCount.Load(), exhaustedCount.Load())
	}
	var consumed, audits, suppressions, responses int
	err = admin.QueryRow(ctx, `SELECT b.consumed,(SELECT count(*) FROM product.aggregate_query_audit WHERE tenant_id=$1 AND snapshot_id=$2),(SELECT count(*) FROM product.aggregate_suppressions WHERE tenant_id=$1 AND snapshot_id=$2),(SELECT count(*) FROM agent.idempotency_responses WHERE tenant_id=$1 AND operation_id='admin.aggregates.query.v2' AND status='completed' AND response_payload_ref IS NOT NULL) FROM product.aggregate_query_budgets b WHERE b.tenant_id=$1 AND b.snapshot_id=$2`, tenantID, budgetSnapshotID).Scan(&consumed, &audits, &suppressions, &responses)
	if err != nil || consumed != 100 || audits != 200 || suppressions != 1 || responses < 200 {
		t.Fatalf("consumed=%d audits=%d suppressions=%d responses=%d err=%v", consumed, audits, suppressions, responses, err)
	}
}

func insertAggregateSnapshot(t *testing.T, ctx context.Context, admin *pgxpool.Pool, payloads *missionPayloadStore, tenantID, metricID, snapshotID string, now time.Time, body string) {
	t.Helper()
	manifest, err := payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: snapshotID, Class: aggregateSnapshotPayloadClass, ContentType: "application/json"}, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO product.aggregate_snapshots(id,tenant_id,metric_id,snapshot_key,source_high_watermark,result_ref,result_hash,frozen_at) VALUES($1,$2,$3,$4,'fixture-high-watermark',$5,$6,$7)`, snapshotID, tenantID, metricID, "snapshot:"+snapshotID, manifest.Ref, manifest.Hash, now); err != nil {
		t.Fatal(err)
	}
}

func aggregateCommand(tenantID, userID, snapshotID, cohort, key string) productapi.AggregateQueryCommand {
	return productapi.AggregateQueryCommand{CommandMetadata: productapi.CommandMetadata{RequestID: "fa000000-0000-4000-8000-000000000007", ClientRequestID: "client-" + key, IdempotencyKey: key, TenantID: tenantID, UserID: userID, SessionID: "fa000000-0000-4000-8000-000000000008"}, SnapshotID: snapshotID, MetricKey: "task_completion_rate", TimeBucket: "week", Dimensions: map[string]string{"cohort": cohort}}
}
