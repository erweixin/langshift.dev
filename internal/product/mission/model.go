// Package mission defines the product-domain state machine for Missions and
// the single per-user Focus pointer. The model is storage-independent so the
// PostgreSQL implementation and model-based concurrency gate share one set of
// transition rules.
package mission

import "errors"

var (
	ErrInvalid         = errors.New("mission command is invalid")
	ErrNotFound        = errors.New("mission does not exist")
	ErrVersionConflict = errors.New("mission or focus version conflicts")
	ErrIllegalState    = errors.New("mission state transition is illegal")
	ErrReplacement     = errors.New("focused mission transition requires an explicit replacement")
	ErrInvariant       = errors.New("mission focus invariant is violated")
)

type Status string

const (
	Draft     Status = "draft"
	Active    Status = "active"
	Paused    Status = "paused"
	Completed Status = "completed"
	Archived  Status = "archived"
)

type Mission struct {
	ID      string
	Status  Status
	Version uint64
}

type Focus struct {
	MissionID string
	Version   uint64
}

type State struct {
	Missions map[string]Mission
	Focus    Focus
}

type StatusCommand struct {
	MissionID              string
	Next                   Status
	ExpectedMissionVersion uint64
	ExpectedFocusVersion   uint64
	ReplacementMissionID   string
}

type FocusCommand struct {
	MissionID            string
	ExpectedFocusVersion uint64
}

func New() State {
	return State{Missions: map[string]Mission{}, Focus: Focus{Version: 0}}
}

func (state State) Create(id string) (State, error) {
	if id == "" || state.Missions == nil {
		return State{}, ErrInvalid
	}
	if _, exists := state.Missions[id]; exists {
		return State{}, ErrVersionConflict
	}
	next := state.clone()
	next.Missions[id] = Mission{ID: id, Status: Draft, Version: 1}
	if err := next.Validate(); err != nil {
		return State{}, err
	}
	return next, nil
}

func (state State) SetFocus(command FocusCommand) (State, error) {
	if command.MissionID == "" || command.ExpectedFocusVersion != state.Focus.Version {
		if command.MissionID == "" {
			return State{}, ErrInvalid
		}
		return State{}, ErrVersionConflict
	}
	target, exists := state.Missions[command.MissionID]
	if !exists {
		return State{}, ErrNotFound
	}
	if target.Status != Active {
		return State{}, ErrIllegalState
	}
	if state.Focus.MissionID == command.MissionID {
		return state.clone(), nil
	}
	next := state.clone()
	next.Focus.MissionID = command.MissionID
	next.Focus.Version++
	if err := next.Validate(); err != nil {
		return State{}, err
	}
	return next, nil
}

func (state State) ChangeStatus(command StatusCommand) (State, error) {
	if command.MissionID == "" || !validStatus(command.Next) {
		return State{}, ErrInvalid
	}
	current, exists := state.Missions[command.MissionID]
	if !exists {
		return State{}, ErrNotFound
	}
	if command.ExpectedMissionVersion != current.Version || command.ExpectedFocusVersion != state.Focus.Version {
		return State{}, ErrVersionConflict
	}
	if current.Status == command.Next {
		if command.ReplacementMissionID != "" {
			return State{}, ErrReplacement
		}
		return state.clone(), nil
	}
	if !legal(current.Status, command.Next) {
		return State{}, ErrIllegalState
	}

	next := state.clone()
	current.Status = command.Next
	current.Version++
	next.Missions[current.ID] = current

	if state.Focus.MissionID == current.ID && command.Next != Active {
		replacements := activeMissionIDs(next.Missions, current.ID)
		switch {
		case len(replacements) == 0 && command.ReplacementMissionID != "":
			return State{}, ErrReplacement
		case len(replacements) == 0:
			next.Focus.MissionID = ""
			next.Focus.Version++
		case command.ReplacementMissionID == "":
			return State{}, ErrReplacement
		case !contains(replacements, command.ReplacementMissionID):
			return State{}, ErrReplacement
		default:
			next.Focus.MissionID = command.ReplacementMissionID
			next.Focus.Version++
		}
	} else if command.ReplacementMissionID != "" {
		return State{}, ErrReplacement
	}

	if command.Next == Active && next.Focus.MissionID == "" {
		next.Focus.MissionID = current.ID
		next.Focus.Version++
	}
	if err := next.Validate(); err != nil {
		return State{}, err
	}
	return next, nil
}

func (state State) Validate() error {
	if state.Missions == nil {
		return ErrInvariant
	}
	active := 0
	for id, item := range state.Missions {
		if id == "" || item.ID != id || item.Version < 1 || !validStatus(item.Status) {
			return ErrInvariant
		}
		if item.Status == Active {
			active++
		}
	}
	if active == 0 {
		if state.Focus.MissionID != "" {
			return ErrInvariant
		}
		return nil
	}
	focused, exists := state.Missions[state.Focus.MissionID]
	if !exists || focused.Status != Active {
		return ErrInvariant
	}
	return nil
}

func (state State) clone() State {
	next := State{Missions: make(map[string]Mission, len(state.Missions)), Focus: state.Focus}
	for id, item := range state.Missions {
		next.Missions[id] = item
	}
	return next
}

func validStatus(status Status) bool {
	return status == Draft || status == Active || status == Paused || status == Completed || status == Archived
}

func legal(current, next Status) bool {
	switch current {
	case Draft:
		return next == Active || next == Archived
	case Active:
		return next == Paused || next == Completed || next == Archived
	case Paused:
		return next == Active || next == Archived
	case Completed:
		return next == Archived
	case Archived:
		return false
	default:
		return false
	}
}

func activeMissionIDs(missions map[string]Mission, except string) []string {
	ids := make([]string, 0, len(missions))
	for id, item := range missions {
		if id != except && item.Status == Active {
			ids = append(ids, id)
		}
	}
	return ids
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
