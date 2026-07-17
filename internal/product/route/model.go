// Package route defines the deterministic Mission route-revision state
// machine. PostgreSQL adapters serialize commands around this model.
package route

import "errors"

type Status string

const (
	Generating Status = "generating"
	Proposed   Status = "proposed"
	Accepted   Status = "accepted"
	Stale      Status = "stale"
	Superseded Status = "superseded"
	Failed     Status = "failed"
)

var (
	ErrInvalid         = errors.New("route command is invalid")
	ErrConflict        = errors.New("route command conflicts with current state")
	ErrResultStale     = errors.New("route result is stale")
	ErrRevisionMissing = errors.New("route revision does not exist")
)

type Mission struct {
	ID                     string
	Version                uint64
	RouteVersion           uint64
	ClaimSetHash           string
	CurrentRouteRevisionID string
}

type Revision struct {
	ID, MissionID, ClaimSetHash string
	Version, RouteVersion       uint64
	BaseRouteVersion            uint64
	Status                      Status
	PayloadRef, PayloadHash     string
	StaleReason                 string
	FailureReason               string
}

type State struct {
	Mission   Mission
	Revisions map[string]Revision
}

type BeginCommand struct {
	RevisionID, ExpectedClaimSetHash string
	ExpectedRouteVersion             uint64
}

type CompleteCommand struct {
	RevisionID, PayloadRef, PayloadHash string
	ExpectedRevisionVersion             uint64
}

type FailCommand struct {
	RevisionID, Reason      string
	ExpectedRevisionVersion uint64
}

type AcceptCommand struct {
	RevisionID, ExpectedClaimSetHash string
	ExpectedRouteVersion             uint64
	ExpectedRevisionVersion          uint64
}

func (state State) Begin(command BeginCommand) (State, error) {
	if !state.valid() || command.RevisionID == "" || command.ExpectedClaimSetHash == "" {
		return State{}, ErrInvalid
	}
	if command.ExpectedRouteVersion != state.Mission.RouteVersion || command.ExpectedClaimSetHash != state.Mission.ClaimSetHash {
		return State{}, ErrConflict
	}
	if _, exists := state.Revisions[command.RevisionID]; exists {
		return State{}, ErrConflict
	}
	nextRouteVersion := state.Mission.RouteVersion + 1
	for _, revision := range state.Revisions {
		if revision.RouteVersion == nextRouteVersion && (revision.Status == Generating || revision.Status == Proposed) {
			return State{}, ErrConflict
		}
	}
	next := state.clone()
	next.Revisions[command.RevisionID] = Revision{ID: command.RevisionID, MissionID: state.Mission.ID, Version: 1, RouteVersion: nextRouteVersion, BaseRouteVersion: state.Mission.RouteVersion, ClaimSetHash: state.Mission.ClaimSetHash, Status: Generating}
	return next, nil
}

// Complete never promotes a result produced from stale inputs. It records the
// immutable payload and materializes stale instead of returning an ambiguous
// retry when Mission route/claim state moved while the planner was running.
func (state State) Complete(command CompleteCommand) (State, error) {
	if !state.valid() || command.RevisionID == "" || command.PayloadRef == "" || command.PayloadHash == "" || command.ExpectedRevisionVersion < 1 {
		return State{}, ErrInvalid
	}
	revision, exists := state.Revisions[command.RevisionID]
	if !exists {
		return State{}, ErrRevisionMissing
	}
	if revision.Status != Generating || revision.Version != command.ExpectedRevisionVersion || revision.PayloadRef != "" || revision.PayloadHash != "" {
		return State{}, ErrConflict
	}
	next := state.clone()
	revision.Version++
	revision.PayloadRef, revision.PayloadHash = command.PayloadRef, command.PayloadHash
	if revision.BaseRouteVersion != state.Mission.RouteVersion || revision.ClaimSetHash != state.Mission.ClaimSetHash {
		revision.Status = Stale
		revision.StaleReason = "mission_input_changed"
		next.Revisions[revision.ID] = revision
		return next, ErrResultStale
	}
	revision.Status = Proposed
	next.Revisions[revision.ID] = revision
	return next, nil
}

func (state State) Fail(command FailCommand) (State, error) {
	if !state.valid() || command.RevisionID == "" || command.Reason == "" || command.ExpectedRevisionVersion < 1 {
		return State{}, ErrInvalid
	}
	revision, exists := state.Revisions[command.RevisionID]
	if !exists {
		return State{}, ErrRevisionMissing
	}
	if revision.Status != Generating || revision.Version != command.ExpectedRevisionVersion || revision.PayloadRef != "" || revision.PayloadHash != "" || revision.FailureReason != "" {
		return State{}, ErrConflict
	}
	next := state.clone()
	revision.Version++
	revision.Status = Failed
	revision.FailureReason = command.Reason
	next.Revisions[revision.ID] = revision
	return next, nil
}

func (state State) Accept(command AcceptCommand) (State, error) {
	if !state.valid() || command.RevisionID == "" || command.ExpectedClaimSetHash == "" || command.ExpectedRevisionVersion < 1 {
		return State{}, ErrInvalid
	}
	revision, exists := state.Revisions[command.RevisionID]
	if !exists {
		return State{}, ErrRevisionMissing
	}
	if revision.Status != Proposed || revision.Version != command.ExpectedRevisionVersion || command.ExpectedRouteVersion != state.Mission.RouteVersion || command.ExpectedClaimSetHash != state.Mission.ClaimSetHash || revision.BaseRouteVersion != state.Mission.RouteVersion || revision.ClaimSetHash != state.Mission.ClaimSetHash || revision.RouteVersion != state.Mission.RouteVersion+1 {
		return State{}, ErrConflict
	}
	next := state.clone()
	if currentID := next.Mission.CurrentRouteRevisionID; currentID != "" {
		current, found := next.Revisions[currentID]
		if !found || current.Status != Accepted && current.Status != Stale {
			return State{}, ErrConflict
		}
		current.Status = Superseded
		current.Version++
		next.Revisions[currentID] = current
	}
	revision.Status = Accepted
	revision.Version++
	next.Revisions[revision.ID] = revision
	next.Mission.Version++
	next.Mission.RouteVersion = revision.RouteVersion
	next.Mission.CurrentRouteRevisionID = revision.ID
	return next, nil
}

func (state State) ChangeClaims(nextHash string) (State, error) {
	if !state.valid() || nextHash == "" || nextHash == state.Mission.ClaimSetHash {
		return State{}, ErrInvalid
	}
	next := state.clone()
	next.Mission.Version++
	next.Mission.RouteVersion++
	next.Mission.ClaimSetHash = nextHash
	if currentID := next.Mission.CurrentRouteRevisionID; currentID != "" {
		current, found := next.Revisions[currentID]
		if !found || current.Status != Accepted {
			return State{}, ErrConflict
		}
		current.Status = Stale
		current.Version++
		current.StaleReason = "claim_set_changed"
		next.Revisions[currentID] = current
	}
	for id, revision := range next.Revisions {
		if id == next.Mission.CurrentRouteRevisionID || revision.Status != Proposed {
			continue
		}
		revision.Status = Stale
		revision.Version++
		revision.StaleReason = "claim_set_changed"
		next.Revisions[id] = revision
	}
	return next, nil
}

func (state State) valid() bool {
	if state.Mission.ID == "" || state.Mission.Version < 1 || state.Mission.ClaimSetHash == "" || state.Revisions == nil {
		return false
	}
	if state.Mission.CurrentRouteRevisionID != "" {
		current, exists := state.Revisions[state.Mission.CurrentRouteRevisionID]
		if !exists || current.MissionID != state.Mission.ID || current.Status != Accepted && current.Status != Stale {
			return false
		}
	}
	accepted := 0
	for id, revision := range state.Revisions {
		if id == "" || revision.ID != id || revision.MissionID != state.Mission.ID || revision.Version < 1 || revision.ClaimSetHash == "" || revision.RouteVersion != revision.BaseRouteVersion+1 || !validStatus(revision.Status) || (revision.Status == Failed) != (revision.FailureReason != "") {
			return false
		}
		if revision.Status == Accepted {
			accepted++
			if id != state.Mission.CurrentRouteRevisionID {
				return false
			}
		}
	}
	return accepted <= 1
}

func (state State) clone() State {
	next := State{Mission: state.Mission, Revisions: make(map[string]Revision, len(state.Revisions))}
	for id, revision := range state.Revisions {
		next.Revisions[id] = revision
	}
	return next
}

func validStatus(status Status) bool {
	return status == Generating || status == Proposed || status == Accepted || status == Stale || status == Superseded || status == Failed
}
