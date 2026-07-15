package runtime

import (
	"errors"
	"testing"
)

func TestRuntimeSessionStateMachineAllowsOnlyExplicitLifecycle(t *testing.T) {
	states := []SessionState{SessionRequested, SessionProvisioning, SessionReady, SessionRunning, SessionIdle, SessionTerminationRequested, SessionTerminated, SessionFailed}
	allowed := map[[2]SessionState]bool{
		{SessionRequested, SessionProvisioning}: true, {SessionRequested, SessionTerminationRequested}: true,
		{SessionProvisioning, SessionReady}: true, {SessionProvisioning, SessionFailed}: true, {SessionProvisioning, SessionTerminationRequested}: true,
		{SessionReady, SessionRunning}: true, {SessionReady, SessionTerminationRequested}: true,
		{SessionRunning, SessionIdle}: true, {SessionRunning, SessionTerminationRequested}: true,
		{SessionIdle, SessionRunning}: true, {SessionIdle, SessionTerminationRequested}: true,
		{SessionTerminationRequested, SessionTerminated}: true, {SessionTerminationRequested, SessionFailed}: true,
	}
	for _, from := range states {
		for _, to := range states {
			err := ValidateSessionTransition(from, to)
			if allowed[[2]SessionState{from, to}] && err != nil {
				t.Fatalf("legal %s -> %s rejected: %v", from, to, err)
			}
			if !allowed[[2]SessionState{from, to}] && !errors.Is(err, ErrInvalidSessionTransition) {
				t.Fatalf("illegal %s -> %s accepted", from, to)
			}
		}
	}
	if !SessionTerminated.Terminal() || !SessionFailed.Terminal() || SessionIdle.Terminal() {
		t.Fatal("terminal state classification is incorrect")
	}
}

