package task

import (
	"errors"
	"testing"
	"time"
)

func TestDailyTaskLifecycle(t *testing.T) {
	now := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	item := Task{ID: "task", MissionID: "mission", RouteRevisionID: "route", Version: 1, Status: Scheduled, ScheduledFor: now}
	var err error
	item, err = item.Transition(TransitionCommand{ExpectedVersion: 1, Next: InProgress, Now: now})
	if err != nil || item.Version != 2 || item.Status != InProgress {
		t.Fatalf("start=%#v err=%v", item, err)
	}
	item, err = item.Transition(TransitionCommand{ExpectedVersion: 2, Next: Submitted, SubmissionID: "submission", Now: now})
	if err != nil || item.SubmissionID != "submission" {
		t.Fatalf("submit=%#v err=%v", item, err)
	}
	item, err = item.Transition(TransitionCommand{ExpectedVersion: 3, Next: Reviewing, ReviewID: "review", Now: now})
	if err != nil || item.ReviewID != "review" {
		t.Fatalf("review=%#v err=%v", item, err)
	}
	item, err = item.Transition(TransitionCommand{ExpectedVersion: 4, Next: Completed, ReviewID: "review", Now: now})
	if err != nil || item.Status != Completed || item.CompletedAt == nil {
		t.Fatalf("complete=%#v err=%v", item, err)
	}
	if _, err = item.Transition(TransitionCommand{ExpectedVersion: 5, Next: InProgress, Now: now}); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal task moved backwards: %v", err)
	}
}

func TestDailyTaskRejectsSkippedPathsAndUnboundReview(t *testing.T) {
	now := time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC)
	base := Task{ID: "task", MissionID: "mission", RouteRevisionID: "route", Version: 1, Status: Scheduled, ScheduledFor: now}
	for _, next := range []Status{Submitted, Reviewing, Completed} {
		if _, err := base.Transition(TransitionCommand{ExpectedVersion: 1, Next: next, SubmissionID: "submission", ReviewID: "review", Now: now}); err == nil {
			t.Fatalf("scheduled task moved directly to %s", next)
		}
	}
	started, _ := base.Transition(TransitionCommand{ExpectedVersion: 1, Next: InProgress, Now: now})
	submitted, _ := started.Transition(TransitionCommand{ExpectedVersion: 2, Next: Submitted, SubmissionID: "submission", Now: now})
	if _, err := submitted.Transition(TransitionCommand{ExpectedVersion: 3, Next: Reviewing, Now: now}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reviewing without a review binding: %v", err)
	}
}
