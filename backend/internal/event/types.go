package event

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidRequest      = errors.New("invalid append request")
	ErrIdempotencyConflict = errors.New("idempotency key reused with a different request")
	ErrIdempotencyInFlight = errors.New("idempotency key has no stored response")
	ErrJobFenceInvalid     = errors.New("job fence is invalid")
	ErrRunVersionConflict  = errors.New("run version conflict")
	ErrProjectionDispatch  = errors.New("projection dispatch failed")
	errMissingIDGenerator  = errors.New("missing id generator")
	errMissingDatabasePool = errors.New("missing database pool")
)

const httpStatusOK = 200

type ActorKind string

const (
	ActorUser   ActorKind = "user"
	ActorWorker ActorKind = "worker"
	ActorSystem ActorKind = "system"
)

type Actor struct {
	Kind ActorKind
	ID   string
}

type Idempotency struct {
	Scope       string
	Key         string
	RequestHash string
}

type IdempotencyResponse struct {
	Status int
	Body   json.RawMessage
}

type RunAggregate struct {
	RunID           string
	ExpectedVersion int
}

type JobFence struct {
	JobID      string
	LeaseToken string
}

type EventDraft struct {
	Type           string
	SchemaVersion  int
	MissionID      string
	TaskID         string
	RunID          string
	ConversationID string
	CausationID    string
	CorrelationID  string
	Payload        json.RawMessage
}

type CommandDraft struct {
	Kind          string
	SubjectUserID string
	Payload       json.RawMessage
	DueAt         *time.Time
}

type AppendRequest struct {
	Actor               Actor
	UserID              string
	CommandID           string
	Idempotency         *Idempotency
	IdempotencyResponse *IdempotencyResponse
	Aggregate           *RunAggregate
	JobFence            *JobFence
	Events              []EventDraft
	Commands            []CommandDraft
}

type AppendResult struct {
	Replayed            bool
	CommandID           string
	Events              []StoredEvent
	Commands            []StoredCommand
	IdempotencyResponse *IdempotencyResponse
	RunVersion          *int
}

type StoredEvent struct {
	EventID        string
	Seq            int64
	Type           string
	SchemaVersion  int
	UserID         string
	MissionID      string
	TaskID         string
	RunID          string
	ConversationID string
	CommandID      string
	CausationID    string
	CorrelationID  string
	CreatedAt      time.Time
	Payload        json.RawMessage
}

type StoredCommand struct {
	JobID         string
	CommandID     string
	Kind          string
	SubjectUserID string
	DueAt         time.Time
	Payload       json.RawMessage
}

type IDGenerator interface {
	NewID() (string, error)
}

type Dispatcher interface {
	Apply(ctx context.Context, tx pgx.Tx, event StoredEvent) error
}

type Options struct {
	IDGenerator IDGenerator
	Dispatcher  Dispatcher
}
