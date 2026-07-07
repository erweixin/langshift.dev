package event

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool       *pgxpool.Pool
	ids        IDGenerator
	dispatcher Dispatcher
}

func NewService(pool *pgxpool.Pool, options Options) *Service {
	ids := options.IDGenerator
	if ids == nil {
		ids = NewULIDGenerator(defaultIDEntropy)
	}
	dispatcher := options.Dispatcher
	if dispatcher == nil {
		dispatcher = NoopDispatcher{}
	}
	return &Service{
		pool:       pool,
		ids:        ids,
		dispatcher: dispatcher,
	}
}

func (s *Service) Append(ctx context.Context, request AppendRequest) (AppendResult, error) {
	if s == nil || s.pool == nil {
		return AppendResult{}, errMissingDatabasePool
	}
	if s.ids == nil {
		return AppendResult{}, errMissingIDGenerator
	}
	if err := validateAppendRequest(request); err != nil {
		return AppendResult{}, err
	}

	response := normalizeIdempotencyResponse(request.IdempotencyResponse)
	if request.Idempotency != nil {
		if response.Status < 100 || response.Status > 599 {
			return AppendResult{}, fmt.Errorf("%w: idempotency response status must be an HTTP status code", ErrInvalidRequest)
		}
		if !json.Valid(response.Body) {
			return AppendResult{}, fmt.Errorf("%w: idempotency response body must be valid json", ErrInvalidRequest)
		}
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AppendResult{}, fmt.Errorf("begin append transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	commandID := request.CommandID
	if request.Idempotency != nil {
		idempotencyCommandID, replay, storedResponse, err := s.reserveIdempotency(ctx, tx, request)
		if err != nil {
			return AppendResult{}, err
		}
		commandID = idempotencyCommandID
		if replay {
			return AppendResult{
				Replayed:            true,
				CommandID:           idempotencyCommandID,
				IdempotencyResponse: storedResponse,
			}, nil
		}
	}

	if request.JobFence != nil {
		jobCommandID, err := validateJobFence(ctx, tx, *request.JobFence)
		if err != nil {
			return AppendResult{}, err
		}
		commandID = jobCommandID
	}

	var runVersion *int
	deferredRunCAS := shouldDeferRunCAS(request)
	if request.Aggregate != nil && !deferredRunCAS {
		newVersion, err := advanceRunVersion(ctx, tx, request.UserID, *request.Aggregate)
		if err != nil {
			return AppendResult{}, err
		}
		runVersion = &newVersion
	}

	events, err := s.appendEvents(ctx, tx, request, commandID, func(stored StoredEvent) error {
		if !deferredRunCAS {
			return nil
		}
		if stored.Type != runAcceptedEventType || stored.RunID != request.Aggregate.RunID {
			return nil
		}
		newVersion, err := advanceRunVersion(ctx, tx, request.UserID, *request.Aggregate)
		if err != nil {
			return err
		}
		runVersion = &newVersion
		deferredRunCAS = false
		return nil
	})
	if err != nil {
		return AppendResult{}, err
	}
	if deferredRunCAS {
		return AppendResult{}, fmt.Errorf("%w: aggregate bootstrap event is required", ErrInvalidRequest)
	}

	commands, err := s.enqueueCommands(ctx, tx, request)
	if err != nil {
		return AppendResult{}, err
	}

	if err := applyEffects(ctx, tx, request.Effects); err != nil {
		return AppendResult{}, err
	}

	if request.JobFence != nil {
		if err := markJobDone(ctx, tx, *request.JobFence); err != nil {
			return AppendResult{}, err
		}
	}

	if request.Idempotency != nil {
		if err := storeIdempotencyResponse(ctx, tx, request, response); err != nil {
			return AppendResult{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return AppendResult{}, fmt.Errorf("commit append transaction: %w", err)
	}
	committed = true

	return AppendResult{
		CommandID:           commandID,
		Events:              events,
		Commands:            commands,
		IdempotencyResponse: response,
		RunVersion:          runVersion,
	}, nil
}
