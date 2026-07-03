package job

import (
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrInvalidRequest  = errors.New("invalid job queue request")
	ErrNoJobAvailable  = errors.New("no job available")
	ErrJobFenceInvalid = errors.New("job fence is invalid")
	errMissingPool     = errors.New("missing database pool")
	errMissingIDs      = errors.New("missing id generator")
)

const (
	defaultLeaseDuration = 5 * time.Minute
	defaultMaxAttempts   = 5
	maxLastErrorBytes    = 2048
)

type IDGenerator interface {
	NewID() (string, error)
}

type Options struct {
	IDGenerator   IDGenerator
	LeaseDuration time.Duration
	MaxAttempts   int
}

type Job struct {
	JobID         string
	CommandID     string
	Kind          string
	SubjectUserID string
	Payload       json.RawMessage
	Status        string
	Attempts      int
	LeaseUntil    *time.Time
	LeaseToken    string
	LeasedBy      string
	HeartbeatAt   *time.Time
	DueAt         time.Time
	LastError     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}
