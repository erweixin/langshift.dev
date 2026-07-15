package runtime

import "errors"

var ErrInvalidSessionTransition = errors.New("invalid runtime session transition")

type SessionState string

const (
	SessionRequested            SessionState = "requested"
	SessionProvisioning         SessionState = "provisioning"
	SessionReady                SessionState = "ready"
	SessionRunning              SessionState = "running"
	SessionIdle                 SessionState = "idle"
	SessionTerminationRequested SessionState = "termination_requested"
	SessionTerminated           SessionState = "terminated"
	SessionFailed               SessionState = "failed"
)

func ValidateSessionTransition(from, to SessionState) error {
	allowed := false
	switch from {
	case SessionRequested:
		allowed = to == SessionProvisioning || to == SessionTerminationRequested
	case SessionProvisioning:
		allowed = to == SessionReady || to == SessionFailed || to == SessionTerminationRequested
	case SessionReady:
		allowed = to == SessionRunning || to == SessionTerminationRequested
	case SessionRunning:
		allowed = to == SessionIdle || to == SessionTerminationRequested
	case SessionIdle:
		allowed = to == SessionRunning || to == SessionTerminationRequested
	case SessionTerminationRequested:
		allowed = to == SessionTerminated || to == SessionFailed
	case SessionTerminated, SessionFailed:
		allowed = false
	default:
		return ErrInvalidSessionTransition
	}
	if !allowed {
		return ErrInvalidSessionTransition
	}
	return nil
}

func (state SessionState) Terminal() bool { return state == SessionTerminated || state == SessionFailed }

