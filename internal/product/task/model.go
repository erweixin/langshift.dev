// Package task defines the deterministic daily-practice lifecycle.
package task

import (
	"errors"
	"time"
)

type Status string

const (
	Scheduled   Status = "scheduled"
	InProgress  Status = "in_progress"
	Submitted   Status = "submitted"
	Reviewing   Status = "reviewing"
	Completed   Status = "completed"
	Skipped     Status = "skipped"
	Rescheduled Status = "rescheduled"
)

var (
	ErrInvalid  = errors.New("daily task command is invalid")
	ErrConflict = errors.New("daily task command conflicts with current state")
)

type Task struct {
	ID, MissionID, RouteRevisionID string
	Version                        uint64
	Status                         Status
	ScheduledFor                   time.Time
	SubmissionID, ReviewID         string
	CompletedAt                    *time.Time
}

type TransitionCommand struct {
	ExpectedVersion uint64
	Next            Status
	SubmissionID    string
	ReviewID        string
	Now             time.Time
}

func (item Task) Transition(command TransitionCommand) (Task, error) {
	if !item.valid() || command.ExpectedVersion < 1 || command.Now.IsZero() || command.Next == "" {
		return Task{}, ErrInvalid
	}
	if item.Version != command.ExpectedVersion || !allowed(item.Status, command.Next) {
		return Task{}, ErrConflict
	}
	if command.Next == Submitted && command.SubmissionID == "" || command.Next != Submitted && command.SubmissionID != "" || command.Next == Reviewing && (item.SubmissionID == "" || command.ReviewID == "") || command.Next != Reviewing && command.Next != Completed && command.ReviewID != "" || command.Next == Completed && (item.SubmissionID == "" || item.ReviewID == "" || command.ReviewID != item.ReviewID) {
		return Task{}, ErrInvalid
	}
	next := item
	next.Version++
	next.Status = command.Next
	if command.Next == Submitted {
		next.SubmissionID = command.SubmissionID
	}
	if command.Next == Reviewing {
		next.ReviewID = command.ReviewID
	}
	if command.Next == Completed {
		completed := command.Now.UTC()
		next.CompletedAt = &completed
	}
	return next, nil
}

func (item Task) valid() bool {
	if item.ID == "" || item.MissionID == "" || item.RouteRevisionID == "" || item.Version < 1 || item.ScheduledFor.IsZero() || !validStatus(item.Status) {
		return false
	}
	if item.Status == Scheduled || item.Status == InProgress || item.Status == Skipped || item.Status == Rescheduled {
		return item.SubmissionID == "" && item.ReviewID == "" && item.CompletedAt == nil
	}
	if item.Status == Submitted {
		return item.SubmissionID != "" && item.ReviewID == "" && item.CompletedAt == nil
	}
	if item.Status == Reviewing {
		return item.SubmissionID != "" && item.ReviewID != "" && item.CompletedAt == nil
	}
	return item.SubmissionID != "" && item.ReviewID != "" && item.CompletedAt != nil
}

func allowed(from, to Status) bool {
	switch from {
	case Scheduled:
		return to == InProgress || to == Skipped || to == Rescheduled
	case InProgress:
		return to == Submitted || to == Skipped || to == Rescheduled
	case Submitted:
		return to == Reviewing
	case Reviewing:
		return to == Completed
	default:
		return false
	}
}

func validStatus(status Status) bool {
	return status == Scheduled || status == InProgress || status == Submitted || status == Reviewing || status == Completed || status == Skipped || status == Rescheduled
}
