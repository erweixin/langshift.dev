package dispatchloop

import (
	"context"
	"errors"
	"testing"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/execution/scheduler"
)

type fakeEpoch struct{ epoch string }

func (epoch fakeEpoch) CurrentStoreEpoch(context.Context) (string, error) { return epoch.epoch, nil }

type fakeStore struct {
	claims           []executionpostgres.DispatchClaim
	active           scheduler.Active
	marked, deferred []string
	loaded           []string
}

func (store *fakeStore) LoadActive(_ context.Context, resource string, _ int) (scheduler.Active, error) {
	store.loaded = append(store.loaded, resource)
	return store.active, nil
}
func (store *fakeStore) PlanResource(_ context.Context, _ scheduler.Config, _ scheduler.Active, resource, _, _ string, _ int) ([]executionpostgres.DispatchClaim, error) {
	result := make([]executionpostgres.DispatchClaim, 0, len(store.claims))
	for _, claim := range store.claims {
		if claim.Candidate.ResourceClass == resource {
			result = append(result, claim)
		}
	}
	return result, nil
}
func (store *fakeStore) MarkDispatched(_ context.Context, claim executionpostgres.DispatchClaim) error {
	store.marked = append(store.marked, claim.Candidate.ID)
	return nil
}
func (store *fakeStore) DeferDispatch(_ context.Context, claim executionpostgres.DispatchClaim, _ string) error {
	store.deferred = append(store.deferred, claim.Candidate.ID)
	return nil
}

type fakePublisher struct {
	fail      map[string]bool
	published []string
}

func (publisher *fakePublisher) Publish(_ context.Context, command eventpostgres.PublishedCommand) error {
	if publisher.fail[command.CommandID] {
		return errors.New("broker unavailable")
	}
	publisher.published = append(publisher.published, command.CommandID)
	return nil
}

func TestCyclePublishesOnlyAdmittedClaimsAndDefersBrokerFailures(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	claim := func(id, resource string) executionpostgres.DispatchClaim {
		return executionpostgres.DispatchClaim{Candidate: executionpostgres.SchedulerCandidate{Job: scheduler.Job{ID: id, TenantID: "tenant", ResourceClass: resource, QueueClass: scheduler.QueueInteractive, EnqueuedAt: now.Add(-250 * time.Millisecond)}, Command: eventpostgres.PublishedCommand{CommandID: id, QueueGeneration: 1, DispatchVersion: 1}}}
	}
	store := &fakeStore{claims: []executionpostgres.DispatchClaim{claim("a", "llm"), claim("b", "llm"), claim("c", "tool")}}
	publisher := &fakePublisher{fail: map[string]bool{"b": true}}
	config := scheduler.Config{Resources: map[string]scheduler.ResourcePolicy{"llm": {HighPriorityThreshold: 90}, "tool": {HighPriorityThreshold: 80}}}
	var observations []Observation
	service := Service{Store: store, Publisher: publisher, Epochs: fakeEpoch{"epoch"}, Config: config, Owner: "scheduler-a", Resources: []string{"tool", "llm"}, BatchLimit: 10, Interval: time.Second, Now: func() time.Time { return now }, Observe: func(value Observation) { observations = append(observations, value) }}
	if err := service.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.loaded) != 2 || store.loaded[0] != "llm" || store.loaded[1] != "tool" {
		t.Fatalf("resource order %#v", store.loaded)
	}
	if len(store.marked) != 2 || store.marked[0] != "a" || store.marked[1] != "c" {
		t.Fatalf("marked %#v", store.marked)
	}
	if len(store.deferred) != 1 || store.deferred[0] != "b" {
		t.Fatalf("deferred %#v", store.deferred)
	}
	if len(observations) != 2 || observations[0].Planned != 2 || observations[0].Published != 1 || observations[0].Deferred != 1 {
		t.Fatalf("observations %#v", observations)
	}
	if waits := observations[0].QueueWaits; len(waits) != 1 || waits[0].QueueClass != "interactive" || waits[0].Duration != 250*time.Millisecond {
		t.Fatalf("queue waits %#v", waits)
	}
	if waits := observations[1].QueueWaits; len(waits) != 1 || waits[0].Duration != 250*time.Millisecond {
		t.Fatalf("tool queue waits %#v", waits)
	}
}

func TestServiceRejectsDuplicateResources(t *testing.T) {
	service := Service{Store: &fakeStore{}, Publisher: &fakePublisher{}, Epochs: fakeEpoch{"epoch"}, Config: scheduler.Config{Resources: map[string]scheduler.ResourcePolicy{"llm": {}}}, Owner: "owner", Resources: []string{"llm", "llm"}, BatchLimit: 1, Interval: time.Second}
	if !errors.Is(service.Cycle(context.Background()), ErrConfiguration) {
		t.Fatal("expected configuration error")
	}
}
