package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultPromptVersion      = "unversioned"
	defaultLedgerWriteTimeout = 5 * time.Second
	maxErrorMessageBytes      = 2048

	ledgerStatusPending       = "pending"
	ledgerStatusOK            = "ok"
	ledgerStatusProviderError = "provider_error"
	ledgerStatusUnknown       = "unknown"

	costBasisActual    = "actual"
	costBasisEstimated = "estimated"
	costBasisZero      = "zero"

	postgresUniqueViolation     = "23505"
	ledgerAttemptKeyUniqueIndex = "agent_llm_ledger_attempt_key_unique"
)

type Completer interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

type RequestPreparer interface {
	Prepare(req Request) (Request, error)
}

type Client struct {
	pool      *pgxpool.Pool
	completer Completer
	ids       IDGenerator
}

type ClientOptions struct {
	IDGenerator IDGenerator
}

func NewClient(pool *pgxpool.Pool, completer Completer, options ClientOptions) *Client {
	ids := options.IDGenerator
	if ids == nil {
		ids = NewULIDGenerator(nil)
	}
	return &Client{
		pool:      pool,
		completer: completer,
		ids:       ids,
	}
}

func (c *Client) Complete(ctx context.Context, req Request) (Response, error) {
	if err := c.validate(); err != nil {
		return Response{}, err
	}

	prepared, err := c.prepareRequest(req)
	if err != nil {
		return Response{}, err
	}

	ledgerID, err := c.ids.NewID()
	if err != nil {
		return Response{}, fmt.Errorf("generate llm ledger id: %w", err)
	}
	if prepared.AttemptKey == "" {
		prepared.AttemptKey, err = c.ids.NewID()
		if err != nil {
			return Response{}, fmt.Errorf("generate llm attempt key: %w", err)
		}
	}

	estimatedInputTokens := estimateInputTokens(prepared.Messages)
	estimatedUsage := Usage{
		InputTokens:  estimatedInputTokens,
		OutputTokens: maxInt(prepared.MaxTokens, 0),
	}
	estimatedCost := c.estimateCostUSD(prepared, estimatedUsage)

	hash, err := requestHash(prepared)
	if err != nil {
		return Response{}, err
	}

	if err := c.insertPending(ctx, ledgerID, prepared, hash, estimatedInputTokens, estimatedCost); err != nil {
		return Response{}, err
	}

	response, err := c.completer.Complete(ctx, prepared)
	if err != nil {
		if settleErr := c.settleProviderError(ctx, ledgerID, err); settleErr != nil {
			return Response{}, fmt.Errorf("llm provider call failed: %w; settle ledger: %v", err, settleErr)
		}
		return Response{}, err
	}

	if err := c.settleOK(ctx, ledgerID, prepared, estimatedInputTokens, response); err != nil {
		return Response{}, err
	}
	response.LedgerID = ledgerID
	return response, nil
}

func (c *Client) SettleUnknownPending(ctx context.Context, olderThan time.Duration) (int, error) {
	if c == nil || c.pool == nil {
		return 0, errors.New("missing database pool")
	}
	if olderThan < 0 {
		return 0, fmt.Errorf("%w: older_than cannot be negative", ErrInvalidRequest)
	}

	tag, err := c.pool.Exec(ctx, `
		UPDATE agent_llm_ledger
		SET status = $1,
			cost_usd = estimated_cost_usd,
			cost_basis = $2,
			finished_at = now(),
			updated_at = now()
		WHERE status = $3
			AND started_at <= now() - ($4::bigint * interval '1 millisecond')
	`, ledgerStatusUnknown, costBasisEstimated, ledgerStatusPending, olderThan.Milliseconds())
	if err != nil {
		return 0, fmt.Errorf("settle unknown pending llm attempts: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (c *Client) prepareRequest(req Request) (Request, error) {
	if preparer, ok := c.completer.(RequestPreparer); ok {
		prepared, err := preparer.Prepare(req)
		if err != nil {
			return Request{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
		}
		req = prepared
	}

	if req.PromptVersion == "" {
		req.PromptVersion = defaultPromptVersion
	}

	manifest, err := normalizeContextManifest(req.ContextManifest)
	if err != nil {
		return Request{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	req.ContextManifest = manifest

	if err := validateRequest(req); err != nil {
		return Request{}, err
	}
	return req, nil
}

func (c *Client) insertPending(ctx context.Context, ledgerID string, req Request, hash string, estimatedInputTokens int, estimatedCost float64) error {
	_, err := c.pool.Exec(ctx, `
		INSERT INTO agent_llm_ledger (
			id,
			user_id,
			run_id,
			surface,
			provider,
			model,
			status,
			tok_in,
			tok_out,
			max_tok_out,
			cache_read,
			cost_usd,
			estimated_cost_usd,
			cost_basis,
			attempt_key,
			request_hash,
			prompt_version,
			context_manifest
		)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, 0, $9, 0,
			$10::numeric, $11::numeric, $12, $13, $14, $15, $16::jsonb
		)
	`, ledgerID, req.UserID, nullIfEmpty(req.RunID), req.Surface, req.Provider, req.Model,
		ledgerStatusPending, estimatedInputTokens, maxInt(req.MaxTokens, 0),
		formatCostUSD(0), formatCostUSD(estimatedCost), costBasisEstimated,
		req.AttemptKey, hash, req.PromptVersion, string(req.ContextManifest))
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAttemptExists
		}
		return fmt.Errorf("insert pending llm ledger row: %w", err)
	}
	return nil
}

func (c *Client) settleOK(ctx context.Context, ledgerID string, req Request, estimatedInputTokens int, response Response) error {
	usage, costBasis := settledUsage(estimatedInputTokens, response)
	cost := c.estimateCostUSD(req, usage)
	hash, err := resultHash(response)
	if err != nil {
		return err
	}

	settleCtx, cancel := ledgerWriteContext(ctx)
	defer cancel()

	tag, err := c.pool.Exec(settleCtx, `
		UPDATE agent_llm_ledger
		SET status = $2,
			tok_in = $3,
			tok_out = $4,
			cache_read = $5,
			cost_usd = $6::numeric,
			cost_basis = $7,
			provider_request_id = $8,
			result_hash = $9,
			finished_at = now(),
			updated_at = now()
		WHERE id = $1
	`, ledgerID, ledgerStatusOK, maxInt(usage.InputTokens, 0), maxInt(usage.OutputTokens, 0),
		maxInt(usage.CacheReadTokens, 0), formatCostUSD(cost), costBasis,
		nullIfEmpty(response.ProviderRequestID), hash)
	if err != nil {
		return fmt.Errorf("settle ok llm ledger row: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("settle ok llm ledger row: no row updated")
	}
	return nil
}

func (c *Client) settleProviderError(ctx context.Context, ledgerID string, cause error) error {
	settleCtx, cancel := ledgerWriteContext(ctx)
	defer cancel()

	tag, err := c.pool.Exec(settleCtx, `
		UPDATE agent_llm_ledger
		SET status = $2,
			cost_usd = $3::numeric,
			cost_basis = $4,
			error_code = $5,
			error_message = $6,
			finished_at = now(),
			updated_at = now()
		WHERE id = $1
	`, ledgerID, ledgerStatusProviderError, formatCostUSD(0), costBasisZero,
		ledgerStatusProviderError, truncateError(cause))
	if err != nil {
		return fmt.Errorf("settle provider error llm ledger row: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("settle provider error llm ledger row: no row updated")
	}
	return nil
}

func (c *Client) estimateCostUSD(req Request, usage Usage) float64 {
	estimator, ok := c.completer.(CostEstimator)
	if !ok {
		return 0
	}
	return estimator.EstimateCostUSD(req, usage)
}

func (c *Client) validate() error {
	if c == nil || c.pool == nil {
		return errors.New("missing database pool")
	}
	if c.completer == nil {
		return errors.New("missing llm completer")
	}
	if c.ids == nil {
		return errors.New("missing id generator")
	}
	return nil
}

func validateRequest(req Request) error {
	if strings.TrimSpace(req.UserID) == "" {
		return fmt.Errorf("%w: user_id is required", ErrInvalidRequest)
	}
	if strings.TrimSpace(req.Surface) == "" {
		return fmt.Errorf("%w: surface is required", ErrInvalidRequest)
	}
	if strings.TrimSpace(req.Provider) == "" {
		return fmt.Errorf("%w: provider is required", ErrInvalidRequest)
	}
	if strings.TrimSpace(req.Model) == "" {
		return fmt.Errorf("%w: model is required", ErrInvalidRequest)
	}
	if strings.TrimSpace(req.PromptVersion) == "" {
		return fmt.Errorf("%w: prompt_version is required", ErrInvalidRequest)
	}
	if req.MaxTokens < 0 {
		return fmt.Errorf("%w: max_tokens cannot be negative", ErrInvalidRequest)
	}
	if len(req.Messages) == 0 {
		return fmt.Errorf("%w: at least one message is required", ErrInvalidRequest)
	}
	for _, message := range req.Messages {
		if strings.TrimSpace(string(message.Role)) == "" {
			return fmt.Errorf("%w: message role is required", ErrInvalidRequest)
		}
	}
	return nil
}

func settledUsage(estimatedInputTokens int, response Response) (Usage, string) {
	usage := response.Usage
	if usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.CacheReadTokens == 0 {
		return Usage{
			InputTokens:  estimatedInputTokens,
			OutputTokens: estimateTextTokens(response.Content),
		}, costBasisEstimated
	}
	return Usage{
		InputTokens:     maxInt(usage.InputTokens, 0),
		OutputTokens:    maxInt(usage.OutputTokens, 0),
		CacheReadTokens: maxInt(usage.CacheReadTokens, 0),
	}, costBasisActual
}

func ledgerWriteContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(base, defaultLedgerWriteTimeout)
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func truncateError(cause error) string {
	if cause == nil {
		return ""
	}
	message := cause.Error()
	if len(message) <= maxErrorMessageBytes {
		return message
	}
	return message[:maxErrorMessageBytes]
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == postgresUniqueViolation &&
		pgErr.ConstraintName == ledgerAttemptKeyUniqueIndex
}
