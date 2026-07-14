package natsjs

import (
	"context"
	"errors"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
)

type PublishObservation struct {
	TenantID string
	Result   eventpostgres.PublishBatchResult
	Err      error
}

type OutboxRunner struct {
	Store          eventpostgres.OutboxStore
	Broker         Broker
	PollInterval   time.Duration
	TenantPageSize int
	BatchSize      int
	ShardIndex     int
	ShardCount     int
	Observe        func(PublishObservation)
}

func (runner OutboxRunner) Run(ctx context.Context) error {
	if err := runner.validate(); err != nil {
		return err
	}
	ticker := time.NewTicker(runner.PollInterval)
	defer ticker.Stop()
	for {
		if err := runner.cycle(ctx); err != nil {
			runner.observe(PublishObservation{Err: err})
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (runner OutboxRunner) cycle(ctx context.Context) error {
	epoch, err := runner.Store.Epochs.CurrentStoreEpoch(ctx)
	if err != nil || epoch == "" {
		return eventpostgres.ErrDeliveryConfiguration
	}
	after := ""
	for {
		tenants, listErr := runner.Store.ListReadyTenantIDs(ctx, epoch, after, runner.TenantPageSize, runner.ShardIndex, runner.ShardCount)
		if listErr != nil {
			return listErr
		}
		for _, tenantID := range tenants {
			result, publishErr := runner.Store.PublishBatch(ctx, runner.Broker, tenantID, epoch, runner.BatchSize)
			runner.observe(PublishObservation{TenantID: tenantID, Result: result, Err: publishErr})
			if errors.Is(publishErr, eventpostgres.ErrStaleStoreEpoch) || errors.Is(publishErr, eventpostgres.ErrDeliveryConfiguration) {
				return publishErr
			}
			if ctx.Err() != nil {
				return nil
			}
		}
		if len(tenants) < runner.TenantPageSize {
			return nil
		}
		after = tenants[len(tenants)-1]
	}
}

func (runner OutboxRunner) validate() error {
	if runner.Store.Pool == nil || runner.Store.Epochs == nil || runner.Broker.Publisher == nil || runner.PollInterval <= 0 || runner.TenantPageSize < 1 || runner.TenantPageSize > maximumRunnerTenantPage || runner.BatchSize < 1 || runner.BatchSize > 500 || runner.ShardCount < 1 || runner.ShardIndex < 0 || runner.ShardIndex >= runner.ShardCount {
		return ErrConfiguration
	}
	return nil
}

const maximumRunnerTenantPage = 5000

func (runner OutboxRunner) observe(observation PublishObservation) {
	if runner.Observe != nil {
		runner.Observe(observation)
	}
}
