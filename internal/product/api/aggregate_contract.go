package api

import (
	"context"
	"time"
)

type AggregateQueryCommand struct {
	CommandMetadata
	SnapshotID string
	MetricKey  string
	TimeBucket string
	Dimensions map[string]string
}

type AggregateQueryResult struct {
	ID                string    `json:"id"`
	Version           uint64    `json:"version"`
	Status            string    `json:"status"`
	UpdatedAt         time.Time `json:"updated_at"`
	SnapshotID        string    `json:"snapshot_id"`
	MetricKey         string    `json:"metric_key"`
	Value             *float64  `json:"value"`
	SuppressionReason *string   `json:"suppression_reason"`
	BudgetRemaining   int       `json:"budget_remaining"`
	Replayed          bool      `json:"replayed,omitempty"`
}

type AggregateQueryService interface {
	Query(context.Context, AggregateQueryCommand) (AggregateQueryResult, error)
}
