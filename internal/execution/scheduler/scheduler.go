// Package scheduler implements deterministic, cost-aware admission planning.
// It does not own business truth: selected Commands still pass through the
// EventService inbox and fenced Worker claim protocol.
package scheduler

import (
	"errors"
	"sort"
	"time"
)

var (
	ErrInvalidConfiguration = errors.New("scheduler configuration is invalid")
	ErrInvalidJob           = errors.New("scheduler job is invalid or unschedulable")
	ErrInvalidState         = errors.New("scheduler durable state is invalid")
)

const (
	maximumUnits       = int64(1_000_000_000_000)
	maximumWeight      = int64(1_000_000)
	maximumConcurrency = 1_000_000
)

type QueueClass string

const (
	QueueInteractive QueueClass = "interactive"
	QueueBackground  QueueClass = "background"
)

type Job struct {
	ID, TenantID, ResourceClass string
	QueueClass                  QueueClass
	Priority                    int
	CostUnits                   int64
	EnqueuedAt, AvailableAt     time.Time
	DueAt                       *time.Time
	RetryCount                  int
}

type TenantPolicy struct {
	Weight               int64 `json:"weight"`
	ActiveConcurrencyCap int   `json:"active_concurrency_cap"`
	BurstUnits           int64 `json:"burst_units"`
	RefillUnitsPerSecond int64 `json:"refill_units_per_second"`
}

type ResourcePolicy struct {
	Capacity                  int   `json:"capacity"`
	InteractiveReserved       int   `json:"interactive_reserved"`
	BackgroundReserved        int   `json:"background_reserved"`
	HighPriorityThreshold     int   `json:"high_priority_threshold"`
	HighPriorityMaxPercentage int   `json:"high_priority_max_percentage"`
	QuantumUnits              int64 `json:"quantum_units"`
}

type Config struct {
	Resources     map[string]ResourcePolicy `json:"resources"`
	DefaultTenant TenantPolicy              `json:"default_tenant"`
	Tenants       map[string]TenantPolicy   `json:"tenants"`
	BatchLimit    int                       `json:"batch_limit"`
}

type Active struct {
	ByResource             map[string]int
	ByResourceQueue        map[ResourceQueue]int
	ByTenantResource       map[TenantResource]int
	HighPriorityByResource map[string]int
}

type ResourceQueue struct {
	Resource string
	Queue    QueueClass
}

type TenantResource struct {
	Tenant, Resource string
}

type BucketState struct {
	Tokens          int64
	LastRefill      time.Time
	RefillRemainder int64
}

type State struct {
	Deficit          map[TenantResource]int64
	Buckets          map[TenantResource]BucketState
	CursorByResource map[string]string
}

type Decision struct {
	JobID, TenantID, ResourceClass string
	QueueClass                     QueueClass
	CostUnits                      int64
	Priority                       int
}

type Plan struct {
	Decisions []Decision
	State     State
}

func Select(now time.Time, config Config, state State, active Active, jobs []Job) (Plan, error) {
	if !validConfig(config) {
		return Plan{}, ErrInvalidConfiguration
	}
	if !validState(config, state) || !validActive(active) {
		return Plan{}, ErrInvalidState
	}
	now = now.UTC()
	next := cloneState(state)
	grouped := make(map[string]map[string][]Job)
	for _, job := range jobs {
		if !validJob(job) {
			return Plan{}, ErrInvalidJob
		}
		if _, known := config.Resources[job.ResourceClass]; !known || job.CostUnits > tenantPolicy(config, job.TenantID).BurstUnits {
			return Plan{}, ErrInvalidJob
		}
		if job.AvailableAt.After(now) || job.DueAt != nil && !job.DueAt.After(now) {
			continue
		}
		if grouped[job.ResourceClass] == nil {
			grouped[job.ResourceClass] = map[string][]Job{}
		}
		grouped[job.ResourceClass][job.TenantID] = append(grouped[job.ResourceClass][job.TenantID], job)
	}
	resources := make([]string, 0, len(grouped))
	for resource := range grouped {
		resources = append(resources, resource)
	}
	sort.Strings(resources)
	plan := Plan{State: next}
	for _, resource := range resources {
		remainingBatch := config.BatchLimit - len(plan.Decisions)
		if remainingBatch <= 0 {
			break
		}
		decisions := selectResource(now, config, &next, active, resource, grouped[resource], remainingBatch)
		plan.Decisions = append(plan.Decisions, decisions...)
	}
	plan.State = next
	return plan, nil
}

func selectResource(now time.Time, config Config, state *State, active Active, resource string, tenantJobs map[string][]Job, batchLimit int) []Decision {
	resourcePolicy := config.Resources[resource]
	availableCapacity := resourcePolicy.Capacity - active.ByResource[resource]
	if availableCapacity <= 0 || batchLimit <= 0 {
		return nil
	}
	if availableCapacity > batchLimit {
		availableCapacity = batchLimit
	}
	tenants := make([]string, 0, len(tenantJobs))
	for tenant, jobs := range tenantJobs {
		tenants = append(tenants, tenant)
		sort.SliceStable(jobs, func(left, right int) bool { return jobLess(jobs[left], jobs[right]) })
		tenantJobs[tenant] = jobs
		key := TenantResource{Tenant: tenant, Resource: resource}
		policy := tenantPolicy(config, tenant)
		state.Buckets[key] = refillBucket(now, state.Buckets[key], policy)
	}
	sort.Strings(tenants)
	start := cursorStart(tenants, state.CursorByResource[resource])
	selectedByTenant := map[string]int{}
	selectedByQueue := map[QueueClass]int{}
	highSelected := 0
	decisions := make([]Decision, 0, availableCapacity)
	maxRounds := maxSchedulingRounds(resourcePolicy, config, tenants, tenantJobs)
	lastTenant := state.CursorByResource[resource]
	for round := 0; round < maxRounds && len(decisions) < availableCapacity; round++ {
		progress := false
		for offset := 0; offset < len(tenants) && len(decisions) < availableCapacity; offset++ {
			tenant := tenants[(start+offset)%len(tenants)]
			jobsForTenant := tenantJobs[tenant]
			if len(jobsForTenant) == 0 {
				continue
			}
			policy := tenantPolicy(config, tenant)
			key := TenantResource{Tenant: tenant, Resource: resource}
			deficit := state.Deficit[key] + resourcePolicy.QuantumUnits*policy.Weight
			if deficit > policy.BurstUnits {
				deficit = policy.BurstUnits
			}
			state.Deficit[key] = deficit
			bucket := state.Buckets[key]
			for len(decisions) < availableCapacity && active.ByTenantResource[key]+selectedByTenant[tenant] < policy.ActiveConcurrencyCap {
				jobsForTenant = tenantJobs[tenant]
				index := pickJob(jobsForTenant, state.Deficit[key], bucket.Tokens, resourcePolicy, active, resource, selectedByQueue, highSelected, tenantJobs)
				if index < 0 {
					break
				}
				job := jobsForTenant[index]
				tenantJobs[tenant] = append(jobsForTenant[:index], jobsForTenant[index+1:]...)
				state.Deficit[key] -= job.CostUnits
				bucket.Tokens -= job.CostUnits
				state.Buckets[key] = bucket
				selectedByTenant[tenant]++
				selectedByQueue[job.QueueClass]++
				if job.Priority >= resourcePolicy.HighPriorityThreshold {
					highSelected++
				}
				decisions = append(decisions, Decision{JobID: job.ID, TenantID: job.TenantID, ResourceClass: job.ResourceClass, QueueClass: job.QueueClass, CostUnits: job.CostUnits, Priority: job.Priority})
				lastTenant = tenant
				progress = true
			}
		}
		if !progress && !canAccrueUsefulDeficit(config, resource, tenants, tenantJobs, state.Deficit) {
			break
		}
	}
	state.CursorByResource[resource] = lastTenant
	return decisions
}

func pickJob(jobs []Job, deficit, tokens int64, resourcePolicy ResourcePolicy, active Active, resource string, selectedByQueue map[QueueClass]int, highSelected int, all map[string][]Job) int {
	interactiveBacklog, backgroundBacklog, normalBacklog := backlogKinds(all, resourcePolicy.HighPriorityThreshold)
	for index, job := range jobs {
		if job.CostUnits > deficit || job.CostUnits > tokens {
			continue
		}
		remainingAfter := resourcePolicy.Capacity - active.ByResource[resource] - selectedByQueue[QueueInteractive] - selectedByQueue[QueueBackground] - 1
		if job.QueueClass == QueueInteractive && backgroundBacklog {
			needed := resourcePolicy.BackgroundReserved - active.ByResourceQueue[ResourceQueue{Resource: resource, Queue: QueueBackground}] - selectedByQueue[QueueBackground]
			if needed > 0 && remainingAfter < needed {
				continue
			}
		}
		if job.QueueClass == QueueBackground && interactiveBacklog {
			needed := resourcePolicy.InteractiveReserved - active.ByResourceQueue[ResourceQueue{Resource: resource, Queue: QueueInteractive}] - selectedByQueue[QueueInteractive]
			if needed > 0 && remainingAfter < needed {
				continue
			}
		}
		if job.Priority >= resourcePolicy.HighPriorityThreshold && normalBacklog {
			maximum := resourcePolicy.Capacity * resourcePolicy.HighPriorityMaxPercentage / 100
			if maximum < 1 {
				maximum = 1
			}
			if active.HighPriorityByResource[resource]+highSelected >= maximum {
				continue
			}
		}
		return index
	}
	return -1
}

func backlogKinds(all map[string][]Job, highThreshold int) (bool, bool, bool) {
	interactive, background, normal := false, false, false
	for _, jobs := range all {
		for _, job := range jobs {
			interactive = interactive || job.QueueClass == QueueInteractive
			background = background || job.QueueClass == QueueBackground
			normal = normal || job.Priority < highThreshold
		}
	}
	return interactive, background, normal
}

func refillBucket(now time.Time, bucket BucketState, policy TenantPolicy) BucketState {
	if bucket.LastRefill.IsZero() {
		return BucketState{Tokens: policy.BurstUnits, LastRefill: now}
	}
	if !now.After(bucket.LastRefill) {
		return bucket
	}
	elapsedMillis := now.Sub(bucket.LastRefill) / time.Millisecond
	if elapsedMillis <= 0 {
		return bucket
	}
	maximumMillis := policy.BurstUnits*1000/policy.RefillUnitsPerSecond + 1000
	if elapsedMillis > time.Duration(maximumMillis) {
		elapsedMillis = time.Duration(maximumMillis)
	}
	numerator := int64(elapsedMillis)*policy.RefillUnitsPerSecond + bucket.RefillRemainder
	bucket.Tokens += numerator / 1000
	bucket.RefillRemainder = numerator % 1000
	if bucket.Tokens >= policy.BurstUnits {
		bucket.Tokens = policy.BurstUnits
		bucket.RefillRemainder = 0
	}
	bucket.LastRefill = bucket.LastRefill.Add(time.Duration(elapsedMillis) * time.Millisecond)
	if bucket.LastRefill.Before(now) && bucket.Tokens == policy.BurstUnits {
		bucket.LastRefill = now
	}
	return bucket
}

func jobLess(left, right Job) bool {
	if left.QueueClass != right.QueueClass {
		return left.QueueClass == QueueInteractive
	}
	if left.Priority != right.Priority {
		return left.Priority > right.Priority
	}
	if !left.EnqueuedAt.Equal(right.EnqueuedAt) {
		return left.EnqueuedAt.Before(right.EnqueuedAt)
	}
	return left.ID < right.ID
}

func cursorStart(tenants []string, previous string) int {
	if len(tenants) == 0 || previous == "" {
		return 0
	}
	index := sort.SearchStrings(tenants, previous)
	if index < len(tenants) && tenants[index] == previous {
		return (index + 1) % len(tenants)
	}
	return index % len(tenants)
}

func tenantPolicy(config Config, tenant string) TenantPolicy {
	if policy, exists := config.Tenants[tenant]; exists {
		return policy
	}
	return config.DefaultTenant
}

func maxSchedulingRounds(resource ResourcePolicy, config Config, tenants []string, jobs map[string][]Job) int {
	maxCost := int64(1)
	count := 0
	for _, tenant := range tenants {
		for _, job := range jobs[tenant] {
			count++
			if job.CostUnits > maxCost {
				maxCost = job.CostUnits
			}
		}
	}
	minimumQuantum := resource.QuantumUnits
	if minimumQuantum < 1 {
		minimumQuantum = 1
	}
	return count + int(maxCost/minimumQuantum) + 2
}

func canAccrueUsefulDeficit(config Config, resource string, tenants []string, jobs map[string][]Job, deficits map[TenantResource]int64) bool {
	for _, tenant := range tenants {
		policy := tenantPolicy(config, tenant)
		if len(jobs[tenant]) > 0 && deficits[TenantResource{Tenant: tenant, Resource: resource}] < policy.BurstUnits {
			return true
		}
	}
	return false
}

func validConfig(config Config) bool {
	if config.BatchLimit < 1 || config.BatchLimit > maximumConcurrency || !validTenantPolicy(config.DefaultTenant) || len(config.Resources) == 0 {
		return false
	}
	for _, policy := range config.Tenants {
		if !validTenantPolicy(policy) {
			return false
		}
	}
	for resource, policy := range config.Resources {
		if resource == "" || policy.Capacity < 1 || policy.Capacity > maximumConcurrency || policy.QuantumUnits < 1 || policy.QuantumUnits > maximumUnits || policy.InteractiveReserved < 0 || policy.BackgroundReserved < 0 || policy.InteractiveReserved+policy.BackgroundReserved > policy.Capacity || policy.HighPriorityThreshold < 0 || policy.HighPriorityMaxPercentage < 1 || policy.HighPriorityMaxPercentage > 100 {
			return false
		}
	}
	return true
}

func ValidateConfig(config Config) error {
	if !validConfig(config) {
		return ErrInvalidConfiguration
	}
	return nil
}

func validTenantPolicy(policy TenantPolicy) bool {
	return policy.Weight > 0 && policy.Weight <= maximumWeight && policy.ActiveConcurrencyCap > 0 && policy.ActiveConcurrencyCap <= maximumConcurrency && policy.BurstUnits > 0 && policy.BurstUnits <= maximumUnits && policy.RefillUnitsPerSecond > 0 && policy.RefillUnitsPerSecond <= maximumUnits
}

func validJob(job Job) bool {
	return job.ID != "" && job.TenantID != "" && job.ResourceClass != "" && (job.QueueClass == QueueInteractive || job.QueueClass == QueueBackground) && job.Priority >= 0 && job.CostUnits > 0 && job.CostUnits <= maximumUnits && !job.EnqueuedAt.IsZero() && !job.AvailableAt.IsZero() && (job.DueAt == nil || !job.DueAt.IsZero()) && job.RetryCount >= 0
}

func validState(config Config, state State) bool {
	for key, deficit := range state.Deficit {
		if key.Tenant == "" || key.Resource == "" || deficit < 0 || deficit > tenantPolicy(config, key.Tenant).BurstUnits {
			return false
		}
		if _, known := config.Resources[key.Resource]; !known {
			return false
		}
	}
	for key, bucket := range state.Buckets {
		if key.Tenant == "" || key.Resource == "" || bucket.Tokens < 0 || bucket.Tokens > tenantPolicy(config, key.Tenant).BurstUnits || bucket.RefillRemainder < 0 || bucket.RefillRemainder >= 1000 {
			return false
		}
		if _, known := config.Resources[key.Resource]; !known {
			return false
		}
	}
	for resource := range state.CursorByResource {
		if _, known := config.Resources[resource]; !known {
			return false
		}
	}
	return true
}

func validActive(active Active) bool {
	for resource, count := range active.ByResource {
		if resource == "" || count < 0 {
			return false
		}
	}
	for key, count := range active.ByResourceQueue {
		if key.Resource == "" || (key.Queue != QueueInteractive && key.Queue != QueueBackground) || count < 0 {
			return false
		}
	}
	for key, count := range active.ByTenantResource {
		if key.Tenant == "" || key.Resource == "" || count < 0 {
			return false
		}
	}
	for resource, count := range active.HighPriorityByResource {
		if resource == "" || count < 0 {
			return false
		}
	}
	return true
}

func cloneState(state State) State {
	cloned := State{Deficit: map[TenantResource]int64{}, Buckets: map[TenantResource]BucketState{}, CursorByResource: map[string]string{}}
	for key, value := range state.Deficit {
		cloned.Deficit[key] = value
	}
	for key, value := range state.Buckets {
		cloned.Buckets[key] = value
	}
	for key, value := range state.CursorByResource {
		cloned.CursorByResource[key] = value
	}
	return cloned
}
