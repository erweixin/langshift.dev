package scheduler

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestWeightedDeficitRoundRobinChargesCostNotJobCount(t *testing.T) {
	now := time.Date(2026, time.July, 14, 18, 0, 0, 0, time.UTC)
	config := testConfig(60)
	config.Tenants["tenant-a"] = TenantPolicy{Weight: 2, ActiveConcurrencyCap: 100, BurstUnits: 1000, RefillUnitsPerSecond: 1000}
	jobs := append(testJobs(now, "tenant-a", "a", 100, 1, QueueInteractive, 10), testJobs(now, "tenant-b", "b", 100, 1, QueueInteractive, 10)...)
	plan, err := Select(now, config, State{}, Active{}, jobs)
	if err != nil {
		t.Fatal(err)
	}
	counts, units := decisionTotals(plan.Decisions)
	if counts["tenant-a"] != 40 || counts["tenant-b"] != 20 || units["tenant-a"] != 40 || units["tenant-b"] != 20 {
		t.Fatalf("weighted decisions counts=%v units=%v", counts, units)
	}

	config = testConfig(40)
	jobs = append(testJobs(now, "tenant-a", "a", 100, 3, QueueInteractive, 10), testJobs(now, "tenant-b", "b", 100, 1, QueueInteractive, 10)...)
	plan, err = Select(now, config, State{}, Active{}, jobs)
	if err != nil {
		t.Fatal(err)
	}
	counts, units = decisionTotals(plan.Decisions)
	if difference(units["tenant-a"], units["tenant-b"]) > 3 || counts["tenant-a"] >= counts["tenant-b"] {
		t.Fatalf("cost fairness counts=%v units=%v", counts, units)
	}
}

func TestReservationsPriorityShareCapsAndBackpressure(t *testing.T) {
	now := time.Date(2026, time.July, 14, 18, 0, 0, 0, time.UTC)
	config := testConfig(10)
	config.Resources["llm"] = ResourcePolicy{Capacity: 10, InteractiveReserved: 2, BackgroundReserved: 2, HighPriorityThreshold: 90, HighPriorityMaxPercentage: 50, QuantumUnits: 100}
	jobs := append(testJobs(now, "a-high", "high", 20, 1, QueueInteractive, 100), testJobs(now, "b-normal", "normal", 20, 1, QueueInteractive, 10)...)
	jobs = append(jobs, testJobs(now, "c-background", "background", 20, 1, QueueBackground, 10)...)
	plan, err := Select(now, config, State{}, Active{}, jobs)
	if err != nil {
		t.Fatal(err)
	}
	classes, high := map[QueueClass]int{}, 0
	for _, decision := range plan.Decisions {
		classes[decision.QueueClass]++
		if decision.Priority >= 90 {
			high++
		}
	}
	if len(plan.Decisions) != 10 || classes[QueueInteractive] < 2 || classes[QueueBackground] < 2 || high > 5 {
		t.Fatalf("decisions=%d classes=%v high=%d", len(plan.Decisions), classes, high)
	}

	active := Active{
		ByResource:       map[string]int{"llm": 1},
		ByTenantResource: map[TenantResource]int{{Tenant: "tenant-a", Resource: "llm"}: 1},
	}
	config = testConfig(3)
	config.DefaultTenant.ActiveConcurrencyCap = 2
	config.Tenants["tenant-a"] = TenantPolicy{Weight: 1, ActiveConcurrencyCap: 1, BurstUnits: 1000, RefillUnitsPerSecond: 1000}
	jobs = append(testJobs(now, "tenant-a", "a", 5, 1, QueueInteractive, 10), testJobs(now, "tenant-b", "b", 5, 1, QueueInteractive, 10)...)
	plan, err = Select(now, config, State{}, active, jobs)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Decisions) != 2 || plan.Decisions[0].TenantID != "tenant-b" || plan.Decisions[1].TenantID != "tenant-b" {
		t.Fatalf("active cap/resource gate decisions=%#v", plan.Decisions)
	}

	active.ByResource["llm"] = 3
	plan, err = Select(now, config, State{}, active, jobs)
	if err != nil || len(plan.Decisions) != 0 {
		t.Fatalf("backpressure plan=%#v error=%v", plan, err)
	}
}

func TestTokenBucketRefillAndRetryAge(t *testing.T) {
	now := time.Date(2026, time.July, 14, 18, 0, 0, 0, time.UTC)
	config := testConfig(10)
	config.DefaultTenant = TenantPolicy{Weight: 1, ActiveConcurrencyCap: 10, BurstUnits: 4, RefillUnitsPerSecond: 2}
	jobs := testJobs(now, "tenant-a", "a", 10, 1, QueueInteractive, 10)
	first, err := Select(now, config, State{}, Active{}, jobs)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Decisions) != 4 {
		t.Fatalf("initial burst=%d", len(first.Decisions))
	}
	remaining := removeDecisions(jobs, first.Decisions)
	blocked, err := Select(now, config, first.State, Active{}, remaining)
	if err != nil || len(blocked.Decisions) != 0 {
		t.Fatalf("unrefilled plan=%#v error=%v", blocked, err)
	}
	refilled, err := Select(now.Add(time.Second), config, blocked.State, Active{}, remaining)
	if err != nil || len(refilled.Decisions) != 2 {
		t.Fatalf("refilled=%d error=%v", len(refilled.Decisions), err)
	}

	config = testConfig(1)
	olderRetry := testJobs(now.Add(-time.Hour), "tenant-a", "retry", 1, 1, QueueInteractive, 50)[0]
	olderRetry.RetryCount = 7
	newer := testJobs(now.Add(-time.Minute), "tenant-a", "new", 1, 1, QueueInteractive, 50)[0]
	olderRetry.AvailableAt, newer.AvailableAt = now.Add(-time.Second), now.Add(-time.Second)
	dueAt := now.Add(time.Hour)
	olderRetry.DueAt, newer.DueAt = &dueAt, &dueAt
	plan, err := Select(now, config, State{}, Active{}, []Job{newer, olderRetry})
	if err != nil || len(plan.Decisions) != 1 || plan.Decisions[0].JobID != olderRetry.ID {
		t.Fatalf("retry age was reset: %#v error=%v", plan.Decisions, err)
	}
}

func TestSelectionIsInputOrderIndependentAndStarvationFree(t *testing.T) {
	now := time.Date(2026, time.July, 14, 18, 0, 0, 0, time.UTC)
	config := testConfig(30)
	var jobs []Job
	for tenant := 0; tenant < 10; tenant++ {
		jobs = append(jobs, testJobs(now, fmt.Sprintf("tenant-%02d", tenant), fmt.Sprintf("j-%02d", tenant), 100, int64(tenant%3+1), QueueClass([]string{"interactive", "background"}[tenant%2]), tenant%100)...)
	}
	baseline, err := Select(now, config, State{}, Active{}, jobs)
	if err != nil {
		t.Fatal(err)
	}
	random := rand.New(rand.NewSource(814))
	for iteration := 0; iteration < 1000; iteration++ {
		shuffled := append([]Job(nil), jobs...)
		random.Shuffle(len(shuffled), func(left, right int) { shuffled[left], shuffled[right] = shuffled[right], shuffled[left] })
		candidate, candidateErr := Select(now, config, State{}, Active{}, shuffled)
		if candidateErr != nil || !reflect.DeepEqual(candidate.Decisions, baseline.Decisions) {
			t.Fatalf("iteration=%d deterministic=%v error=%v", iteration, reflect.DeepEqual(candidate.Decisions, baseline.Decisions), candidateErr)
		}
	}

	config = testConfig(1)
	config.DefaultTenant = TenantPolicy{Weight: 1, ActiveConcurrencyCap: 1000, BurstUnits: 2000, RefillUnitsPerSecond: 2000}
	state := State{}
	counts := map[string]int{}
	remaining := jobs
	for iteration := 0; iteration < 1000; iteration++ {
		plan, selectErr := Select(now, config, state, Active{}, remaining)
		if selectErr != nil || len(plan.Decisions) != 1 {
			t.Fatalf("iteration=%d decisions=%d error=%v", iteration, len(plan.Decisions), selectErr)
		}
		state = plan.State
		counts[plan.Decisions[0].TenantID]++
		remaining = removeDecisions(remaining, plan.Decisions)
	}
	for tenant := 0; tenant < 10; tenant++ {
		name := fmt.Sprintf("tenant-%02d", tenant)
		if counts[name] != 100 {
			t.Fatalf("tenant %s scheduled %d times", name, counts[name])
		}
	}
}

func TestInvalidConfigurationAndJobsFailClosed(t *testing.T) {
	now := time.Now().UTC()
	if _, err := Select(now, Config{}, State{}, Active{}, nil); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("invalid config: %v", err)
	}
	config := testConfig(1)
	invalid := testJobs(now, "tenant-a", "a", 1, 1, QueueInteractive, 10)[0]
	invalid.CostUnits = config.DefaultTenant.BurstUnits + 1
	if _, err := Select(now, config, State{}, Active{}, []Job{invalid}); !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("unschedulable job: %v", err)
	}
	corrupt := State{Buckets: map[TenantResource]BucketState{{Tenant: "tenant-a", Resource: "llm"}: {Tokens: -1}}}
	if _, err := Select(now, config, corrupt, Active{}, nil); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("corrupt state: %v", err)
	}
	if _, err := Select(now, config, State{}, Active{ByResource: map[string]int{"llm": -1}}, nil); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("corrupt active counts: %v", err)
	}
}

func testConfig(capacity int) Config {
	return Config{
		Resources:     map[string]ResourcePolicy{"llm": {Capacity: capacity, InteractiveReserved: 0, BackgroundReserved: 0, HighPriorityThreshold: 90, HighPriorityMaxPercentage: 100, QuantumUnits: 1}},
		DefaultTenant: TenantPolicy{Weight: 1, ActiveConcurrencyCap: 100, BurstUnits: 1000, RefillUnitsPerSecond: 1000},
		Tenants:       map[string]TenantPolicy{}, BatchLimit: capacity,
	}
}

func testJobs(now time.Time, tenant, prefix string, count int, cost int64, class QueueClass, priority int) []Job {
	jobs := make([]Job, 0, count)
	for index := 0; index < count; index++ {
		dueAt := now.Add(time.Hour)
		jobs = append(jobs, Job{ID: fmt.Sprintf("%s-%04d", prefix, index), TenantID: tenant, ResourceClass: "llm", QueueClass: class, Priority: priority, CostUnits: cost, EnqueuedAt: now.Add(time.Duration(index) * time.Millisecond), AvailableAt: now.Add(-time.Second), DueAt: &dueAt})
	}
	return jobs
}

func decisionTotals(decisions []Decision) (map[string]int, map[string]int64) {
	counts, units := map[string]int{}, map[string]int64{}
	for _, decision := range decisions {
		counts[decision.TenantID]++
		units[decision.TenantID] += decision.CostUnits
	}
	return counts, units
}

func removeDecisions(jobs []Job, decisions []Decision) []Job {
	selected := map[string]bool{}
	for _, decision := range decisions {
		selected[decision.JobID] = true
	}
	remaining := make([]Job, 0, len(jobs)-len(decisions))
	for _, job := range jobs {
		if !selected[job.ID] {
			remaining = append(remaining, job)
		}
	}
	return remaining
}

func difference(left, right int64) int64 {
	if left > right {
		return left - right
	}
	return right - left
}
