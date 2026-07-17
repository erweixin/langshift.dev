package api

import (
	"context"
	"time"
)

type CreatePortfolioExportCommand struct {
	CommandMetadata
	ProjectID                       string
	ExpectedProjectVersion          uint64
	ExpectedWorkspaceBindingVersion uint64
	WorkspaceRevision               string
	Format                          string
	ArtifactRevisionIDs             []string
}

type PortfolioExportResult struct {
	ID                   string     `json:"id"`
	ProjectID            string     `json:"project_id"`
	RunID                string     `json:"run_id"`
	Status               string     `json:"status"`
	Version              uint64     `json:"version"`
	Format               string     `json:"format"`
	RevisionManifestHash string     `json:"revision_manifest_hash"`
	ContentHash          *string    `json:"content_hash"`
	MediaType            *string    `json:"media_type"`
	ByteSize             *int64     `json:"byte_size"`
	CreatedAt            time.Time  `json:"created_at"`
	StartedAt            *time.Time `json:"started_at"`
	CompletedAt          *time.Time `json:"completed_at"`
	ExpiresAt            *time.Time `json:"expires_at"`
	FailureCode          *string    `json:"failure_code"`
	Replayed             bool       `json:"replayed"`
}

type PortfolioExportService interface {
	Request(context.Context, CreatePortfolioExportCommand) (PortfolioExportResult, error)
	Get(context.Context, string, string, string) (PortfolioExportResult, error)
}
