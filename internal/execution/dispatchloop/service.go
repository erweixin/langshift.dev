// Package dispatchloop connects durable scheduler admission to the
// worker-visible command transport.
package dispatchloop

import (
	"context"
	"errors"
	"sort"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/execution/scheduler"
)

var ErrConfiguration = errors.New("dispatch loop configuration is invalid")

type Store interface {
	LoadActive(context.Context, string, int) (scheduler.Active, error)
	PlanResource(context.Context, scheduler.Config, scheduler.Active, string, string, string, int) ([]executionpostgres.DispatchClaim, error)
	MarkDispatched(context.Context, executionpostgres.DispatchClaim) error
	DeferDispatch(context.Context, executionpostgres.DispatchClaim, string) error
}

type Publisher interface {
	Publish(context.Context, eventpostgres.PublishedCommand) error
}

type EpochAuthority interface {
	CurrentStoreEpoch(context.Context) (string, error)
}

type Service struct {
	Store      Store
	Publisher  Publisher
	Epochs     EpochAuthority
	Config     scheduler.Config
	Owner      string
	Resources  []string
	BatchLimit int
	Interval   time.Duration
	ErrorCode  func(error) string
	Observe    func(Observation)
}

type Observation struct {
	Resource  string
	Planned   int
	Published int
	Deferred  int
	Err       error
}

func (service Service) Run(ctx context.Context) error {
	if err := service.validate(); err != nil {
		return err
	}
	if err := service.Cycle(ctx); err != nil && ctx.Err() == nil {
		service.observe(Observation{Err: err})
	}
	ticker := time.NewTicker(service.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := service.Cycle(ctx); err != nil && ctx.Err() == nil {
				service.observe(Observation{Err: err})
			}
		}
	}
}

func (service Service) Cycle(ctx context.Context) error {
	if err := service.validate(); err != nil {
		return err
	}
	epoch, err := service.Epochs.CurrentStoreEpoch(ctx)
	if err != nil || epoch == "" {
		if err == nil {
			err = ErrConfiguration
		}
		return err
	}
	resources := append([]string(nil), service.Resources...)
	sort.Strings(resources)
	var cycleErrors []error
	for _, resource := range resources {
		policy, exists := service.Config.Resources[resource]
		if !exists {
			cycleErrors = append(cycleErrors, ErrConfiguration)
			continue
		}
		observation := Observation{Resource: resource}
		active, loadErr := service.Store.LoadActive(ctx, resource, policy.HighPriorityThreshold)
		if loadErr != nil {
			observation.Err = loadErr
			service.observe(observation)
			cycleErrors = append(cycleErrors, loadErr)
			continue
		}
		claims, planErr := service.Store.PlanResource(ctx, service.Config, active, resource, service.Owner, epoch, service.BatchLimit)
		if errors.Is(planErr, executionpostgres.ErrSchedulerBusy) {
			service.observe(observation)
			continue
		}
		if planErr != nil {
			observation.Err = planErr
			service.observe(observation)
			cycleErrors = append(cycleErrors, planErr)
			continue
		}
		observation.Planned = len(claims)
		for _, claim := range claims {
			if publishErr := service.Publisher.Publish(ctx, claim.Candidate.Command); publishErr != nil {
				observation.Deferred++
				code := "dispatch_publish_failed"
				if service.ErrorCode != nil {
					if classified := service.ErrorCode(publishErr); classified != "" {
						code = classified
					}
				}
				if deferErr := service.Store.DeferDispatch(ctx, claim, code); deferErr != nil {
					cycleErrors = append(cycleErrors, deferErr)
				}
				continue
			}
			if markErr := service.Store.MarkDispatched(ctx, claim); markErr != nil {
				cycleErrors = append(cycleErrors, markErr)
				continue
			}
			observation.Published++
		}
		service.observe(observation)
	}
	return errors.Join(cycleErrors...)
}

func (service Service) validate() error {
	if service.Store == nil || service.Publisher == nil || service.Epochs == nil || service.Owner == "" || len(service.Resources) == 0 || service.BatchLimit < 1 || service.BatchLimit > 1000 || service.Interval <= 0 {
		return ErrConfiguration
	}
	seen := map[string]bool{}
	for _, resource := range service.Resources {
		if resource == "" || seen[resource] {
			return ErrConfiguration
		}
		seen[resource] = true
		if _, exists := service.Config.Resources[resource]; !exists {
			return ErrConfiguration
		}
	}
	return nil
}

func (service Service) observe(observation Observation) {
	if service.Observe != nil {
		service.Observe(observation)
	}
}
