//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	billingpostgres "github.com/langshift/lites/internal/billing/postgres"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/payload"
)

func TestProviderDispatchIsAtMostOnceAndFallbackIsFullyAccounted(t *testing.T) {
	ctx := context.Background()
	admin := llmPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := llmPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()
	now := time.Date(2026, time.July, 15, 20, 0, 0, 0, time.UTC)
	const (
		userID         = "c6000000-0000-4000-8000-000000000001"
		tenantID       = "c6000000-0000-4000-8000-000000000002"
		runID          = "c6000000-0000-4000-8000-000000000003"
		runAttempt     = "c6000000-0000-4000-8000-000000000004"
		llmAttempt     = "c6000000-0000-4000-8000-000000000005"
		first          = "c6000000-0000-4000-8000-000000000006"
		second         = "c6000000-0000-4000-8000-000000000007"
		epoch          = "c6000000-0000-4000-8000-000000000008"
		correlation    = "c6000000-0000-4000-8000-000000000009"
		credential     = "c6000000-0000-4000-8000-000000000014"
		bucket         = "c6000000-0000-4000-8000-000000000015"
		reservationOne = "c6000000-0000-4000-8000-000000000016"
		reservationTwo = "c6000000-0000-4000-8000-000000000017"
		unknownLLM     = "c6000000-0000-4000-8000-000000000018"
		unknownAttempt = "c6000000-0000-4000-8000-000000000019"
		unknownReserve = "c6000000-0000-4000-8000-000000000020"
		abandonLLM     = "c6000000-0000-4000-8000-000000000021"
		abandonAttempt = "c6000000-0000-4000-8000-000000000022"
		abandonReserve = "c6000000-0000-4000-8000-000000000023"
		fencedLLM      = "c6000000-0000-4000-8000-000000000024"
		fencedAttempt  = "c6000000-0000-4000-8000-000000000025"
		fencedReserve  = "c6000000-0000-4000-8000-000000000026"
		lateLLM        = "c6000000-0000-4000-8000-000000000027"
		lateAttempt    = "c6000000-0000-4000-8000-000000000028"
		lateReserve    = "c6000000-0000-4000-8000-000000000029"
		cancellationID = "c6000000-0000-4000-8000-000000000030"
		cancelEventID  = "c6000000-0000-4000-8000-000000000031"
		cancelOutboxID = "c6000000-0000-4000-8000-000000000032"
		cancelPublish  = "c6000000-0000-4000-8000-000000000033"
		cancelledStart = "c6000000-0000-4000-8000-000000000034"
	)
	setup := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'llm-owner@example.invalid','en','active')`, []any{userID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','LLM Tenant','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO product.byok_credential_versions(tenant_id,credential_id,version,user_id,provider_id,bound_host,secret_ref,secret_version,status,last_validated_at) VALUES($1,$2,1,$3,'openai','api.openai.com','vault://byok/c6/v1','1','active',$4)`, []any{tenantID, credential, userID, now}},
		{`INSERT INTO product.byok_credentials(id,tenant_id,user_id,provider_id,bound_host,secret_ref,secret_version,status,last_validated_at) VALUES($1,$2,$3,'openai','api.openai.com','vault://byok/c6/v1','1','active',$4)`, []any{credential, tenantID, userID, now}},
		{`INSERT INTO product.byok_credential_versions(tenant_id,credential_id,version,user_id,provider_id,bound_host,secret_ref,secret_version,status,last_validated_at) VALUES($1,$2,2,$3,'openai','api.openai.com','vault://byok/c6/v2','2','active',$4)`, []any{tenantID, credential, userID, now}},
		{`UPDATE product.byok_credentials SET version=2,secret_ref='vault://byok/c6/v2',secret_version='2',updated_at=$1 WHERE tenant_id=$2 AND id=$3`, []any{now, tenantID, credential}},
		{`INSERT INTO contracts.credit_buckets(id,tenant_id,bucket_kind,granted_units,starts_at,expires_at,created_at,updated_at) VALUES($1,$2,'llm',1000,$3,$4,$5,$5)`, []any{bucket, tenantID, now.Add(-time.Hour), now.Add(24 * time.Hour), now}},
		{`INSERT INTO agent.outbox(id,tenant_id,command_id,command_type,aggregate_kind,aggregate_id,store_epoch,payload_ref,payload_hash,status,available_at,published_at) VALUES('c6000000-0000-4000-8000-000000000012',$1,'c6000000-0000-4000-8000-000000000011','StartAgentRun','run',$2,$3,'encrypted://run/start','run-start-c6','published',$4,$4)`, []any{tenantID, runID, epoch, now}},
		{`INSERT INTO agent.jobs(id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,due_at,enqueued_at) VALUES('c6000000-0000-4000-8000-000000000013',$1,'c6000000-0000-4000-8000-000000000011','interactive','llm',100,1,5,'running',$2,$3,$2)`, []any{tenantID, now, now.Add(time.Hour)}},
		{`INSERT INTO agent.job_attempts(id,tenant_id,job_id,command_id,fence,lease_token_hash,lease_expires_at,worker_id,status,started_at) VALUES($1,$2,'c6000000-0000-4000-8000-000000000013','c6000000-0000-4000-8000-000000000011',1,$3,$4,'llm-worker-c6','running',$5)`, []any{runAttempt, tenantID, bytes.Repeat([]byte{0xc6}, 32), now.Add(10 * time.Minute), now}},
		{`INSERT INTO agent.runs(id,tenant_id,user_id,conversation_id,status,run_version,active_command_id,active_attempt_id,current_fence,lease_token_hash,lease_expires_at,due_at,profile_snapshot_id,budget_snapshot) VALUES($1,$2,$3,'c6000000-0000-4000-8000-000000000010','executing',3,'c6000000-0000-4000-8000-000000000011',$4,1,$5,$6,$7,'agent@c6','{}')`, []any{runID, tenantID, userID, runAttempt, bytes.Repeat([]byte{0xc6}, 32), now.Add(10 * time.Minute), now.Add(time.Hour)}},
	}
	for _, statement := range setup {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	appender := eventpostgres.Appender{Now: func() time.Time { return now }}
	billing := billingpostgres.Store{Pool: pool, Appender: appender, Epochs: llmEpochStub{epoch}, StoreEpoch: epoch, IDKey: bytes.Repeat([]byte{0xc9}, 32), Now: func() time.Time { return now }}
	store := Store{Pool: pool, Appender: appender, Epochs: llmEpochStub{epoch}, StoreEpoch: epoch, IDKey: bytes.Repeat([]byte{0xc7}, 32), TokenPepper: bytes.Repeat([]byte{0xc8}, 32), Billing: billing, Now: func() time.Time { return now }}
	manifest := ContextManifest{
		SchemaVersion: 1,
		Run:           SnapshotBinding{ID: runID, Version: 3, Hash: "run-hash-c6"},
		Prompt:        SnapshotBinding{ID: "prompt:c6", Version: 1, Hash: strings.Repeat("a", 64)},
		Messages:      []SnapshotBinding{{ID: "messages:c6", Version: 1, Hash: "messages-hash-c6"}},
		Tools:         []SnapshotBinding{{ID: "tools:c6", Version: 4, Hash: "tools-hash-c6"}},
		Memory:        []SnapshotBinding{{ID: "memory:c6", Version: 2, Hash: "memory-hash-c6"}},
		Router:        SnapshotBinding{ID: "router:c6", Version: 5, Hash: "router-hash-c6"},
		Budget:        SnapshotBinding{ID: "budget:c6", Version: 1, Hash: "budget-hash-c6"},
		Policy:        SnapshotBinding{ID: "policy:c6", Version: 7, Hash: "policy-hash-c6"},
		CandidateModels: []ModelCandidate{
			{ProviderID: "openai", ModelID: "model:c6-primary", ModelVersion: "2026-07-01", BoundHost: "api.openai.com", PricingVersion: "pricing:c6-primary"},
			{ProviderID: "anthropic", ModelID: "model:c6-fallback", ModelVersion: "2026-06-15", BoundHost: "api.anthropic.com", PricingVersion: "pricing:c6-fallback"},
		},
	}
	started, err := store.StartLLMAttempt(ctx, StartLLMAttemptCommand{AttemptID: llmAttempt, TenantID: tenantID, UserID: userID, RunID: runID, RunAttemptID: runAttempt, RunVersion: 3, RunFence: 1, StreamGeneration: 1, AttemptKey: "attempt-key-c6", CorrelationID: correlation, Manifest: manifest, Actor: json.RawMessage(`{"kind":"service"}`), StartedEvent: pointer("llm-started")})
	if err != nil || started.Status != "running" || started.ContextManifestHash == "" {
		t.Fatalf("start=%#v err=%v", started, err)
	}
	if _, err = billing.Reserve(ctx, billingpostgres.ReserveCommand{ReservationID: reservationOne, RequestID: "usage-request-c6-1", TenantID: tenantID, UserID: userID, BucketID: bucket, OperationKey: "provider-attempt-c6-1", SubjectKind: "provider_attempt", SubjectID: first, SubjectVersion: 1, ReservedUnits: 100, ExpiresAt: now.Add(10 * time.Minute), CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), ReservedEvent: billingpostgres.PayloadPointer{Ref: "encrypted://usage/reserved/1", Hash: "usage-reserved-1"}}); err != nil {
		t.Fatal(err)
	}
	prepareOne, _ := store.IssuePrepareToken()
	preparedOne, err := store.PrepareProviderAttempt(ctx, PrepareProviderAttemptCommand{AttemptID: first, ProviderAttemptID: "provider-attempt-c6-1", LLMAttemptID: llmAttempt, UsageReservationID: reservationOne, TenantID: tenantID, UserID: userID, AttemptKey: "attempt-key-c6", Ordinal: 1, Candidate: manifest.CandidateModels[0], RequestHash: "request-c6-1", ContextManifestHash: started.ContextManifestHash, PrepareToken: prepareOne, PrepareTokenExpiresAt: now.Add(time.Minute), BYOK: &BYOKBinding{CredentialID: credential, Version: 1, SecretVersion: "1"}, CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), PreparedEvent: pointer("provider-prepared-1")})
	if err != nil || preparedOne.Ordinal != 1 {
		t.Fatalf("prepare one=%#v err=%v", preparedOne, err)
	}
	credentialTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer credentialTx.Rollback(ctx)
	if _, err = credentialTx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		t.Fatal(err)
	}
	var exactSecretRef string
	if err = credentialTx.QueryRow(ctx, `SELECT secret_ref FROM product.byok_credential_versions WHERE tenant_id=$1 AND credential_id=$2 AND version=1 AND provider_id='openai' AND bound_host='api.openai.com' AND secret_version='1' AND status='active'`, tenantID, credential).Scan(&exactSecretRef); err != nil || exactSecretRef != "vault://byok/c6/v1" {
		t.Fatalf("agent role cannot resolve exact BYOK version: ref=%q err=%v", exactSecretRef, err)
	}
	if err = credentialTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	completionOne, _ := store.IssueCompletionToken()
	dispatch := AuthorizeDispatchCommand{AttemptID: first, TenantID: tenantID, RequestHash: "request-c6-1", PrepareToken: prepareOne, CompletionToken: completionOne, CompletionDeadline: now.Add(2 * time.Minute), CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), DispatchEvent: pointer("provider-dispatched-1")}
	const contenders = 16
	var wait sync.WaitGroup
	type authorizationOutcome struct {
		authorization DispatchAuthorization
		err           error
	}
	results := make(chan authorizationOutcome, contenders)
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			authorization, authorizeErr := store.AuthorizeDispatch(ctx, dispatch)
			results <- authorizationOutcome{authorization: authorization, err: authorizeErr}
		}()
	}
	wait.Wait()
	close(results)
	winners, rejected := 0, 0
	for outcome := range results {
		if outcome.err == nil {
			winners++
			if outcome.authorization.BYOK == nil || outcome.authorization.BYOK.CredentialID != credential || outcome.authorization.BYOK.Version != 1 || outcome.authorization.BYOK.SecretRef != "vault://byok/c6/v1" || outcome.authorization.BYOK.SecretVersion != "1" {
				t.Fatalf("dispatch did not resolve exact credential: %#v", outcome.authorization.BYOK)
			}
		} else if errors.Is(outcome.err, ErrAlreadyDispatched) {
			rejected++
		} else {
			t.Fatalf("unexpected dispatch error: %v", outcome.err)
		}
	}
	if winners != 1 || rejected != contenders-1 {
		t.Fatalf("dispatch winners=%d rejected=%d", winners, rejected)
	}
	if _, err = store.RecordProviderResult(ctx, RecordProviderResultCommand{AttemptID: first, TenantID: tenantID, CompletionToken: completionOne, Status: "failed", UsageStatus: "confirmed", ProviderRequestID: "openai-request-c6", ErrorClass: "provider_overloaded", InputTokens: 100, CostMicrounits: 0, BillableUnits: 30, CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), RecordedEvent: pointer("provider-recorded-1"), SettledEvent: pointer("usage-settled-1")}); err != nil {
		t.Fatal(err)
	}
	if _, err = billing.Reserve(ctx, billingpostgres.ReserveCommand{ReservationID: reservationTwo, RequestID: "usage-request-c6-2", TenantID: tenantID, UserID: userID, BucketID: bucket, OperationKey: "provider-attempt-c6-2", SubjectKind: "provider_attempt", SubjectID: second, SubjectVersion: 1, ReservedUnits: 100, ExpiresAt: now.Add(10 * time.Minute), CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), ReservedEvent: billingpostgres.PayloadPointer{Ref: "encrypted://usage/reserved/2", Hash: "usage-reserved-2"}}); err != nil {
		t.Fatal(err)
	}
	prepareTwo, _ := store.IssuePrepareToken()
	_, err = store.PrepareProviderAttempt(ctx, PrepareProviderAttemptCommand{AttemptID: second, ProviderAttemptID: "provider-attempt-c6-2", LLMAttemptID: llmAttempt, UsageReservationID: reservationTwo, TenantID: tenantID, UserID: userID, AttemptKey: "attempt-key-c6", Ordinal: 2, Candidate: manifest.CandidateModels[1], RequestHash: "request-c6-2", ContextManifestHash: started.ContextManifestHash, FallbackFromID: first, PrepareToken: prepareTwo, PrepareTokenExpiresAt: now.Add(time.Minute), CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), PreparedEvent: pointer("provider-prepared-2")})
	if err != nil {
		t.Fatal(err)
	}
	completionTwo, _ := store.IssueCompletionToken()
	if _, err = store.AuthorizeDispatch(ctx, AuthorizeDispatchCommand{AttemptID: second, TenantID: tenantID, RequestHash: "request-c6-2", PrepareToken: prepareTwo, CompletionToken: completionTwo, CompletionDeadline: now.Add(2 * time.Minute), CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), DispatchEvent: pointer("provider-dispatched-2")}); err != nil {
		t.Fatal(err)
	}
	visible := now.Add(2 * time.Second)
	if _, err = store.RecordProviderResult(ctx, RecordProviderResultCommand{AttemptID: second, TenantID: tenantID, CompletionToken: completionTwo, Status: "completed", ResponseHash: "response-c6-2", UsageStatus: "confirmed", ProviderRequestID: "anthropic-request-c6", InputTokens: 80, OutputTokens: 40, CostMicrounits: 30, BillableUnits: 50, VisibleOutputStartedAt: &visible, CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), RecordedEvent: pointer("provider-recorded-2"), SettledEvent: pointer("usage-settled-2")}); err != nil {
		t.Fatal(err)
	}
	finalized, err := store.FinalizeLLMAttempt(ctx, FinalizeLLMAttemptCommand{AttemptID: llmAttempt, TenantID: tenantID, Status: "completed", SelectedProviderAttemptID: second, ResultHash: "result-c6", CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), FinalizedEvent: pointer("llm-finalized")})
	if err != nil || finalized.TotalInputTokens != 180 || finalized.TotalOutputTokens != 40 || finalized.TotalCostMicrounits != 30 || len(finalized.ProviderAttemptIDs) != 2 {
		t.Fatalf("finalized=%#v err=%v", finalized, err)
	}
	unknownStarted, err := store.StartLLMAttempt(ctx, StartLLMAttemptCommand{AttemptID: unknownLLM, TenantID: tenantID, UserID: userID, RunID: runID, RunAttemptID: runAttempt, RunVersion: 3, RunFence: 1, StreamGeneration: 2, AttemptKey: "attempt-key-c6-unknown", CorrelationID: correlation, Manifest: manifest, Actor: json.RawMessage(`{"kind":"service"}`), StartedEvent: pointer("llm-unknown-started")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = billing.Reserve(ctx, billingpostgres.ReserveCommand{ReservationID: unknownReserve, RequestID: "usage-request-c6-unknown", TenantID: tenantID, UserID: userID, BucketID: bucket, OperationKey: "provider-attempt-c6-unknown", SubjectKind: "provider_attempt", SubjectID: unknownAttempt, SubjectVersion: 1, ReservedUnits: 100, ExpiresAt: now.Add(10 * time.Minute), CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), ReservedEvent: billingpostgres.PayloadPointer{Ref: "encrypted://usage/reserved/unknown", Hash: "usage-reserved-unknown"}}); err != nil {
		t.Fatal(err)
	}
	unknownPrepare, _ := store.IssuePrepareToken()
	if _, err = store.PrepareProviderAttempt(ctx, PrepareProviderAttemptCommand{AttemptID: unknownAttempt, ProviderAttemptID: "provider-attempt-c6-unknown", LLMAttemptID: unknownLLM, UsageReservationID: unknownReserve, TenantID: tenantID, UserID: userID, AttemptKey: "attempt-key-c6-unknown", Ordinal: 1, Candidate: manifest.CandidateModels[1], RequestHash: "request-c6-unknown", ContextManifestHash: unknownStarted.ContextManifestHash, PrepareToken: unknownPrepare, PrepareTokenExpiresAt: now.Add(time.Minute), CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), PreparedEvent: pointer("provider-prepared-unknown")}); err != nil {
		t.Fatal(err)
	}
	unknownCompletion, _ := store.IssueCompletionToken()
	if _, err = store.AuthorizeDispatch(ctx, AuthorizeDispatchCommand{AttemptID: unknownAttempt, TenantID: tenantID, RequestHash: "request-c6-unknown", PrepareToken: unknownPrepare, CompletionToken: unknownCompletion, CompletionDeadline: now.Add(30 * time.Second), CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), DispatchEvent: pointer("provider-dispatched-unknown")}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, err = store.MarkDispatchOutcomeUnknown(ctx, MarkOutcomeUnknownCommand{AttemptID: unknownAttempt, TenantID: tenantID, ReasonCode: "completion_deadline_exceeded", ReconciliationDueAt: now.Add(time.Hour), CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), RecordedEvent: pointer("provider-recorded-unknown")}); err != nil {
		t.Fatal(err)
	}
	var unknownStatus string
	if err = admin.QueryRow(ctx, `SELECT status FROM contracts.usage_reservations WHERE id=$1`, unknownReserve).Scan(&unknownStatus); err != nil || unknownStatus != "reserved" {
		t.Fatalf("unknown reservation status=%s err=%v", unknownStatus, err)
	}
	if _, err = store.ReconcileProviderUsage(ctx, ReconcileProviderUsageCommand{AttemptID: unknownAttempt, TenantID: tenantID, ProviderRequestID: "anthropic-request-unknown", EvidenceHash: "invoice-evidence-unknown", InputTokens: 60, OutputTokens: 10, CostMicrounits: 10, BillableUnits: 40, CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), ReconciledEvent: pointer("provider-reconciled-unknown"), SettledEvent: pointer("usage-settled-unknown")}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.FinalizeLLMAttempt(ctx, FinalizeLLMAttemptCommand{AttemptID: unknownLLM, TenantID: tenantID, Status: "failed", FailureCode: "provider_outcome_unknown", CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), FinalizedEvent: pointer("llm-unknown-finalized")}); err != nil {
		t.Fatal(err)
	}
	abandonStarted, err := store.StartLLMAttempt(ctx, StartLLMAttemptCommand{AttemptID: abandonLLM, TenantID: tenantID, UserID: userID, RunID: runID, RunAttemptID: runAttempt, RunVersion: 3, RunFence: 1, StreamGeneration: 3, AttemptKey: "attempt-key-c6-abandon", CorrelationID: correlation, Manifest: manifest, Actor: json.RawMessage(`{"kind":"service"}`), StartedEvent: pointer("llm-abandon-started")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = billing.Reserve(ctx, billingpostgres.ReserveCommand{ReservationID: abandonReserve, RequestID: "usage-request-c6-abandon", TenantID: tenantID, UserID: userID, BucketID: bucket, OperationKey: "provider-attempt-c6-abandon", SubjectKind: "provider_attempt", SubjectID: abandonAttempt, SubjectVersion: 1, ReservedUnits: 100, ExpiresAt: now.Add(10 * time.Minute), CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), ReservedEvent: billingpostgres.PayloadPointer{Ref: "encrypted://usage/reserved/abandon", Hash: "usage-reserved-abandon"}}); err != nil {
		t.Fatal(err)
	}
	abandonToken, _ := store.IssuePrepareToken()
	if _, err = store.PrepareProviderAttempt(ctx, PrepareProviderAttemptCommand{AttemptID: abandonAttempt, ProviderAttemptID: "provider-attempt-c6-abandon", LLMAttemptID: abandonLLM, UsageReservationID: abandonReserve, TenantID: tenantID, UserID: userID, AttemptKey: "attempt-key-c6-abandon", Ordinal: 1, Candidate: manifest.CandidateModels[1], RequestHash: "request-c6-abandon", ContextManifestHash: abandonStarted.ContextManifestHash, PrepareToken: abandonToken, PrepareTokenExpiresAt: now.Add(30 * time.Second), CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), PreparedEvent: pointer("provider-prepared-abandon")}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, err = store.AbandonExpiredProviderAttempt(ctx, AbandonProviderAttemptCommand{AttemptID: abandonAttempt, TenantID: tenantID, ReasonCode: "prepare_token_expired", CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), AbandonedEvent: pointer("provider-abandoned"), ReleasedEvent: pointer("usage-released-abandon")}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.FinalizeLLMAttempt(ctx, FinalizeLLMAttemptCommand{AttemptID: abandonLLM, TenantID: tenantID, Status: "failed", FailureCode: "provider_not_dispatched", CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), FinalizedEvent: pointer("llm-abandon-finalized")}); err != nil {
		t.Fatal(err)
	}
	var dispatchEvents, providerRows, ledgerRows, providerCosts int
	var reservedUnits, settledUnits uint64
	if err = admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM agent.events WHERE aggregate_kind='provider_attempt' AND event_type='ProviderAttemptDispatchAuthorized' AND aggregate_id IN ($1,$2,$3,$4)),(SELECT count(*) FROM agent.llm_provider_attempts WHERE llm_attempt_id IN ($5,$6,$7)),(SELECT count(*) FROM contracts.usage_ledger WHERE reservation_id IN ($8,$9,$10,$11)),(SELECT count(*) FROM contracts.provider_costs WHERE provider_attempt_row_id IN ($1,$2,$3,$4)),(SELECT reserved_units FROM contracts.credit_buckets WHERE id=$12),(SELECT settled_units FROM contracts.credit_buckets WHERE id=$12)`, first, second, unknownAttempt, abandonAttempt, llmAttempt, unknownLLM, abandonLLM, reservationOne, reservationTwo, unknownReserve, abandonReserve, bucket).Scan(&dispatchEvents, &providerRows, &ledgerRows, &providerCosts, &reservedUnits, &settledUnits); err != nil {
		t.Fatal(err)
	}
	if dispatchEvents != 3 || providerRows != 4 || ledgerRows != 4 || providerCosts != 3 || reservedUnits != 0 || settledUnits != 120 {
		t.Fatalf("dispatch events=%d provider rows=%d ledger=%d costs=%d reserved=%d settled=%d", dispatchEvents, providerRows, ledgerRows, providerCosts, reservedUnits, settledUnits)
	}
	var dispatchedRequests, completeProviderAttemptIDs, completeModelVersions, completeUsage, completeCosts, completeContextManifests int
	if err = admin.QueryRow(ctx, `SELECT
		count(*),
		count(*) FILTER (WHERE NULLIF(p.provider_attempt_id,'') IS NOT NULL AND NULLIF(p.provider_request_id,'') IS NOT NULL),
		count(*) FILTER (WHERE NULLIF(p.model_version,'') IS NOT NULL),
		count(*) FILTER (WHERE p.usage_status IN ('confirmed','estimated') AND p.input_tokens>=0 AND p.output_tokens>=0),
		count(*) FILTER (WHERE p.cost_microunits>=0 AND EXISTS(SELECT 1 FROM contracts.provider_costs c WHERE c.tenant_id=p.tenant_id AND c.provider_attempt_row_id=p.id AND c.provider_attempt_id=p.provider_attempt_id AND c.model_version=p.model_version AND c.input_tokens=p.input_tokens AND c.output_tokens=p.output_tokens AND c.cost_microunits=p.cost_microunits)),
		count(*) FILTER (WHERE NULLIF(p.context_manifest_hash,'') IS NOT NULL AND p.context_manifest_hash=l.context_manifest_hash AND jsonb_typeof(l.context_manifest)='object' AND jsonb_array_length(l.context_manifest->'candidate_models')>0)
		FROM agent.llm_provider_attempts p
		JOIN agent.llm_attempts l ON l.tenant_id=p.tenant_id AND l.id=p.llm_attempt_id
		WHERE p.tenant_id=$1 AND p.id IN ($2,$3,$4,$5) AND p.dispatch_event_id IS NOT NULL`, tenantID, first, second, unknownAttempt, abandonAttempt).Scan(&dispatchedRequests, &completeProviderAttemptIDs, &completeModelVersions, &completeUsage, &completeCosts, &completeContextManifests); err != nil {
		t.Fatal(err)
	}
	if dispatchedRequests != 3 || completeProviderAttemptIDs != dispatchedRequests || completeModelVersions != dispatchedRequests || completeUsage != dispatchedRequests || completeCosts != dispatchedRequests || completeContextManifests != dispatchedRequests {
		t.Fatalf("LLM manifest coverage dispatched=%d provider_attempt_id=%d model_version=%d usage=%d cost=%d context_manifest=%d", dispatchedRequests, completeProviderAttemptIDs, completeModelVersions, completeUsage, completeCosts, completeContextManifests)
	}
	manifestMetrics, err := json.Marshal(map[string]any{
		"scenario": "llm_request_manifest_completeness", "real_provider_requests": dispatchedRequests,
		"provider_attempt_id_complete": completeProviderAttemptIDs, "model_version_complete": completeModelVersions,
		"usage_complete": completeUsage, "cost_complete": completeCosts, "context_manifest_complete": completeContextManifests,
		"coverage_percent": 100, "missing_records": 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("llm_manifest_gate=%s", manifestMetrics)

	// A logical attempt and even a prepared physical attempt retain immutable
	// history after the Run terminates, but neither may cross the provider
	// dispatch boundary with the stale Run execution right.
	fencedStarted, err := store.StartLLMAttempt(ctx, StartLLMAttemptCommand{AttemptID: fencedLLM, TenantID: tenantID, UserID: userID, RunID: runID, RunAttemptID: runAttempt, RunVersion: 3, RunFence: 1, StreamGeneration: 4, AttemptKey: "attempt-key-c6-fenced", CorrelationID: correlation, Manifest: manifest, Actor: json.RawMessage(`{"kind":"service"}`), StartedEvent: pointer("llm-fenced-started")})
	if err != nil {
		t.Fatal(err)
	}
	lateStarted, err := store.StartLLMAttempt(ctx, StartLLMAttemptCommand{AttemptID: lateLLM, TenantID: tenantID, UserID: userID, RunID: runID, RunAttemptID: runAttempt, RunVersion: 3, RunFence: 1, StreamGeneration: 5, AttemptKey: "attempt-key-c6-late", CorrelationID: correlation, Manifest: manifest, Actor: json.RawMessage(`{"kind":"service"}`), StartedEvent: pointer("llm-late-started")})
	if err != nil {
		t.Fatal(err)
	}
	for _, reservation := range []billingpostgres.ReserveCommand{
		{ReservationID: fencedReserve, RequestID: "usage-request-c6-fenced", TenantID: tenantID, UserID: userID, BucketID: bucket, OperationKey: "provider-attempt-c6-fenced", SubjectKind: "provider_attempt", SubjectID: fencedAttempt, SubjectVersion: 1, ReservedUnits: 100, ExpiresAt: now.Add(10 * time.Minute), CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), ReservedEvent: billingpostgres.PayloadPointer{Ref: "encrypted://usage/reserved/fenced", Hash: "usage-reserved-fenced"}},
		{ReservationID: lateReserve, RequestID: "usage-request-c6-late", TenantID: tenantID, UserID: userID, BucketID: bucket, OperationKey: "provider-attempt-c6-late", SubjectKind: "provider_attempt", SubjectID: lateAttempt, SubjectVersion: 1, ReservedUnits: 100, ExpiresAt: now.Add(10 * time.Minute), CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), ReservedEvent: billingpostgres.PayloadPointer{Ref: "encrypted://usage/reserved/late", Hash: "usage-reserved-late"}},
	} {
		if _, err = billing.Reserve(ctx, reservation); err != nil {
			t.Fatal(err)
		}
	}
	fencedPrepare, _ := store.IssuePrepareToken()
	if _, err = store.PrepareProviderAttempt(ctx, PrepareProviderAttemptCommand{AttemptID: fencedAttempt, ProviderAttemptID: "provider-attempt-c6-fenced", LLMAttemptID: fencedLLM, UsageReservationID: fencedReserve, TenantID: tenantID, UserID: userID, AttemptKey: "attempt-key-c6-fenced", Ordinal: 1, Candidate: manifest.CandidateModels[0], RequestHash: "request-c6-fenced", ContextManifestHash: fencedStarted.ContextManifestHash, PrepareToken: fencedPrepare, PrepareTokenExpiresAt: now.Add(time.Minute), CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), PreparedEvent: pointer("provider-prepared-fenced")}); err != nil {
		t.Fatal(err)
	}
	cancelTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cancelTx.Rollback(ctx) }()
	if _, err = cancelTx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err = appender.Append(ctx, cancelTx, eventpostgres.Input{Event: eventpostgres.Event{
		ID: cancelEventID, TenantID: tenantID, UserID: userID, EventType: "RunCancellationRequested", SchemaVersion: 1,
		AggregateKind: "run_cancellation", AggregateID: cancellationID, AggregateVersion: 1, StoreEpoch: epoch,
		OccurredAt: now, Actor: json.RawMessage(`{"kind":"user"}`), CorrelationID: correlation,
		PayloadRef: "encrypted://llm/cancellation/requested", PayloadHash: strings.Repeat("a", 64),
	}, Commands: []eventpostgres.OutboxCommand{{ID: cancelOutboxID, CommandID: cancelPublish, CommandType: "events.publish", PayloadRef: "encrypted://llm/cancellation/requested", PayloadHash: strings.Repeat("a", 64)}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = cancelTx.Exec(ctx, `INSERT INTO agent.run_cancellations(
		id,tenant_id,run_id,root_cancellation_id,parent_cancellation_id,cancel_generation,status,requested_by,requested_at,reason,
		store_epoch,request_hash,request_event_id,request_payload_ref,request_payload_hash,settlement_payload_ref,settlement_payload_hash,reconciliation_due_at,created_at,updated_at
	) VALUES($1,$2,$3,$1,NULL,1,'requested',$4,$5,'user_requested',$6,$7,$8,$9,$10,$11,$12,$5,$5,$5)`, cancellationID, tenantID, runID, userID, now, epoch, strings.Repeat("b", 64), cancelEventID, "encrypted://llm/cancellation/requested", strings.Repeat("a", 64), "encrypted://llm/cancellation/settled", strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err = cancelTx.Exec(ctx, `UPDATE agent.run_cancellations SET status='terminating',version=2,updated_at=$1 WHERE tenant_id=$2 AND id=$3`, now, tenantID, cancellationID); err != nil {
		t.Fatal(err)
	}
	if _, err = cancelTx.Exec(ctx, `UPDATE agent.runs SET cancel_requested_at=$1,cancel_generation=1,active_cancellation_id=$2,current_fence=current_fence+1,updated_at=$1 WHERE tenant_id=$3 AND id=$4`, now, cancellationID, tenantID, runID); err != nil {
		t.Fatal(err)
	}
	if err = cancelTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	fencedCompletion, _ := store.IssueCompletionToken()
	if _, err = store.AuthorizeDispatch(ctx, AuthorizeDispatchCommand{AttemptID: fencedAttempt, TenantID: tenantID, RequestHash: "request-c6-fenced", PrepareToken: fencedPrepare, CompletionToken: fencedCompletion, CompletionDeadline: now.Add(time.Minute), CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), DispatchEvent: pointer("provider-dispatched-fenced")}); !errors.Is(err, ErrRunFence) {
		t.Fatalf("stale prepared attempt dispatch error=%v", err)
	}
	latePrepare, _ := store.IssuePrepareToken()
	if _, err = store.PrepareProviderAttempt(ctx, PrepareProviderAttemptCommand{AttemptID: lateAttempt, ProviderAttemptID: "provider-attempt-c6-late", LLMAttemptID: lateLLM, UsageReservationID: lateReserve, TenantID: tenantID, UserID: userID, AttemptKey: "attempt-key-c6-late", Ordinal: 1, Candidate: manifest.CandidateModels[0], RequestHash: "request-c6-late", ContextManifestHash: lateStarted.ContextManifestHash, PrepareToken: latePrepare, PrepareTokenExpiresAt: now.Add(time.Minute), CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), PreparedEvent: pointer("provider-prepared-late")}); !errors.Is(err, ErrRunFence) {
		t.Fatalf("late provider preparation error=%v", err)
	}
	if _, err = store.StartLLMAttempt(ctx, StartLLMAttemptCommand{AttemptID: cancelledStart, TenantID: tenantID, UserID: userID, RunID: runID, RunAttemptID: runAttempt, RunVersion: 3, RunFence: 2, StreamGeneration: 6, AttemptKey: "attempt-key-c6-cancelled", CorrelationID: correlation, Manifest: manifest, Actor: json.RawMessage(`{"kind":"service"}`), StartedEvent: pointer("llm-cancelled-start")}); !errors.Is(err, ErrRunFence) {
		t.Fatalf("LLM start after cancellation error=%v", err)
	}
	blobs := &llmCancellationBlobs{values: map[string][]byte{}}
	payloads := payload.EnvelopeStore{Keys: llmCancellationKeys{key: payload.Key{ID: "llm-cancellation-v1", Material: bytes.Repeat([]byte{0xca}, 32)}}, Blobs: blobs}
	converger := RunCancellationService{Store: store, Payloads: payloads}
	if err = converger.ConvergeRunCancellation(ctx, tenantID, runID, cancellationID, epoch); err != nil {
		t.Fatal(err)
	}
	var fencedProviderStatus, fencedReservationStatus, fencedLLMStatus, lateLLMStatus string
	var abandonedEvents, finalizedEvents, lateFinalizedV2Events int
	if err = admin.QueryRow(ctx, `SELECT
		(SELECT status FROM agent.llm_provider_attempts WHERE tenant_id=$1 AND id=$2),
		(SELECT status FROM contracts.usage_reservations WHERE tenant_id=$1 AND id=$3),
		(SELECT status FROM agent.llm_attempts WHERE tenant_id=$1 AND id=$4),
		(SELECT status FROM agent.llm_attempts WHERE tenant_id=$1 AND id=$5),
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_id=$2 AND event_type='ProviderAttemptAbandoned'),
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_id=$4 AND event_type='LLMAttemptFinalized' AND event_schema_version=2),
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_id=$5 AND event_type='LLMAttemptFinalized' AND event_schema_version=2)`, tenantID, fencedAttempt, fencedReserve, fencedLLM, lateLLM).Scan(&fencedProviderStatus, &fencedReservationStatus, &fencedLLMStatus, &lateLLMStatus, &abandonedEvents, &finalizedEvents, &lateFinalizedV2Events); err != nil {
		t.Fatal(err)
	}
	if fencedProviderStatus != "abandoned" || fencedReservationStatus != "released" || fencedLLMStatus != "cancelled" || lateLLMStatus != "cancelled" || abandonedEvents != 1 || finalizedEvents != 1 || lateFinalizedV2Events != 1 {
		t.Fatalf("provider=%s reservation=%s fenced_llm=%s late_llm=%s abandoned_events=%d finalized_v2_events=%d late_finalized_v2_events=%d", fencedProviderStatus, fencedReservationStatus, fencedLLMStatus, lateLLMStatus, abandonedEvents, finalizedEvents, lateFinalizedV2Events)
	}
}

type llmCancellationKeys struct{ key payload.Key }

func (provider llmCancellationKeys) Current(context.Context, string) (payload.Key, error) {
	return provider.key, nil
}

func (provider llmCancellationKeys) ByID(context.Context, string, string) (payload.Key, error) {
	return provider.key, nil
}

type llmCancellationBlobs struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (store *llmCancellationBlobs) Put(_ context.Context, key string, value []byte) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.values[key]; ok && !bytes.Equal(existing, value) {
		return "", errors.New("immutable blob conflict")
	}
	store.values[key] = append([]byte(nil), value...)
	return key, nil
}

func (store *llmCancellationBlobs) Get(_ context.Context, key string) ([]byte, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, ok := store.values[key]
	if !ok {
		return nil, errors.New("blob missing")
	}
	return append([]byte(nil), value...), nil
}

func pointer(name string) PayloadPointer {
	return PayloadPointer{Ref: "encrypted://llm/" + name, Hash: "hash-" + name}
}

type llmEpochStub struct{ epoch string }

func (stub llmEpochStub) CurrentStoreEpoch(context.Context) (string, error) { return stub.epoch, nil }

func llmPool(t *testing.T, ctx context.Context, environment string) *pgxpool.Pool {
	t.Helper()
	value := os.Getenv(environment)
	if value == "" {
		t.Fatalf("%s is required", environment)
	}
	pool, err := pgxpool.New(ctx, value)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}
