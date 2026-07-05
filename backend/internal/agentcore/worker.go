package agentcore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"lites/backend/internal/event"
	"lites/backend/internal/job"
	"lites/backend/internal/run"
)

type Worker struct {
	queue     Queue
	events    EventAppender
	llm       LLM
	handler   Handler
	ids       event.IDGenerator
	workerID  string
	idleSleep time.Duration
}

func NewWorker(queue Queue, events EventAppender, llmClient LLM, handler Handler, options WorkerOptions) *Worker {
	ids := options.IDGenerator
	if ids == nil {
		ids = event.NewULIDGenerator(nil)
	}
	workerID := strings.TrimSpace(options.WorkerID)
	if workerID == "" {
		workerID = defaultWorkerID
	}
	idleSleep := options.IdleSleep
	if idleSleep <= 0 {
		idleSleep = time.Duration(defaultIdleSleepMillis) * time.Millisecond
	}
	return &Worker{
		queue:     queue,
		events:    events,
		llm:       llmClient,
		handler:   handler,
		ids:       ids,
		workerID:  workerID,
		idleSleep: idleSleep,
	}
}

func (w *Worker) Run(ctx context.Context) {
	for {
		processed, err := w.ProcessOne(ctx)
		if err != nil {
			slog.Error("agent core worker iteration failed", "worker_id", w.workerID, "error", err)
		}
		if ctx.Err() != nil {
			return
		}
		if processed {
			continue
		}

		timer := time.NewTimer(w.idleSleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	if err := w.validate(); err != nil {
		return false, err
	}

	claimed, fence, err := w.queue.Claim(ctx, w.handler.JobKinds(), w.workerID)
	if err != nil {
		if errors.Is(err, job.ErrNoJobAvailable) {
			return false, nil
		}
		return false, err
	}

	attemptKey, err := w.ids.NewID()
	if err != nil {
		return true, w.failClaimedJob(ctx, fence, fmt.Errorf("generate agent attempt key: %w", err))
	}

	request, err := w.handler.BuildLLMRequest(ctx, JobContext{
		Job:        claimed,
		AttemptKey: attemptKey,
	})
	if err != nil {
		return true, w.failClaimedJob(ctx, fence, err)
	}
	if strings.TrimSpace(request.UserID) == "" {
		return true, w.failClaimedJob(ctx, fence, fmt.Errorf("%w: llm request user_id is required", ErrInvalidRequest))
	}
	if strings.TrimSpace(request.RunID) == "" {
		return true, w.failClaimedJob(ctx, fence, fmt.Errorf("%w: llm request run_id is required", ErrInvalidRequest))
	}

	response, llmErr := w.llm.Complete(ctx, request)
	appendRequest, err := w.handler.BuildAppendRequest(ctx, Completion{
		Job:        claimed,
		AttemptKey: attemptKey,
		Request:    request,
		Response:   response,
		Err:        llmErr,
	})
	if err != nil {
		return true, w.failClaimedJob(ctx, fence, err)
	}

	if err := w.appendCompletion(ctx, fence, appendRequest); err != nil {
		if isStaleRunAppend(err) {
			if ackErr := w.ackStaleJob(ctx, appendRequest.UserID, fence); ackErr != nil {
				return true, fmt.Errorf("ack stale agent job: %w", ackErr)
			}
			return true, nil
		}
		if failErr := w.queue.Fail(ctx, fence, err); failErr != nil {
			return true, fmt.Errorf("fail agent job after append error: %w", failErr)
		}
		return true, err
	}
	return true, nil
}

func (w *Worker) appendCompletion(ctx context.Context, fence event.JobFence, completion CompletionAppend) error {
	if strings.TrimSpace(completion.UserID) == "" {
		return fmt.Errorf("%w: append user_id is required", ErrInvalidRequest)
	}
	request := event.AppendRequest{
		Actor: event.Actor{
			Kind: event.ActorWorker,
			ID:   w.workerID,
		},
		UserID: completion.UserID,
		JobFence: &event.JobFence{
			JobID:      fence.JobID,
			LeaseToken: fence.LeaseToken,
		},
		Events: completion.Events,
	}
	if completion.ExpectedRunVersion != nil {
		if strings.TrimSpace(completion.RunID) == "" {
			return fmt.Errorf("%w: append run_id is required when run CAS is requested", ErrInvalidRequest)
		}
		request.Aggregate = &event.RunAggregate{
			RunID:           completion.RunID,
			ExpectedVersion: *completion.ExpectedRunVersion,
		}
	}

	_, err := w.events.Append(ctx, request)
	return err
}

func (w *Worker) ackStaleJob(ctx context.Context, userID string, fence event.JobFence) error {
	if strings.TrimSpace(userID) == "" {
		return fmt.Errorf("%w: stale ack user_id is required", ErrInvalidRequest)
	}
	_, err := w.events.Append(ctx, event.AppendRequest{
		Actor: event.Actor{
			Kind: event.ActorWorker,
			ID:   w.workerID,
		},
		UserID: userID,
		JobFence: &event.JobFence{
			JobID:      fence.JobID,
			LeaseToken: fence.LeaseToken,
		},
	})
	return err
}

func (w *Worker) failClaimedJob(ctx context.Context, fence event.JobFence, cause error) error {
	if err := w.queue.Fail(ctx, fence, cause); err != nil {
		return fmt.Errorf("fail invalid agent job: %w", err)
	}
	return cause
}

func (w *Worker) validate() error {
	if w == nil || w.queue == nil {
		return errMissingQueue
	}
	if w.events == nil {
		return errMissingEvents
	}
	if w.llm == nil {
		return errMissingLLM
	}
	if w.handler == nil {
		return errMissingHandler
	}
	if w.ids == nil {
		return errMissingIDs
	}
	if strings.TrimSpace(w.workerID) == "" {
		return fmt.Errorf("%w: worker_id is required", ErrInvalidRequest)
	}
	if len(w.handler.JobKinds()) == 0 {
		return fmt.Errorf("%w: handler must claim at least one job kind", ErrInvalidRequest)
	}
	return nil
}

func isStaleRunAppend(err error) bool {
	return errors.Is(err, event.ErrRunVersionConflict) || errors.Is(err, run.ErrInvalidTransition)
}
