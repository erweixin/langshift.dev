// Package project defines the production Create aggregate state machines.
package project

import "errors"

var (
	ErrConflict          = errors.New("project state conflicts with expected version")
	ErrInvalidTransition = errors.New("project transition is not allowed")
	ErrIncompleteProject = errors.New("project completion requirements are not satisfied")
	ErrInvalidMilestone  = errors.New("milestone state is invalid")
)

type Status string

const (
	Draft     Status = "draft"
	Active    Status = "active"
	Blocked   Status = "blocked"
	Completed Status = "completed"
	Archived  Status = "archived"
)

type Project struct {
	ID      string
	Version uint64
	Status  Status
}

func (project Project) Transition(expected uint64, next Status) (Project, error) {
	if project.ID == "" || project.Version == 0 || expected != project.Version {
		return Project{}, ErrConflict
	}
	allowed := map[Status]map[Status]bool{
		Draft:     {Active: true, Archived: true},
		Active:    {Blocked: true, Completed: true, Archived: true},
		Blocked:   {Active: true, Completed: true, Archived: true},
		Completed: {Archived: true},
	}
	if !allowed[project.Status][next] {
		return Project{}, ErrInvalidTransition
	}
	project.Version++
	project.Status = next
	return project, nil
}

type MilestoneStatus string

const (
	Planned    MilestoneStatus = "planned"
	InProgress MilestoneStatus = "in_progress"
	Submitted  MilestoneStatus = "submitted"
	Verified   MilestoneStatus = "verified"
	Rework     MilestoneStatus = "rework"
	Done       MilestoneStatus = "completed"
)

type Milestone struct {
	ID, ProjectID string
	Version       uint64
	Status        MilestoneStatus
	Required      bool
}

type MilestoneCommand struct {
	ExpectedVersion   uint64
	Action            string
	ResultBound       bool
	VerificationBound bool
}

func (milestone Milestone) Transition(command MilestoneCommand) (Milestone, error) {
	if milestone.ID == "" || milestone.ProjectID == "" || milestone.Version == 0 || milestone.Version != command.ExpectedVersion {
		return Milestone{}, ErrConflict
	}
	next := map[string]MilestoneStatus{
		"start": InProgress, "submit": Submitted, "verify": Verified,
		"request_rework": Rework, "complete": Done,
	}[command.Action]
	allowed := map[MilestoneStatus]map[MilestoneStatus]bool{
		Planned:    {InProgress: true},
		InProgress: {Submitted: true},
		Submitted:  {Verified: true, Rework: true},
		Rework:     {Submitted: true},
		Verified:   {Done: true},
	}
	if next == "" || !allowed[milestone.Status][next] {
		return Milestone{}, ErrInvalidTransition
	}
	if next == Submitted && !command.ResultBound || next == Verified && !command.VerificationBound || next == Done && !command.VerificationBound {
		return Milestone{}, ErrInvalidMilestone
	}
	milestone.Version++
	milestone.Status = next
	return milestone, nil
}

type CompletionFacts struct {
	RequiredMilestones, CompletedRequired int
	WorkspaceBound, PassingValidation     bool
	ArtifactRevisionBound, EvidenceBound  bool
	ReflectionBound                       bool
}

func (facts CompletionFacts) Validate() error {
	if facts.RequiredMilestones < 2 || facts.CompletedRequired != facts.RequiredMilestones || !facts.WorkspaceBound || !facts.PassingValidation || !facts.ArtifactRevisionBound || !facts.EvidenceBound || !facts.ReflectionBound {
		return ErrIncompleteProject
	}
	return nil
}
