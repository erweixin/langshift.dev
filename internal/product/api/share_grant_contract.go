package api

import (
	"context"
	"time"
)

type CreateShareGrantCommand struct {
	CommandMetadata
	GranteeUserID    string
	ResourceKind     string
	ResourceID       string
	ResourceRevision string
	Scope            []string
	ExpiresAt        *time.Time
}

type RevokeShareGrantCommand struct {
	CommandMetadata
	GrantID              string
	Reason               string
	ExpectedGrantVersion uint64
}

type ShareGrantResult struct {
	ID        string    `json:"id"`
	Version   uint64    `json:"version"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
	Replayed  bool      `json:"replayed,omitempty"`
}

type ShareGrantAccessQuery struct {
	TenantID, GranteeUserID                           string
	ResourceKind, ResourceID, ResourceRevision, Scope string
}

type ShareGrantService interface {
	Create(context.Context, CreateShareGrantCommand) (ShareGrantResult, error)
	Revoke(context.Context, RevokeShareGrantCommand) (ShareGrantResult, error)
	Authorize(context.Context, ShareGrantAccessQuery) (bool, error)
}
