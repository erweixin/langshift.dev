package agentcore

import (
	"context"
	"errors"
	"time"

	"lites/backend/internal/event"
	"lites/backend/internal/job"
	"lites/backend/internal/llm"
)

const (
	defaultWorkerID        = "agentcore-worker"
	defaultIdleSleepMillis = 1000
	defaultHeartbeatMillis = 30000
)

var (
	ErrInvalidRequest = errors.New("invalid agent core request")
	errMissingQueue   = errors.New("missing agent job queue")
	errMissingEvents  = errors.New("missing agent event appender")
	errMissingLLM     = errors.New("missing agent llm client")
	errMissingHandler = errors.New("missing agent handler")
	errMissingIDs     = errors.New("missing agent id generator")
)

type Queue interface {
	Claim(ctx context.Context, kinds []string, workerID string) (job.Job, event.JobFence, error)
	Heartbeat(ctx context.Context, fence event.JobFence) error
	Fail(ctx context.Context, fence event.JobFence, cause error) error
}

type EventAppender interface {
	Append(ctx context.Context, request event.AppendRequest) (event.AppendResult, error)
}

type LLM interface {
	Complete(ctx context.Context, req llm.Request) (llm.Response, error)
}

type Handler interface {
	JobKinds() []string
	BuildLLMRequest(ctx context.Context, job JobContext) (llm.Request, error)
	BuildAppendRequest(ctx context.Context, completion Completion) (CompletionAppend, error)
}

type JobContext struct {
	Job        job.Job
	AttemptKey string
}

type Completion struct {
	Job        job.Job
	AttemptKey string
	Request    llm.Request
	Response   llm.Response
	Err        error
}

type CompletionAppend struct {
	UserID             string
	RunID              string
	ExpectedRunVersion *int
	Events             []event.EventDraft
	Effects            []event.TxEffect
}

type WorkerOptions struct {
	IDGenerator       event.IDGenerator
	WorkerID          string
	IdleSleep         time.Duration
	HeartbeatInterval time.Duration
}

func ExpectedRunVersion(version int) *int {
	return &version
}
