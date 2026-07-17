package project

import (
	"errors"
	"testing"
)

func TestProjectStateMachineRejectsSkippedAndTerminalTransitions(t *testing.T) {
	project := Project{ID: "project", Version: 1, Status: Draft}
	if _, err := project.Transition(1, Completed); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("draft completion error=%v", err)
	}
	active, err := project.Transition(1, Active)
	if err != nil || active.Version != 2 || active.Status != Active {
		t.Fatalf("active=%#v err=%v", active, err)
	}
	completed, err := active.Transition(2, Completed)
	if err != nil || completed.Version != 3 {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
	if _, err = completed.Transition(3, Active); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("terminal reversal error=%v", err)
	}
	if _, err = active.Transition(1, Blocked); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale transition error=%v", err)
	}
}

func TestMilestoneRequiresResultThenVerification(t *testing.T) {
	milestone := Milestone{ID: "milestone", ProjectID: "project", Version: 1, Status: Planned, Required: true}
	inProgress, err := milestone.Transition(MilestoneCommand{ExpectedVersion: 1, Action: "start"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = inProgress.Transition(MilestoneCommand{ExpectedVersion: 2, Action: "submit"}); !errors.Is(err, ErrInvalidMilestone) {
		t.Fatalf("unbound submit error=%v", err)
	}
	submitted, err := inProgress.Transition(MilestoneCommand{ExpectedVersion: 2, Action: "submit", ResultBound: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = submitted.Transition(MilestoneCommand{ExpectedVersion: 3, Action: "complete", VerificationBound: true}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("skipped verification error=%v", err)
	}
	verified, err := submitted.Transition(MilestoneCommand{ExpectedVersion: 3, Action: "verify", VerificationBound: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = verified.Transition(MilestoneCommand{ExpectedVersion: 4, Action: "complete"}); !errors.Is(err, ErrInvalidMilestone) {
		t.Fatalf("completion without verification binding error=%v", err)
	}
}

func TestCompletionFactsRequireFullCreateJourney(t *testing.T) {
	valid := CompletionFacts{RequiredMilestones: 2, CompletedRequired: 2, WorkspaceBound: true, PassingValidation: true, ArtifactRevisionBound: true, EvidenceBound: true, ReflectionBound: true}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := []CompletionFacts{
		{RequiredMilestones: 1, CompletedRequired: 1, WorkspaceBound: true, PassingValidation: true, ArtifactRevisionBound: true, EvidenceBound: true, ReflectionBound: true},
		{RequiredMilestones: 2, CompletedRequired: 1, WorkspaceBound: true, PassingValidation: true, ArtifactRevisionBound: true, EvidenceBound: true, ReflectionBound: true},
		{RequiredMilestones: 2, CompletedRequired: 2, WorkspaceBound: true, PassingValidation: false, ArtifactRevisionBound: true, EvidenceBound: true, ReflectionBound: true},
		{RequiredMilestones: 2, CompletedRequired: 2, WorkspaceBound: true, PassingValidation: true, ArtifactRevisionBound: false, EvidenceBound: true, ReflectionBound: true},
		{RequiredMilestones: 2, CompletedRequired: 2, WorkspaceBound: true, PassingValidation: true, ArtifactRevisionBound: true, EvidenceBound: true, ReflectionBound: false},
	}
	for index, value := range cases {
		if err := value.Validate(); !errors.Is(err, ErrIncompleteProject) {
			t.Fatalf("case %d error=%v", index, err)
		}
	}
}
