package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"lites/backend/internal/llm"
	"lites/backend/internal/testsupport"
)

func TestClientCompleteWritesPendingAndSettlesOK(t *testing.T) {
	ctx := context.Background()
	pool := testsupport.NewMigratedPool(t, ctx, "test_llm")
	completer := &fakeCompleter{
		response: llm.Response{
			Content:           `{"lesson":"ok"}`,
			Usage:             llm.Usage{InputTokens: 12, OutputTokens: 7, CacheReadTokens: 3},
			ProviderRequestID: "provider_123",
			FinishReason:      "stop",
			Raw:               json.RawMessage(`{"id":"provider_123"}`),
		},
	}
	client := llm.NewClient(pool, completer, llm.ClientOptions{
		IDGenerator: testsupport.NewSequenceIDs("ledger_ok"),
	})

	response, err := client.Complete(ctx, llm.Request{
		UserID:          "user_1",
		RunID:           "run_1",
		Surface:         "lesson_generation",
		Provider:        "fake",
		Model:           "fake-model",
		AttemptKey:      "attempt_ok",
		PromptVersion:   "prompt_v1",
		ContextManifest: json.RawMessage(`{"b":2, "a":1}`),
		MaxTokens:       100,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "teach English"},
			{Role: llm.RoleUser, Content: "make a lesson"},
		},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	if response.LedgerID != "ledger_ok" {
		t.Fatalf("response ledger id = %q, want ledger_ok", response.LedgerID)
	}
	if completer.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", completer.calls)
	}
	if completer.request.AttemptKey != "attempt_ok" {
		t.Fatalf("provider attempt key = %q, want attempt_ok", completer.request.AttemptKey)
	}

	var row ledgerRow
	readLedgerRow(t, ctx, pool, "ledger_ok", &row)
	if row.Status != "ok" {
		t.Fatalf("status = %q, want ok", row.Status)
	}
	if row.UserID != "user_1" || row.RunID != "run_1" {
		t.Fatalf("ledger ownership = user:%q run:%q, want user_1/run_1", row.UserID, row.RunID)
	}
	if row.Surface != "lesson_generation" || row.Provider != "fake" || row.Model != "fake-model" {
		t.Fatalf("route fields = %q/%q/%q", row.Surface, row.Provider, row.Model)
	}
	if row.TokIn != 12 || row.TokOut != 7 || row.CacheRead != 3 || row.MaxTokOut != 100 {
		t.Fatalf("tokens = in:%d out:%d cache:%d max:%d", row.TokIn, row.TokOut, row.CacheRead, row.MaxTokOut)
	}
	if row.CostUSD != "0.19000000" || row.CostBasis != "actual" {
		t.Fatalf("cost = %s/%s, want 0.19000000/actual", row.CostUSD, row.CostBasis)
	}
	if row.EstimatedCostUSD == "0.00000000" {
		t.Fatalf("estimated cost should be populated")
	}
	if row.AttemptKey != "attempt_ok" || row.PromptVersion != "prompt_v1" {
		t.Fatalf("attempt/prompt = %q/%q", row.AttemptKey, row.PromptVersion)
	}
	if len(row.RequestHash) != 64 || len(row.ResultHash) != 64 {
		t.Fatalf("hash lengths = request:%d result:%d, want 64/64", len(row.RequestHash), len(row.ResultHash))
	}
	if row.ProviderRequestID != "provider_123" {
		t.Fatalf("provider request id = %q, want provider_123", row.ProviderRequestID)
	}
	if !row.Finished {
		t.Fatal("finished_at should be set")
	}

	var manifest map[string]int
	if err := json.Unmarshal([]byte(row.ContextManifest), &manifest); err != nil {
		t.Fatalf("unmarshal context_manifest: %v", err)
	}
	if manifest["a"] != 1 || manifest["b"] != 2 {
		t.Fatalf("context manifest = %#v, want a/b", manifest)
	}
}

func TestClientCompleteSettlesProviderError(t *testing.T) {
	ctx := context.Background()
	pool := testsupport.NewMigratedPool(t, ctx, "test_llm")
	providerErr := errors.New("provider unavailable")
	completer := &fakeCompleter{err: providerErr}
	client := llm.NewClient(pool, completer, llm.ClientOptions{
		IDGenerator: testsupport.NewSequenceIDs("ledger_error"),
	})

	_, err := client.Complete(ctx, llm.Request{
		UserID:        "user_1",
		Surface:       "lesson_generation",
		Provider:      "fake",
		Model:         "fake-model",
		AttemptKey:    "attempt_error",
		PromptVersion: "prompt_v1",
		MaxTokens:     100,
		Messages:      []llm.Message{{Role: llm.RoleUser, Content: "make a lesson"}},
	})
	if !errors.Is(err, providerErr) {
		t.Fatalf("complete error = %v, want provider error", err)
	}
	if completer.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", completer.calls)
	}

	var status string
	var costUSD string
	var costBasis string
	var errorCode string
	var errorMessage string
	var finished bool
	if err := pool.QueryRow(ctx, `
		SELECT status, cost_usd::text, cost_basis, COALESCE(error_code, ''),
			COALESCE(error_message, ''), finished_at IS NOT NULL
		FROM agent_llm_ledger
		WHERE id = $1
	`, "ledger_error").Scan(&status, &costUSD, &costBasis, &errorCode, &errorMessage, &finished); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if status != "provider_error" || costUSD != "0.00000000" || costBasis != "zero" {
		t.Fatalf("settled error = %q/%s/%s, want provider_error/0/zero", status, costUSD, costBasis)
	}
	if errorCode != "provider_error" || errorMessage != "provider unavailable" {
		t.Fatalf("error fields = %q/%q", errorCode, errorMessage)
	}
	if !finished {
		t.Fatal("finished_at should be set")
	}
}

func TestClientSettleUnknownPending(t *testing.T) {
	ctx := context.Background()
	pool := testsupport.NewMigratedPool(t, ctx, "test_llm")
	client := llm.NewClient(pool, &fakeCompleter{}, llm.ClientOptions{})

	insertPendingLedger(t, ctx, pool, "old_pending", "old_attempt", "0.42000000", "2 hours")
	insertPendingLedger(t, ctx, pool, "recent_pending", "recent_attempt", "0.99000000", "1 minute")

	updated, err := client.SettleUnknownPending(ctx, time.Hour)
	if err != nil {
		t.Fatalf("settle unknown: %v", err)
	}
	if updated != 1 {
		t.Fatalf("updated = %d, want 1", updated)
	}

	var oldStatus string
	var oldCost string
	var oldBasis string
	var oldFinished bool
	if err := pool.QueryRow(ctx, `
		SELECT status, cost_usd::text, cost_basis, finished_at IS NOT NULL
		FROM agent_llm_ledger
		WHERE id = $1
	`, "old_pending").Scan(&oldStatus, &oldCost, &oldBasis, &oldFinished); err != nil {
		t.Fatalf("read old pending: %v", err)
	}
	if oldStatus != "unknown" || oldCost != "0.42000000" || oldBasis != "estimated" || !oldFinished {
		t.Fatalf("old pending = %q/%s/%s/%t", oldStatus, oldCost, oldBasis, oldFinished)
	}

	var recentStatus string
	var recentFinished bool
	if err := pool.QueryRow(ctx, `
		SELECT status, finished_at IS NOT NULL
		FROM agent_llm_ledger
		WHERE id = $1
	`, "recent_pending").Scan(&recentStatus, &recentFinished); err != nil {
		t.Fatalf("read recent pending: %v", err)
	}
	if recentStatus != "pending" || recentFinished {
		t.Fatalf("recent pending = %q/%t, want pending/false", recentStatus, recentFinished)
	}
}

func TestClientDuplicateAttemptKeyDoesNotCallProviderAgain(t *testing.T) {
	ctx := context.Background()
	pool := testsupport.NewMigratedPool(t, ctx, "test_llm")
	completer := &fakeCompleter{
		response: llm.Response{
			Content: "ok",
			Usage:   llm.Usage{InputTokens: 5, OutputTokens: 5},
		},
	}
	client := llm.NewClient(pool, completer, llm.ClientOptions{
		IDGenerator: testsupport.NewSequenceIDs("ledger_first", "ledger_second"),
	})

	request := llm.Request{
		UserID:        "user_1",
		Surface:       "lesson_generation",
		Provider:      "fake",
		Model:         "fake-model",
		AttemptKey:    "same_attempt",
		PromptVersion: "prompt_v1",
		MaxTokens:     100,
		Messages:      []llm.Message{{Role: llm.RoleUser, Content: "make a lesson"}},
	}

	if _, err := client.Complete(ctx, request); err != nil {
		t.Fatalf("first complete: %v", err)
	}
	if _, err := client.Complete(ctx, request); !errors.Is(err, llm.ErrAttemptExists) {
		t.Fatalf("second complete error = %v, want %v", err, llm.ErrAttemptExists)
	}
	if completer.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", completer.calls)
	}
}

type fakeCompleter struct {
	request  llm.Request
	response llm.Response
	err      error
	calls    int
}

func (c *fakeCompleter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	c.calls++
	c.request = req
	if c.err != nil {
		return llm.Response{}, c.err
	}
	return c.response, nil
}

func (c *fakeCompleter) EstimateCostUSD(_ llm.Request, usage llm.Usage) float64 {
	return float64(usage.InputTokens+usage.OutputTokens) / 100
}

type ledgerRow struct {
	UserID            string
	RunID             string
	Surface           string
	Provider          string
	Model             string
	Status            string
	TokIn             int
	TokOut            int
	MaxTokOut         int
	CacheRead         int
	CostUSD           string
	EstimatedCostUSD  string
	CostBasis         string
	AttemptKey        string
	RequestHash       string
	PromptVersion     string
	ContextManifest   string
	ProviderRequestID string
	ResultHash        string
	Finished          bool
}

func readLedgerRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string, row *ledgerRow) {
	t.Helper()

	if err := pool.QueryRow(ctx, `
		SELECT user_id,
			COALESCE(run_id, ''),
			surface,
			provider,
			model,
			status,
			tok_in,
			tok_out,
			max_tok_out,
			cache_read,
			cost_usd::text,
			estimated_cost_usd::text,
			cost_basis,
			attempt_key,
			request_hash,
			prompt_version,
			context_manifest::text,
			COALESCE(provider_request_id, ''),
			COALESCE(result_hash, ''),
			finished_at IS NOT NULL
		FROM agent_llm_ledger
		WHERE id = $1
	`, id).Scan(
		&row.UserID,
		&row.RunID,
		&row.Surface,
		&row.Provider,
		&row.Model,
		&row.Status,
		&row.TokIn,
		&row.TokOut,
		&row.MaxTokOut,
		&row.CacheRead,
		&row.CostUSD,
		&row.EstimatedCostUSD,
		&row.CostBasis,
		&row.AttemptKey,
		&row.RequestHash,
		&row.PromptVersion,
		&row.ContextManifest,
		&row.ProviderRequestID,
		&row.ResultHash,
		&row.Finished,
	); err != nil {
		t.Fatalf("read ledger row %s: %v", id, err)
	}
}

func insertPendingLedger(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string, attemptKey string, estimatedCost string, age string) {
	t.Helper()

	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_llm_ledger (
			id,
			user_id,
			surface,
			provider,
			model,
			status,
			tok_in,
			max_tok_out,
			cost_usd,
			estimated_cost_usd,
			cost_basis,
			attempt_key,
			request_hash,
			prompt_version,
			context_manifest,
			started_at
		)
		VALUES (
			$1, 'user_1', 'lesson_generation', 'fake', 'fake-model', 'pending',
			10, 100, 0, $3::numeric, 'estimated', $2, 'hash_' || $1,
			'prompt_v1', '{}'::jsonb, now() - ($4::interval)
		)
	`, id, attemptKey, estimatedCost, age); err != nil {
		t.Fatalf("insert pending ledger %s: %v", id, err)
	}
}
