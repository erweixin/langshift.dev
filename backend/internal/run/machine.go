package run

func IsTerminal(status string) bool {
	switch status {
	case StatusSucceeded, StatusFailed, StatusExpired, StatusCancelled:
		return true
	default:
		return false
	}
}

func CanTransition(from string, to string) bool {
	if from == to {
		return true
	}
	if IsTerminal(from) {
		return false
	}

	switch to {
	case StatusQueued:
		return from == StatusAccepted
	case StatusExecuting:
		return from == StatusQueued || from == StatusWaitingTool || from == StatusWaitingApproval
	case StatusSucceeded:
		return from == StatusExecuting
	case StatusFailed, StatusExpired, StatusCancelled:
		return !IsTerminal(from)
	default:
		return false
	}
}

func TargetStatusForEvent(eventType string) (string, bool) {
	switch eventType {
	case EventRunAccepted:
		return StatusAccepted, true
	case EventRunQueued:
		return StatusQueued, true
	case EventRunStarted:
		return StatusExecuting, true
	case EventRunSucceeded:
		return StatusSucceeded, true
	case EventRunFailed:
		return StatusFailed, true
	case EventRunExpired:
		return StatusExpired, true
	default:
		return "", false
	}
}
