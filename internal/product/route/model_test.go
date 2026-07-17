package route

import (
	"errors"
	"fmt"
	"testing"
)

func TestPlannerCompletionMaterializesStaleWhenClaimsMove(t *testing.T) {
	state := newState()
	state, err := state.Begin(BeginCommand{RevisionID: "route-1", ExpectedRouteVersion: 0, ExpectedClaimSetHash: "claims-1"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = state.ChangeClaims("claims-2")
	if err != nil {
		t.Fatal(err)
	}
	state, err = state.Complete(CompleteCommand{RevisionID: "route-1", ExpectedRevisionVersion: 1, PayloadRef: "payload-1", PayloadHash: "hash-1"})
	if !errors.Is(err, ErrResultStale) || state.Revisions["route-1"].Status != Stale || state.Mission.CurrentRouteRevisionID != "" {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	if _, err = state.Accept(AcceptCommand{RevisionID: "route-1", ExpectedRevisionVersion: 2, ExpectedRouteVersion: 1, ExpectedClaimSetHash: "claims-2"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale route accepted: %v", err)
	}
}

func TestAcceptSupersedesPreviousRevisionAtomically(t *testing.T) {
	state := proposedState(t, newState(), "route-1")
	state, err := state.Accept(AcceptCommand{RevisionID: "route-1", ExpectedRevisionVersion: 2, ExpectedRouteVersion: 0, ExpectedClaimSetHash: "claims-1"})
	if err != nil {
		t.Fatal(err)
	}
	state = proposedState(t, state, "route-2")
	state, err = state.Accept(AcceptCommand{RevisionID: "route-2", ExpectedRevisionVersion: 2, ExpectedRouteVersion: 1, ExpectedClaimSetHash: "claims-1"})
	if err != nil || state.Mission.CurrentRouteRevisionID != "route-2" || state.Mission.RouteVersion != 2 || state.Revisions["route-1"].Status != Superseded || state.Revisions["route-2"].Status != Accepted {
		t.Fatalf("state=%#v err=%v", state, err)
	}
}

func TestRouteClaimInterleavingsNeverPromoteStaleResult(t *testing.T) {
	for index := 0; index < 1000; index++ {
		state := newState()
		revisionID := fmt.Sprintf("route-%d", index)
		var err error
		state, err = state.Begin(BeginCommand{RevisionID: revisionID, ExpectedRouteVersion: 0, ExpectedClaimSetHash: "claims-1"})
		if err != nil {
			t.Fatal(err)
		}
		if index%2 == 0 {
			state, err = state.ChangeClaims("claims-2")
			if err != nil {
				t.Fatal(err)
			}
			state, err = state.Complete(CompleteCommand{RevisionID: revisionID, ExpectedRevisionVersion: 1, PayloadRef: "payload", PayloadHash: "hash"})
			if !errors.Is(err, ErrResultStale) {
				t.Fatalf("iteration=%d err=%v", index, err)
			}
		} else {
			state, err = state.Complete(CompleteCommand{RevisionID: revisionID, ExpectedRevisionVersion: 1, PayloadRef: "payload", PayloadHash: "hash"})
			if err != nil {
				t.Fatal(err)
			}
			state, err = state.ChangeClaims("claims-2")
			if err != nil {
				t.Fatal(err)
			}
		}
		if state.Revisions[revisionID].Status != Stale {
			t.Fatalf("iteration=%d status=%s", index, state.Revisions[revisionID].Status)
		}
		if _, acceptErr := state.Accept(AcceptCommand{RevisionID: revisionID, ExpectedRevisionVersion: state.Revisions[revisionID].Version, ExpectedRouteVersion: state.Mission.RouteVersion, ExpectedClaimSetHash: state.Mission.ClaimSetHash}); !errors.Is(acceptErr, ErrConflict) {
			t.Fatalf("iteration=%d stale input accepted: %v", index, acceptErr)
		}
	}
}

func TestRoutePlannerFailureTerminatesGeneratingRevision(t *testing.T) {
	state := State{Mission: Mission{ID: "mission-1", Version: 1, ClaimSetHash: "claims-1"}, Revisions: map[string]Revision{}}
	state, err := state.Begin(BeginCommand{RevisionID: "route-1", ExpectedClaimSetHash: "claims-1"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = state.Fail(FailCommand{RevisionID: "route-1", ExpectedRevisionVersion: 1, Reason: "planner_failed"})
	if err != nil || state.Revisions["route-1"].Status != Failed || state.Revisions["route-1"].Version != 2 || state.Revisions["route-1"].FailureReason != "planner_failed" {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	if _, err = state.Fail(FailCommand{RevisionID: "route-1", ExpectedRevisionVersion: 2, Reason: "again"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal failure replay err=%v", err)
	}
}

func proposedState(t *testing.T, state State, id string) State {
	t.Helper()
	next, err := state.Begin(BeginCommand{RevisionID: id, ExpectedRouteVersion: state.Mission.RouteVersion, ExpectedClaimSetHash: state.Mission.ClaimSetHash})
	if err != nil {
		t.Fatal(err)
	}
	next, err = next.Complete(CompleteCommand{RevisionID: id, ExpectedRevisionVersion: 1, PayloadRef: "payload-" + id, PayloadHash: "hash-" + id})
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func newState() State {
	return State{Mission: Mission{ID: "mission-1", Version: 1, ClaimSetHash: "claims-1"}, Revisions: map[string]Revision{}}
}
