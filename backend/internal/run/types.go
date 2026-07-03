package run

import "errors"

const (
	StatusAccepted        = "accepted"
	StatusQueued          = "queued"
	StatusExecuting       = "executing"
	StatusWaitingTool     = "waiting_tool"
	StatusWaitingApproval = "waiting_approval"
	StatusSucceeded       = "succeeded"
	StatusFailed          = "failed"
	StatusExpired         = "expired"
	StatusCancelled       = "cancelled"
)

const (
	EventRunAccepted  = "RunAccepted"
	EventRunQueued    = "RunQueued"
	EventRunStarted   = "RunStarted"
	EventRunSucceeded = "RunSucceeded"
	EventRunFailed    = "RunFailed"
	EventRunExpired   = "RunExpired"
)

var (
	ErrInvalidTransition = errors.New("invalid run transition")
	ErrInvalidPayload    = errors.New("invalid run event payload")
	ErrMissingRunCAS     = errors.New("run transition requires run_version CAS")
	errMissingPool       = errors.New("missing database pool")
	errMissingEvents     = errors.New("missing event service")
	errMissingIDs        = errors.New("missing id generator")
)
