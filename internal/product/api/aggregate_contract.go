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

type AggregateSnapshotCommand struct {
	CommandMetadata
	MetricKey              string
	Dimensions             map[string]string
	TimeBucket             string
	PeriodStart, PeriodEnd time.Time
}

type AggregateSnapshotResult struct {
	ID                  string            `json:"id"`
	Version             uint64            `json:"version"`
	Status              string            `json:"status"`
	MetricKey           string            `json:"metric_key"`
	Dimensions          map[string]string `json:"dimensions"`
	TimeBucket          string            `json:"time_bucket"`
	PeriodStart         string            `json:"period_start"`
	PeriodEnd           string            `json:"period_end"`
	PopulationCount     int               `json:"population_count"`
	CellCount           int               `json:"cell_count"`
	SourceHighWatermark string            `json:"source_high_watermark"`
	FrozenAt            time.Time         `json:"frozen_at"`
	Replayed            bool              `json:"replayed"`
}

type AggregateSnapshotService interface {
	Create(context.Context, AggregateSnapshotCommand) (AggregateSnapshotResult, error)
}
