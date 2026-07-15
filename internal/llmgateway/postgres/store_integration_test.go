//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
)

func TestProviderDispatchIsAtMostOnceAndFallbackIsFullyAccounted(t *testing.T) {
	ctx := context.Background()
	admin := llmPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := llmPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()
	now := time.Date(2026, time.July, 15, 20, 0, 0, 0, time.UTC)
	const (
		userID      = "c6000000-0000-4000-8000-000000000001"
		tenantID    = "c6000000-0000-4000-8000-000000000002"
		runID       = "c6000000-0000-4000-8000-000000000003"
		runAttempt  = "c6000000-0000-4000-8000-000000000004"
		llmAttempt  = "c6000000-0000-4000-8000-000000000005"
		first       = "c6000000-0000-4000-8000-000000000006"
		second      = "c6000000-0000-4000-8000-000000000007"
		epoch       = "c6000000-0000-4000-8000-000000000008"
		correlation = "c6000000-0000-4000-8000-000000000009"
		credential  = "c6000000-0000-4000-8000-000000000014"
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
	store := Store{Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, Epochs: llmEpochStub{epoch}, StoreEpoch: epoch, IDKey: bytes.Repeat([]byte{0xc7}, 32), TokenPepper: bytes.Repeat([]byte{0xc8}, 32), Now: func() time.Time { return now }}
	manifest := ContextManifest{
		SchemaVersion: 1,
		Run:           SnapshotBinding{ID: runID, Version: 3, Hash: "run-hash-c6"},
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
	prepareOne, _ := store.IssuePrepareToken()
	preparedOne, err := store.PrepareProviderAttempt(ctx, PrepareProviderAttemptCommand{AttemptID: first, ProviderAttemptID: "provider-attempt-c6-1", LLMAttemptID: llmAttempt, TenantID: tenantID, UserID: userID, AttemptKey: "attempt-key-c6", Ordinal: 1, Candidate: manifest.CandidateModels[0], RequestHash: "request-c6-1", ContextManifestHash: started.ContextManifestHash, PrepareToken: prepareOne, PrepareTokenExpiresAt: now.Add(time.Minute), BYOK: &BYOKBinding{CredentialID: credential, Version: 1, SecretVersion: "1"}, CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), PreparedEvent: pointer("provider-prepared-1")})
	if err != nil || preparedOne.Ordinal != 1 {
		t.Fatalf("prepare one=%#v err=%v", preparedOne, err)
	}
	completionOne, _ := store.IssueCompletionToken()
	dispatch := AuthorizeDispatchCommand{AttemptID: first, TenantID: tenantID, RequestHash: "request-c6-1", PrepareToken: prepareOne, CompletionToken: completionOne, CompletionDeadline: now.Add(2 * time.Minute), CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), DispatchEvent: pointer("provider-dispatched-1")}
	const contenders = 16
	var wait sync.WaitGroup
	results := make(chan error, contenders)
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, authorizeErr := store.AuthorizeDispatch(ctx, dispatch)
			results <- authorizeErr
		}()
	}
	wait.Wait()
	close(results)
	winners, rejected := 0, 0
	for authorizeErr := range results {
		if authorizeErr == nil {
			winners++
		} else if errors.Is(authorizeErr, ErrAlreadyDispatched) {
			rejected++
		} else {
			t.Fatalf("unexpected dispatch error: %v", authorizeErr)
		}
	}
	if winners != 1 || rejected != contenders-1 {
		t.Fatalf("dispatch winners=%d rejected=%d", winners, rejected)
	}
	if _, err = store.RecordProviderResult(ctx, RecordProviderResultCommand{AttemptID: first, TenantID: tenantID, CompletionToken: completionOne, Status: "failed", UsageStatus: "confirmed", ProviderRequestID: "openai-request-c6", ErrorClass: "provider_overloaded", InputTokens: 100, CostMicrounits: 20, CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), RecordedEvent: pointer("provider-recorded-1")}); err != nil {
		t.Fatal(err)
	}
	prepareTwo, _ := store.IssuePrepareToken()
	_, err = store.PrepareProviderAttempt(ctx, PrepareProviderAttemptCommand{AttemptID: second, ProviderAttemptID: "provider-attempt-c6-2", LLMAttemptID: llmAttempt, TenantID: tenantID, UserID: userID, AttemptKey: "attempt-key-c6", Ordinal: 2, Candidate: manifest.CandidateModels[1], RequestHash: "request-c6-2", ContextManifestHash: started.ContextManifestHash, FallbackFromID: first, PrepareToken: prepareTwo, PrepareTokenExpiresAt: now.Add(time.Minute), CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), PreparedEvent: pointer("provider-prepared-2")})
	if err != nil {
		t.Fatal(err)
	}
	completionTwo, _ := store.IssueCompletionToken()
	if _, err = store.AuthorizeDispatch(ctx, AuthorizeDispatchCommand{AttemptID: second, TenantID: tenantID, RequestHash: "request-c6-2", PrepareToken: prepareTwo, CompletionToken: completionTwo, CompletionDeadline: now.Add(2 * time.Minute), CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), DispatchEvent: pointer("provider-dispatched-2")}); err != nil {
		t.Fatal(err)
	}
	visible := now.Add(2 * time.Second)
	if _, err = store.RecordProviderResult(ctx, RecordProviderResultCommand{AttemptID: second, TenantID: tenantID, CompletionToken: completionTwo, Status: "completed", ResponseHash: "response-c6-2", UsageStatus: "confirmed", ProviderRequestID: "anthropic-request-c6", InputTokens: 80, OutputTokens: 40, CostMicrounits: 30, VisibleOutputStartedAt: &visible, CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), RecordedEvent: pointer("provider-recorded-2")}); err != nil {
		t.Fatal(err)
	}
	finalized, err := store.FinalizeLLMAttempt(ctx, FinalizeLLMAttemptCommand{AttemptID: llmAttempt, TenantID: tenantID, Status: "completed", SelectedProviderAttemptID: second, ResultHash: "result-c6", CorrelationID: correlation, Actor: json.RawMessage(`{"kind":"service"}`), FinalizedEvent: pointer("llm-finalized")})
	if err != nil || finalized.TotalInputTokens != 180 || finalized.TotalOutputTokens != 40 || finalized.TotalCostMicrounits != 50 || len(finalized.ProviderAttemptIDs) != 2 {
		t.Fatalf("finalized=%#v err=%v", finalized, err)
	}
	var dispatchEvents, providerRows int
	if err = admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM agent.events WHERE aggregate_kind='provider_attempt' AND event_type='ProviderAttemptDispatchAuthorized' AND aggregate_id IN ($1,$2)),(SELECT count(*) FROM agent.llm_provider_attempts WHERE llm_attempt_id=$3)`, first, second, llmAttempt).Scan(&dispatchEvents, &providerRows); err != nil {
		t.Fatal(err)
	}
	if dispatchEvents != 2 || providerRows != 2 {
		t.Fatalf("dispatch events=%d provider rows=%d", dispatchEvents, providerRows)
	}
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
