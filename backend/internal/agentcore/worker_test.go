package agentcore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"lites/backend/internal/event"
	"lites/backend/internal/job"
	"lites/backend/internal/llm"
	"lites/backend/internal/run"
)

func TestWorkerProcessOneReturnsFalseWhenNoJobAvailable(t *testing.T) {
	ctx := context.Background()
	queue := &fakeQueue{claimErr: job.ErrNoJobAvailable}
	events := &fakeEvents{}
	client := &fakeLLM{}
	handler := &fakeHandler{}
	worker := NewWorker(queue, events, client, handler, WorkerOptions{
		IDGenerator: &sequenceIDs{values: []string{"attempt_1"}},
		WorkerID:    "worker_1",
	})

	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("process one: %v", err)
	}
	if processed {
		t.Fatal("processed = true, want false")
	}
	if client.called || len(events.requests) != 0 || queue.failed {
		t.Fatalf("side effects = llm:%t appends:%d failed:%t", client.called, len(events.requests), queue.failed)
	}
}

func TestWorkerProcessOneFailsClaimedJobWhenHandlerRejectsPayload(t *testing.T) {
	ctx := context.Background()
	cause := errors.New("bad payload")
	queue := &fakeQueue{claimed: testJob()}
	worker := NewWorker(queue, &fakeEvents{}, &fakeLLM{}, &fakeHandler{requestErr: cause}, WorkerOptions{
		IDGenerator: &sequenceIDs{values: []string{"attempt_1"}},
		WorkerID:    "worker_1",
	})

	processed, err := worker.ProcessOne(ctx)
	if !processed {
		t.Fatal("processed = false, want true")
	}
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, want %v", err, cause)
	}
	if !queue.failed || !errors.Is(queue.failCause, cause) {
		t.Fatalf("queue failure = %t/%v, want handler cause", queue.failed, queue.failCause)
	}
}

func TestWorkerProcessOneAppendsCompletionWithFenceAndRunCAS(t *testing.T) {
	ctx := context.Background()
	queue := &fakeQueue{claimed: testJob()}
	events := &fakeEvents{}
	client := &fakeLLM{
		response: llm.Response{
			LedgerID: "ledger_1",
			Content:  `{"ok":true}`,
		},
	}
	handler := &fakeHandler{
		append: CompletionAppend{
			UserID:             "user_1",
			RunID:              "run_1",
			ExpectedRunVersion: ExpectedRunVersion(0),
			Events: []event.EventDraft{{
				Type:          run.EventRunStarted,
				SchemaVersion: 1,
				RunID:         "run_1",
				Payload:       json.RawMessage(`{"run_id":"run_1"}`),
			}},
		},
	}
	worker := NewWorker(queue, events, client, handler, WorkerOptions{
		IDGenerator: &sequenceIDs{values: []string{"attempt_1"}},
		WorkerID:    "worker_1",
	})

	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("process one: %v", err)
	}
	if !processed {
		t.Fatal("processed = false, want true")
	}
	if !client.called || client.request.AttemptKey != "attempt_1" {
		t.Fatalf("llm request = %+v", client.request)
	}
	if len(events.requests) != 1 {
		t.Fatalf("append count = %d, want 1", len(events.requests))
	}
	request := events.requests[0]
	if request.Actor.Kind != event.ActorWorker || request.Actor.ID != "worker_1" {
		t.Fatalf("actor = %+v", request.Actor)
	}
	if request.UserID != "user_1" || request.JobFence == nil || request.JobFence.JobID != "job_1" || request.JobFence.LeaseToken != "lease_1" {
		t.Fatalf("append fence/user = %s/%+v", request.UserID, request.JobFence)
	}
	if request.Aggregate == nil || request.Aggregate.RunID != "run_1" || request.Aggregate.ExpectedVersion != 0 {
		t.Fatalf("aggregate = %+v", request.Aggregate)
	}
	if len(request.Events) != 1 || request.Events[0].Type != run.EventRunStarted {
		t.Fatalf("events = %+v", request.Events)
	}
	if queue.failed {
		t.Fatalf("queue failed with %v", queue.failCause)
	}
}

func TestWorkerProcessOneAcknowledgesStaleRunWithoutFailingJob(t *testing.T) {
	ctx := context.Background()
	queue := &fakeQueue{claimed: testJob()}
	events := &fakeEvents{appendErrs: []error{run.ErrInvalidTransition, nil}}
	handler := &fakeHandler{
		append: CompletionAppend{
			UserID:             "user_1",
			RunID:              "run_1",
			ExpectedRunVersion: ExpectedRunVersion(0),
			Events: []event.EventDraft{{
				Type:          run.EventRunSucceeded,
				SchemaVersion: 1,
				RunID:         "run_1",
				Payload:       json.RawMessage(`{"run_id":"run_1"}`),
			}},
		},
	}
	worker := NewWorker(queue, events, &fakeLLM{}, handler, WorkerOptions{
		IDGenerator: &sequenceIDs{values: []string{"attempt_1"}},
		WorkerID:    "worker_1",
	})

	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("process one: %v", err)
	}
	if !processed {
		t.Fatal("processed = false, want true")
	}
	if len(events.requests) != 2 {
		t.Fatalf("append count = %d, want completion append + stale ack", len(events.requests))
	}
	ack := events.requests[1]
	if ack.Aggregate != nil || len(ack.Events) != 0 {
		t.Fatalf("ack append = %+v, want fence-only append", ack)
	}
	if ack.JobFence == nil || ack.JobFence.JobID != "job_1" || ack.JobFence.LeaseToken != "lease_1" {
		t.Fatalf("ack fence = %+v", ack.JobFence)
	}
	if queue.failed {
		t.Fatalf("queue failed with %v", queue.failCause)
	}
}

func TestWorkerProcessOneFailsJobWhenAppendFails(t *testing.T) {
	ctx := context.Background()
	appendErr := errors.New("append failed")
	queue := &fakeQueue{claimed: testJob()}
	events := &fakeEvents{appendErrs: []error{appendErr}}
	handler := &fakeHandler{
		append: CompletionAppend{
			UserID:             "user_1",
			RunID:              "run_1",
			ExpectedRunVersion: ExpectedRunVersion(0),
			Events: []event.EventDraft{{
				Type:          run.EventRunFailed,
				SchemaVersion: 1,
				RunID:         "run_1",
				Payload:       json.RawMessage(`{"run_id":"run_1"}`),
			}},
		},
	}
	worker := NewWorker(queue, events, &fakeLLM{}, handler, WorkerOptions{
		IDGenerator: &sequenceIDs{values: []string{"attempt_1"}},
		WorkerID:    "worker_1",
	})

	processed, err := worker.ProcessOne(ctx)
	if !processed {
		t.Fatal("processed = false, want true")
	}
	if !errors.Is(err, appendErr) {
		t.Fatalf("err = %v, want append err", err)
	}
	if !queue.failed || !errors.Is(queue.failCause, appendErr) {
		t.Fatalf("queue failure = %t/%v, want append err", queue.failed, queue.failCause)
	}
}

type fakeQueue struct {
	claimed   job.Job
	claimErr  error
	failed    bool
	failFence event.JobFence
	failCause error
}

func (q *fakeQueue) Claim(context.Context, []string, string) (job.Job, event.JobFence, error) {
	if q.claimErr != nil {
		return job.Job{}, event.JobFence{}, q.claimErr
	}
	claimed := q.claimed
	if claimed.JobID == "" {
		claimed = testJob()
	}
	return claimed, event.JobFence{JobID: "job_1", LeaseToken: "lease_1"}, nil
}

func (q *fakeQueue) Fail(_ context.Context, fence event.JobFence, cause error) error {
	q.failed = true
	q.failFence = fence
	q.failCause = cause
	return nil
}

type fakeEvents struct {
	requests   []event.AppendRequest
	appendErrs []error
}

func (e *fakeEvents) Append(_ context.Context, request event.AppendRequest) (event.AppendResult, error) {
	e.requests = append(e.requests, request)
	if len(e.appendErrs) == 0 {
		return event.AppendResult{}, nil
	}
	err := e.appendErrs[0]
	e.appendErrs = e.appendErrs[1:]
	if err != nil {
		return event.AppendResult{}, err
	}
	return event.AppendResult{}, nil
}

type fakeLLM struct {
	called   bool
	request  llm.Request
	response llm.Response
	err      error
}

func (f *fakeLLM) Complete(_ context.Context, request llm.Request) (llm.Response, error) {
	f.called = true
	f.request = request
	if f.err != nil {
		return llm.Response{}, f.err
	}
	return f.response, nil
}

type fakeHandler struct {
	kinds      []string
	requestErr error
	append     CompletionAppend
	appendErr  error
}

func (h *fakeHandler) JobKinds() []string {
	if len(h.kinds) > 0 {
		return h.kinds
	}
	return []string{"test_agent_job"}
}

func (h *fakeHandler) BuildLLMRequest(_ context.Context, task JobContext) (llm.Request, error) {
	if h.requestErr != nil {
		return llm.Request{}, h.requestErr
	}
	return llm.Request{
		UserID:          task.Job.SubjectUserID,
		RunID:           "run_1",
		Surface:         "test_surface",
		AttemptKey:      task.AttemptKey,
		PromptVersion:   "test-prompt-v0",
		ContextManifest: json.RawMessage(`{"test":true}`),
		Messages:        []llm.Message{{Role: llm.RoleUser, Content: "test"}},
		JSONMode:        true,
		SchemaName:      "test_schema",
	}, nil
}

func (h *fakeHandler) BuildAppendRequest(context.Context, Completion) (CompletionAppend, error) {
	if h.appendErr != nil {
		return CompletionAppend{}, h.appendErr
	}
	return h.append, nil
}

type sequenceIDs struct {
	values []string
	next   int
}

func (g *sequenceIDs) NewID() (string, error) {
	if g.next >= len(g.values) {
		return "generated", nil
	}
	id := g.values[g.next]
	g.next++
	return id, nil
}

func testJob() job.Job {
	now := time.Now().UTC()
	return job.Job{
		JobID:         "job_1",
		CommandID:     "command_1",
		Kind:          "test_agent_job",
		SubjectUserID: "user_1",
		Payload:       json.RawMessage(`{"run_id":"run_1"}`),
		Status:        "leased",
		Attempts:      1,
		LeaseToken:    "lease_1",
		LeasedBy:      "worker_1",
		DueAt:         now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}
