package api

import (
	"context"
	"encoding/json"
	"time"
)

type EnterpriseResource struct {
	ID        string    `json:"id"`
	Version   uint64    `json:"version"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
	Replayed  bool      `json:"replayed,omitempty"`
}

type CreateProgramCommand struct {
	CommandMetadata
	Name     string
	Settings json.RawMessage
}

type UpdateProgramCommand struct {
	CommandMetadata
	ProgramID       string
	Name            string
	Status          string
	Settings        json.RawMessage
	ExpectedVersion uint64
}

type CreateCohortCommand struct {
	CommandMetadata
	ProgramID        string
	Name             string
	StartsAt, EndsAt *time.Time
}

type EnrollCohortCommand struct {
	CommandMetadata
	CohortID        string
	UserIDs         []string
	ExpectedVersion uint64
}

type UnenrollCohortCommand struct {
	CommandMetadata
	CohortID, TargetUserID, Reason string
	ExpectedVersion                uint64
}

type PublishRolePackCommand struct {
	CommandMetadata
	ProgramID                       string
	Revision                        int
	RoleProfileIDs, TaskTemplateIDs []string
}

type EnterpriseAdminService interface {
	CreateProgram(context.Context, CreateProgramCommand) (EnterpriseResource, error)
	UpdateProgram(context.Context, UpdateProgramCommand) (EnterpriseResource, error)
	CreateCohort(context.Context, CreateCohortCommand) (EnterpriseResource, error)
	EnrollCohort(context.Context, EnrollCohortCommand) (EnterpriseResource, error)
	UnenrollCohort(context.Context, UnenrollCohortCommand) (EnterpriseResource, error)
	PublishRolePack(context.Context, PublishRolePackCommand) (EnterpriseResource, error)
}
