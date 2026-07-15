package statemachine

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestAllLegalAndIllegalTransitionsAreExhaustivelyClassified(t *testing.T) {
	metrics := struct {
		Scenario                   string `json:"scenario"`
		Machines                   int    `json:"machines"`
		States                     int    `json:"states"`
		LegalTransitions           int    `json:"legal_transitions"`
		LegalTransitionsAccepted   int    `json:"legal_transitions_accepted"`
		IllegalTransitions         int    `json:"illegal_transitions"`
		IllegalTransitionsRejected int    `json:"illegal_transitions_rejected"`
		ReachableStates            int    `json:"reachable_states"`
	}{Scenario: "execution_state_machine_model"}
	for name, snapshot := range AllSnapshots() {
		name, snapshot := name, snapshot
		metrics.Machines++
		metrics.States += len(snapshot.States)
		t.Run(name, func(t *testing.T) {
			allowed := map[string]map[string]bool{}
			for from, destinations := range snapshot.Transitions {
				allowed[from] = map[string]bool{}
				for _, to := range destinations {
					allowed[from][to] = true
					metrics.LegalTransitions++
				}
			}
			for _, from := range snapshot.States {
				for _, to := range snapshot.States {
					err := validateSnapshotTransition(name, from, to)
					if allowed[from][to] && err != nil {
						t.Fatalf("legal transition %s -> %s rejected: %v", from, to, err)
					}
					if allowed[from][to] {
						metrics.LegalTransitionsAccepted++
					}
					if !allowed[from][to] && !errors.Is(err, ErrIllegalTransition) {
						t.Fatalf("illegal transition %s -> %s was not rejected: %v", from, to, err)
					}
					if !allowed[from][to] {
						metrics.IllegalTransitions++
						metrics.IllegalTransitionsRejected++
					}
				}
			}
			reachable := map[string]bool{}
			queue := append([]string(nil), snapshot.Initial...)
			for len(queue) > 0 {
				state := queue[0]
				queue = queue[1:]
				if reachable[state] {
					continue
				}
				reachable[state] = true
				queue = append(queue, snapshot.Transitions[state]...)
			}
			if len(reachable) != len(snapshot.States) {
				t.Fatalf("not every state is reachable from an initial state: reachable=%v states=%v", reachable, snapshot.States)
			}
			metrics.ReachableStates += len(reachable)
		})
	}
	encoded, err := json.Marshal(metrics)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("state_machine_model=%s", encoded)
}

func TestUnknownStatesAndInvalidInitialStatesAreRejected(t *testing.T) {
	if err := Runs.ValidateTransition(RunState("forged"), RunQueued); !errors.Is(err, ErrUnknownState) {
		t.Fatalf("unknown from state: %v", err)
	}
	if err := Runs.ValidateTransition(RunAccepted, RunState("forged")); !errors.Is(err, ErrUnknownState) {
		t.Fatalf("unknown to state: %v", err)
	}
	if err := Runs.ValidateInitial(RunExecuting); !errors.Is(err, ErrInvalidInitial) {
		t.Fatalf("non-initial run state: %v", err)
	}
	if err := ToolCalls.ValidateInitial(ToolCallSucceeded); err != nil {
		t.Fatalf("inline platform success is a valid initial ToolCall state: %v", err)
	}
}

func TestInvalidDefinitionsFailClosed(t *testing.T) {
	tests := []struct {
		name        string
		states      []RunState
		initial     []RunState
		terminal    []RunState
		transitions map[RunState][]RunState
	}{
		{name: "unknown initial", states: []RunState{RunAccepted}, initial: []RunState{RunQueued}, transitions: map[RunState][]RunState{RunAccepted: {}}},
		{name: "terminal has outgoing", states: []RunState{RunAccepted, RunSucceeded}, initial: []RunState{RunAccepted}, terminal: []RunState{RunSucceeded}, transitions: map[RunState][]RunState{RunAccepted: {RunSucceeded}, RunSucceeded: {RunAccepted}}},
		{name: "self transition", states: []RunState{RunAccepted}, initial: []RunState{RunAccepted}, transitions: map[RunState][]RunState{RunAccepted: {RunAccepted}}},
		{name: "missing transition entry", states: []RunState{RunAccepted, RunQueued}, initial: []RunState{RunAccepted}, transitions: map[RunState][]RunState{RunAccepted: {RunQueued}}},
	}
	for _, test := range tests {
		if _, err := New(test.name, test.states, test.initial, test.terminal, test.transitions); !errors.Is(err, ErrInvalidDefinition) {
			t.Errorf("%s: %v", test.name, err)
		}
	}
}

func validateSnapshotTransition(name, from, to string) error {
	switch name {
	case Runs.Name():
		return Runs.ValidateTransition(RunState(from), RunState(to))
	case ToolCalls.Name():
		return ToolCalls.ValidateTransition(ToolCallState(from), ToolCallState(to))
	case Commands.Name():
		return Commands.ValidateTransition(CommandState(from), CommandState(to))
	case Attempts.Name():
		return Attempts.ValidateTransition(AttemptState(from), AttemptState(to))
	case Approvals.Name():
		return Approvals.ValidateTransition(ApprovalState(from), ApprovalState(to))
	case WorkspaceRevisions.Name():
		return WorkspaceRevisions.ValidateTransition(WorkspaceRevisionState(from), WorkspaceRevisionState(to))
	default:
		panic(name)
	}
}
