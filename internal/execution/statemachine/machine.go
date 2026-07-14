// Package statemachine contains the immutable lifecycle contracts used by the
// execution kernel. Persistence code must validate a transition here before it
// attempts the corresponding compare-and-swap update.
package statemachine

import (
	"errors"
	"fmt"
	"sort"
)

var (
	ErrUnknownState      = errors.New("state is not part of the machine")
	ErrIllegalTransition = errors.New("state transition is illegal")
	ErrInvalidInitial    = errors.New("state is not a valid initial state")
	ErrInvalidDefinition = errors.New("state machine definition is invalid")
)

type stringState interface{ ~string }

// Machine is immutable after construction and safe for concurrent use.
type Machine[S stringState] struct {
	name        string
	states      map[S]struct{}
	initial     map[S]struct{}
	terminal    map[S]struct{}
	transitions map[S]map[S]struct{}
}

// Snapshot is the stable, serialization-friendly representation used by the
// contract drift and exhaustive model tests.
type Snapshot struct {
	Name        string
	States      []string
	Initial     []string
	Terminal    []string
	Transitions map[string][]string
}

func MustNew[S stringState](name string, states, initial, terminal []S, transitions map[S][]S) Machine[S] {
	machine, err := New(name, states, initial, terminal, transitions)
	if err != nil {
		panic(err)
	}
	return machine
}

func New[S stringState](name string, states, initial, terminal []S, transitions map[S][]S) (Machine[S], error) {
	machine := Machine[S]{
		name:        name,
		states:      make(map[S]struct{}, len(states)),
		initial:     make(map[S]struct{}, len(initial)),
		terminal:    make(map[S]struct{}, len(terminal)),
		transitions: make(map[S]map[S]struct{}, len(states)),
	}
	if name == "" || len(states) == 0 || len(initial) == 0 {
		return Machine[S]{}, ErrInvalidDefinition
	}
	for _, state := range states {
		if state == "" {
			return Machine[S]{}, ErrInvalidDefinition
		}
		if _, duplicate := machine.states[state]; duplicate {
			return Machine[S]{}, ErrInvalidDefinition
		}
		machine.states[state] = struct{}{}
		machine.transitions[state] = map[S]struct{}{}
	}
	for _, state := range initial {
		if _, known := machine.states[state]; !known {
			return Machine[S]{}, ErrInvalidDefinition
		}
		machine.initial[state] = struct{}{}
	}
	for _, state := range terminal {
		if _, known := machine.states[state]; !known {
			return Machine[S]{}, ErrInvalidDefinition
		}
		machine.terminal[state] = struct{}{}
	}
	if len(transitions) != len(states) {
		return Machine[S]{}, ErrInvalidDefinition
	}
	for from, destinations := range transitions {
		if _, known := machine.states[from]; !known {
			return Machine[S]{}, ErrInvalidDefinition
		}
		if _, terminalState := machine.terminal[from]; terminalState && len(destinations) != 0 {
			return Machine[S]{}, ErrInvalidDefinition
		}
		for _, to := range destinations {
			if _, known := machine.states[to]; !known || from == to {
				return Machine[S]{}, ErrInvalidDefinition
			}
			if _, duplicate := machine.transitions[from][to]; duplicate {
				return Machine[S]{}, ErrInvalidDefinition
			}
			machine.transitions[from][to] = struct{}{}
		}
	}
	return machine, nil
}

func (machine Machine[S]) Name() string { return machine.name }

func (machine Machine[S]) ValidateInitial(state S) error {
	if _, known := machine.states[state]; !known {
		return fmt.Errorf("%s initial %q: %w", machine.name, state, ErrUnknownState)
	}
	if _, allowed := machine.initial[state]; !allowed {
		return fmt.Errorf("%s initial %q: %w", machine.name, state, ErrInvalidInitial)
	}
	return nil
}

func (machine Machine[S]) ValidateTransition(from, to S) error {
	if _, known := machine.states[from]; !known {
		return fmt.Errorf("%s from %q: %w", machine.name, from, ErrUnknownState)
	}
	if _, known := machine.states[to]; !known {
		return fmt.Errorf("%s to %q: %w", machine.name, to, ErrUnknownState)
	}
	if _, allowed := machine.transitions[from][to]; !allowed {
		return fmt.Errorf("%s %q -> %q: %w", machine.name, from, to, ErrIllegalTransition)
	}
	return nil
}

func (machine Machine[S]) IsTerminal(state S) bool {
	_, terminal := machine.terminal[state]
	return terminal
}

func (machine Machine[S]) Snapshot() Snapshot {
	snapshot := Snapshot{
		Name:        machine.name,
		States:      make([]string, 0, len(machine.states)),
		Initial:     make([]string, 0, len(machine.initial)),
		Terminal:    make([]string, 0, len(machine.terminal)),
		Transitions: make(map[string][]string, len(machine.transitions)),
	}
	for state := range machine.states {
		snapshot.States = append(snapshot.States, string(state))
	}
	for state := range machine.initial {
		snapshot.Initial = append(snapshot.Initial, string(state))
	}
	for state := range machine.terminal {
		snapshot.Terminal = append(snapshot.Terminal, string(state))
	}
	for from, destinations := range machine.transitions {
		values := make([]string, 0, len(destinations))
		for to := range destinations {
			values = append(values, string(to))
		}
		sort.Strings(values)
		snapshot.Transitions[string(from)] = values
	}
	sort.Strings(snapshot.States)
	sort.Strings(snapshot.Initial)
	sort.Strings(snapshot.Terminal)
	return snapshot
}

type RunState string

const (
	RunAccepted        RunState = "accepted"
	RunQueued          RunState = "queued"
	RunExecuting       RunState = "executing"
	RunWaitingTool     RunState = "waiting_tool"
	RunWaitingChild    RunState = "waiting_child"
	RunWaitingApproval RunState = "waiting_approval"
	RunSucceeded       RunState = "succeeded"
	RunFailed          RunState = "failed"
	RunCancelled       RunState = "cancelled"
	RunExpired         RunState = "expired"
)

var Runs = MustNew("run",
	[]RunState{RunAccepted, RunQueued, RunExecuting, RunWaitingTool, RunWaitingChild, RunWaitingApproval, RunSucceeded, RunFailed, RunCancelled, RunExpired},
	[]RunState{RunAccepted},
	[]RunState{RunSucceeded, RunFailed, RunCancelled, RunExpired},
	map[RunState][]RunState{
		RunAccepted:        {RunQueued, RunCancelled, RunExpired},
		RunQueued:          {RunExecuting, RunCancelled, RunExpired},
		RunExecuting:       {RunWaitingTool, RunWaitingChild, RunWaitingApproval, RunSucceeded, RunFailed, RunCancelled, RunExpired},
		RunWaitingTool:     {RunQueued, RunWaitingApproval, RunFailed, RunCancelled, RunExpired},
		RunWaitingChild:    {RunQueued, RunFailed, RunCancelled, RunExpired},
		RunWaitingApproval: {RunWaitingTool, RunQueued, RunCancelled, RunExpired},
		RunSucceeded:       {}, RunFailed: {}, RunCancelled: {}, RunExpired: {},
	})

type ToolCallState string

const (
	ToolCallRequested         ToolCallState = "requested"
	ToolCallPreviewRequested  ToolCallState = "preview_requested"
	ToolCallPreparingApproval ToolCallState = "preparing_approval"
	ToolCallAwaitingApproval  ToolCallState = "awaiting_approval"
	ToolCallExecuting         ToolCallState = "executing"
	ToolCallCommitRequested   ToolCallState = "commit_requested"
	ToolCallCommitting        ToolCallState = "committing"
	ToolCallOutcomeUnknown    ToolCallState = "outcome_unknown"
	ToolCallSucceeded         ToolCallState = "succeeded"
	ToolCallFailed            ToolCallState = "failed"
	ToolCallCancelled         ToolCallState = "cancelled"
	ToolCallResolvedUnknown   ToolCallState = "resolved_unknown"
)

var ToolCalls = MustNew("tool_call",
	[]ToolCallState{ToolCallRequested, ToolCallPreviewRequested, ToolCallPreparingApproval, ToolCallAwaitingApproval, ToolCallExecuting, ToolCallCommitRequested, ToolCallCommitting, ToolCallOutcomeUnknown, ToolCallSucceeded, ToolCallFailed, ToolCallCancelled, ToolCallResolvedUnknown},
	[]ToolCallState{ToolCallRequested, ToolCallPreviewRequested, ToolCallAwaitingApproval, ToolCallSucceeded},
	[]ToolCallState{ToolCallSucceeded, ToolCallFailed, ToolCallCancelled, ToolCallResolvedUnknown},
	map[ToolCallState][]ToolCallState{
		ToolCallRequested:         {ToolCallExecuting, ToolCallCancelled},
		ToolCallPreviewRequested:  {ToolCallPreparingApproval, ToolCallCancelled},
		ToolCallPreparingApproval: {ToolCallAwaitingApproval, ToolCallFailed, ToolCallCancelled},
		ToolCallAwaitingApproval:  {ToolCallRequested, ToolCallSucceeded, ToolCallCancelled},
		ToolCallExecuting:         {ToolCallCommitRequested, ToolCallSucceeded, ToolCallFailed, ToolCallOutcomeUnknown, ToolCallCancelled},
		ToolCallCommitRequested:   {ToolCallCommitting, ToolCallCancelled},
		ToolCallCommitting:        {ToolCallSucceeded, ToolCallFailed, ToolCallOutcomeUnknown},
		ToolCallOutcomeUnknown:    {ToolCallSucceeded, ToolCallFailed, ToolCallCommitRequested, ToolCallResolvedUnknown},
		ToolCallSucceeded:         {}, ToolCallFailed: {}, ToolCallCancelled: {}, ToolCallResolvedUnknown: {},
	})

type CommandState string

const (
	CommandPending    CommandState = "pending"
	CommandPublishing CommandState = "publishing"
	CommandPublished  CommandState = "published"
)

var Commands = MustNew("command",
	[]CommandState{CommandPending, CommandPublishing, CommandPublished},
	[]CommandState{CommandPending}, nil,
	map[CommandState][]CommandState{
		CommandPending: {CommandPublishing}, CommandPublishing: {CommandPublished, CommandPending}, CommandPublished: {CommandPending},
	})

type AttemptState string

const (
	AttemptRunning   AttemptState = "running"
	AttemptSucceeded AttemptState = "succeeded"
	AttemptFailed    AttemptState = "failed"
	AttemptAbandoned AttemptState = "abandoned"
	AttemptExpired   AttemptState = "expired"
)

var Attempts = MustNew("attempt",
	[]AttemptState{AttemptRunning, AttemptSucceeded, AttemptFailed, AttemptAbandoned, AttemptExpired},
	[]AttemptState{AttemptRunning},
	[]AttemptState{AttemptSucceeded, AttemptFailed, AttemptAbandoned, AttemptExpired},
	map[AttemptState][]AttemptState{
		AttemptRunning: {AttemptSucceeded, AttemptFailed, AttemptAbandoned, AttemptExpired}, AttemptSucceeded: {}, AttemptFailed: {}, AttemptAbandoned: {}, AttemptExpired: {},
	})

type ApprovalState string

const (
	ApprovalPending     ApprovalState = "pending"
	ApprovalGranted     ApprovalState = "granted"
	ApprovalRejected    ApprovalState = "rejected"
	ApprovalExpired     ApprovalState = "expired"
	ApprovalInvalidated ApprovalState = "invalidated"
)

var Approvals = MustNew("approval",
	[]ApprovalState{ApprovalPending, ApprovalGranted, ApprovalRejected, ApprovalExpired, ApprovalInvalidated},
	[]ApprovalState{ApprovalPending},
	[]ApprovalState{ApprovalRejected, ApprovalExpired, ApprovalInvalidated},
	map[ApprovalState][]ApprovalState{
		ApprovalPending: {ApprovalGranted, ApprovalRejected, ApprovalExpired, ApprovalInvalidated}, ApprovalGranted: {ApprovalInvalidated}, ApprovalRejected: {}, ApprovalExpired: {}, ApprovalInvalidated: {},
	})

type WorkspaceRevisionState string

const (
	WorkspaceRevisionPrepared       WorkspaceRevisionState = "prepared"
	WorkspaceRevisionAuthorized     WorkspaceRevisionState = "authorized"
	WorkspaceRevisionPublishing     WorkspaceRevisionState = "publishing"
	WorkspaceRevisionConfirmed      WorkspaceRevisionState = "confirmed"
	WorkspaceRevisionOutcomeUnknown WorkspaceRevisionState = "outcome_unknown"
	WorkspaceRevisionFailed         WorkspaceRevisionState = "failed"
	WorkspaceRevisionAbandoned      WorkspaceRevisionState = "abandoned"
)

var WorkspaceRevisions = MustNew("workspace_revision",
	[]WorkspaceRevisionState{WorkspaceRevisionPrepared, WorkspaceRevisionAuthorized, WorkspaceRevisionPublishing, WorkspaceRevisionConfirmed, WorkspaceRevisionOutcomeUnknown, WorkspaceRevisionFailed, WorkspaceRevisionAbandoned},
	[]WorkspaceRevisionState{WorkspaceRevisionPrepared},
	[]WorkspaceRevisionState{WorkspaceRevisionConfirmed, WorkspaceRevisionFailed, WorkspaceRevisionAbandoned},
	map[WorkspaceRevisionState][]WorkspaceRevisionState{
		WorkspaceRevisionPrepared:       {WorkspaceRevisionAuthorized, WorkspaceRevisionAbandoned},
		WorkspaceRevisionAuthorized:     {WorkspaceRevisionPublishing, WorkspaceRevisionAbandoned},
		WorkspaceRevisionPublishing:     {WorkspaceRevisionConfirmed, WorkspaceRevisionOutcomeUnknown, WorkspaceRevisionFailed},
		WorkspaceRevisionOutcomeUnknown: {WorkspaceRevisionAuthorized, WorkspaceRevisionConfirmed, WorkspaceRevisionFailed, WorkspaceRevisionAbandoned},
		WorkspaceRevisionConfirmed:      {}, WorkspaceRevisionFailed: {}, WorkspaceRevisionAbandoned: {},
	})

func AllSnapshots() map[string]Snapshot {
	return map[string]Snapshot{
		Runs.Name(): Runs.Snapshot(), ToolCalls.Name(): ToolCalls.Snapshot(), Commands.Name(): Commands.Snapshot(),
		Attempts.Name(): Attempts.Snapshot(), Approvals.Name(): Approvals.Snapshot(), WorkspaceRevisions.Name(): WorkspaceRevisions.Snapshot(),
	}
}
