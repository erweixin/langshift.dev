package mission

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

func TestFocusedMissionRequiresExplicitReplacement(t *testing.T) {
	state := New()
	state = mustCreate(t, state, "a")
	state = mustCreate(t, state, "b")
	state = mustStatus(t, state, StatusCommand{MissionID: "a", Next: Active, ExpectedMissionVersion: 1, ExpectedFocusVersion: 0})
	state = mustStatus(t, state, StatusCommand{MissionID: "b", Next: Active, ExpectedMissionVersion: 1, ExpectedFocusVersion: 1})

	before := state.clone()
	_, err := state.ChangeStatus(StatusCommand{MissionID: "a", Next: Paused, ExpectedMissionVersion: 2, ExpectedFocusVersion: 1})
	if !errors.Is(err, ErrReplacement) || !reflect.DeepEqual(state, before) {
		t.Fatalf("ChangeStatus() error = %v, state mutated=%v", err, !reflect.DeepEqual(state, before))
	}
	next := mustStatus(t, state, StatusCommand{MissionID: "a", Next: Paused, ExpectedMissionVersion: 2, ExpectedFocusVersion: 1, ReplacementMissionID: "b"})
	if next.Focus.MissionID != "b" || next.Focus.Version != 2 {
		t.Fatalf("focus = %#v", next.Focus)
	}
}

func TestOnlyActiveMissionCanBeFocused(t *testing.T) {
	state := mustCreate(t, New(), "draft")
	if _, err := state.SetFocus(FocusCommand{MissionID: "draft", ExpectedFocusVersion: 0}); !errors.Is(err, ErrIllegalState) {
		t.Fatalf("SetFocus() error = %v", err)
	}
}

func TestStaleCASDoesNotMutate(t *testing.T) {
	state := mustCreate(t, New(), "a")
	state = mustStatus(t, state, StatusCommand{MissionID: "a", Next: Active, ExpectedMissionVersion: 1, ExpectedFocusVersion: 0})
	before := state.clone()
	if _, err := state.ChangeStatus(StatusCommand{MissionID: "a", Next: Paused, ExpectedMissionVersion: 1, ExpectedFocusVersion: 1}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("ChangeStatus() error = %v", err)
	}
	if !reflect.DeepEqual(state, before) {
		t.Fatal("stale command mutated state")
	}
}

func TestRandomizedMissionFocusModel(t *testing.T) {
	random := rand.New(rand.NewSource(20260716))
	state := New()
	for index := 0; index < 12; index++ {
		state = mustCreate(t, state, fmt.Sprintf("mission-%02d", index))
	}
	for operation := 0; operation < 10_000; operation++ {
		before := state.clone()
		missionID := fmt.Sprintf("mission-%02d", random.Intn(12))
		current := state.Missions[missionID]
		var next State
		var err error
		if random.Intn(4) == 0 && activeCount(state) > 0 {
			target := randomActive(state, random)
			expected := state.Focus.Version
			if random.Intn(5) == 0 && expected > 0 {
				expected--
			}
			next, err = state.SetFocus(FocusCommand{MissionID: target, ExpectedFocusVersion: expected})
		} else {
			targets := legalTargets(current.Status)
			if len(targets) == 0 {
				continue
			}
			command := StatusCommand{MissionID: missionID, Next: targets[random.Intn(len(targets))], ExpectedMissionVersion: current.Version, ExpectedFocusVersion: state.Focus.Version}
			if state.Focus.MissionID == missionID && command.Next != Active {
				if replacement := anotherActive(state, missionID); replacement != "" && random.Intn(4) != 0 {
					command.ReplacementMissionID = replacement
				}
			}
			if random.Intn(6) == 0 && command.ExpectedMissionVersion > 0 {
				command.ExpectedMissionVersion--
			}
			next, err = state.ChangeStatus(command)
		}
		if err != nil {
			if !reflect.DeepEqual(state, before) {
				t.Fatalf("operation %d rejected but mutated state: %v", operation, err)
			}
			continue
		}
		state = next
		if err = state.Validate(); err != nil {
			t.Fatalf("operation %d violated invariant: %v; state=%#v", operation, err, state)
		}
	}
}

func mustCreate(t *testing.T, state State, id string) State {
	t.Helper()
	next, err := state.Create(id)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func mustStatus(t *testing.T, state State, command StatusCommand) State {
	t.Helper()
	next, err := state.ChangeStatus(command)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func legalTargets(status Status) []Status {
	switch status {
	case Draft:
		return []Status{Active, Archived}
	case Active:
		return []Status{Paused, Completed, Archived}
	case Paused:
		return []Status{Active, Archived}
	case Completed:
		return []Status{Archived}
	default:
		return nil
	}
}

func activeCount(state State) int {
	count := 0
	for _, item := range state.Missions {
		if item.Status == Active {
			count++
		}
	}
	return count
}

func randomActive(state State, random *rand.Rand) string {
	ids := make([]string, 0, activeCount(state))
	for id, item := range state.Missions {
		if item.Status == Active {
			ids = append(ids, id)
		}
	}
	return ids[random.Intn(len(ids))]
}

func anotherActive(state State, except string) string {
	for id, item := range state.Missions {
		if id != except && item.Status == Active {
			return id
		}
	}
	return ""
}
