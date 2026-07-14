package anonymousclaim

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

const (
	claimGateRequests          = 10_000
	claimGateFaultRepetitions  = 100
	claimGateConcurrentWorkers = 128
	claimGateCleanupWorkers    = 32
)

var errInjectedCrash = errors.New("injected process crash")

type claimGateMetrics struct {
	ClaimRequests         int                  `json:"claim_requests"`
	ConcurrentWorkers     int                  `json:"concurrent_workers"`
	ReplayRequests        int                  `json:"replay_requests"`
	ExpiryCleanupAttempts int                  `json:"expiry_cleanup_attempts"`
	SuccessfulClaims      int                  `json:"successful_claim_records"`
	MissionEffects        int                  `json:"mission_effects"`
	DeletionReceipts      int                  `json:"deletion_receipts"`
	ExpiredAfterReserved  int                  `json:"expired_after_reserved"`
	ManualReview          int                  `json:"manual_review"`
	FaultRepetitions      int                  `json:"fault_repetitions_per_boundary"`
	FaultRuns             int                  `json:"fault_runs"`
	ReconcileRedeliveries int                  `json:"reconcile_redeliveries"`
	UnknownCommitResults  int                  `json:"unknown_commit_results"`
	DuplicateMissions     int                  `json:"duplicate_missions"`
	IncompleteReceiptRuns int                  `json:"incomplete_receipt_runs"`
	Boundaries            []claimFaultBoundary `json:"boundaries"`
}

type claimFaultBoundary struct {
	Name       string `json:"name"`
	Position   string `json:"position"`
	Iterations int    `json:"iterations"`
	Recovered  int    `json:"recovered"`
}

var gateMetrics claimGateMetrics

func TestMain(m *testing.M) {
	code := m.Run()
	if output := os.Getenv("LITES_CLAIM_GATE_MODEL_REPORT"); output != "" {
		report := struct {
			SchemaVersion string           `json:"schema_version"`
			SourceCommit  string           `json:"source_commit"`
			GeneratedAt   time.Time        `json:"generated_at"`
			Status        string           `json:"status"`
			Metrics       claimGateMetrics `json:"metrics"`
		}{
			SchemaVersion: "1.0.0",
			SourceCommit:  os.Getenv("LITES_SOURCE_COMMIT"),
			GeneratedAt:   time.Now().UTC(),
			Status:        "failed",
			Metrics:       gateMetrics,
		}
		if code == 0 && claimGatePassed(gateMetrics) {
			report.Status = "passed"
		}
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil || os.WriteFile(output, append(encoded, '\n'), 0o600) != nil {
			code = 1
		}
	}
	os.Exit(code)
}

func claimGatePassed(metrics claimGateMetrics) bool {
	return metrics.ClaimRequests == claimGateRequests &&
		metrics.ConcurrentWorkers == claimGateConcurrentWorkers+claimGateCleanupWorkers &&
		metrics.ReplayRequests == claimGateRequests &&
		metrics.ExpiryCleanupAttempts == claimGateRequests &&
		metrics.SuccessfulClaims == 1 &&
		metrics.MissionEffects == 1 &&
		metrics.DeletionReceipts == len(requiredReceipts) &&
		metrics.ExpiredAfterReserved == 0 &&
		metrics.ManualReview == 0 &&
		metrics.FaultRepetitions == claimGateFaultRepetitions &&
		metrics.FaultRuns == len(claimFaultCases())*claimGateFaultRepetitions &&
		metrics.ReconcileRedeliveries == metrics.FaultRuns &&
		metrics.UnknownCommitResults == claimGateFaultRepetitions &&
		metrics.DuplicateMissions == 0 &&
		metrics.IncompleteReceiptRuns == 0 &&
		len(metrics.Boundaries) == len(claimFaultCases()) &&
		allFaultBoundariesRecovered(metrics.Boundaries)
}

func allFaultBoundariesRecovered(boundaries []claimFaultBoundary) bool {
	for _, boundary := range boundaries {
		if boundary.Iterations != claimGateFaultRepetitions || boundary.Recovered != boundary.Iterations {
			return false
		}
	}
	return true
}

func TestTenThousandFullClaimReplaysConvergeDuringExpiryCleanup(t *testing.T) {
	ctx := context.Background()
	expiresAt := time.Unix(1_800_100_000, 0).UTC()
	store := &memoryStore{saga: Saga{ID: "anonymous-route-gate", AnonymousSubjectID: "anonymous-subject-gate", Status: Available, Version: 1, ClaimKey: "claim-key-gate", ExpiresAt: expiresAt}}
	destination := &idempotentDestination{effects: map[string]string{}}
	eraser := &idempotentEraser{receipts: map[string]DeletionReceipt{}, calls: map[string]int{}}
	reservation := Reservation{ClaimID: "anonymous-route-gate", ClaimKey: "claim-key-gate", TargetTenantID: "tenant-gate", TargetUserID: "user-gate", MissionID: "mission-gate"}
	claimService := Service{Store: store, Destination: destination, Eraser: eraser, Now: func() time.Time { return expiresAt.Add(-time.Nanosecond) }}
	cleanupService := Service{Store: store, Now: func() time.Time { return expiresAt }}
	if _, err := claimService.Reserve(ctx, reservation); err != nil {
		t.Fatal(err)
	}

	claimJobs := make(chan struct{})
	cleanupJobs := make(chan struct{})
	errorsFound := make(chan error, claimGateRequests*2)
	var wait sync.WaitGroup
	for range claimGateConcurrentWorkers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range claimJobs {
				final, err := claimService.Claim(ctx, reservation)
				if err != nil {
					errorsFound <- err
					continue
				}
				if final.Status != Claimed {
					errorsFound <- ErrInvariant
				}
			}
		}()
	}
	for range claimGateCleanupWorkers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range cleanupJobs {
				state, err := cleanupService.ExpireAvailable(ctx, reservation.ClaimID)
				if err != nil {
					errorsFound <- err
					continue
				}
				if state.Status == Expired {
					errorsFound <- ErrInvariant
				}
			}
		}()
	}
	for range claimGateRequests {
		claimJobs <- struct{}{}
		cleanupJobs <- struct{}{}
	}
	close(claimJobs)
	close(cleanupJobs)
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("claim gate operation: %v", err)
	}
	final, err := store.Load(ctx, reservation.ClaimID)
	if err != nil || final.Status != Claimed || len(final.DeletionReceipts) != len(requiredReceipts) {
		t.Fatalf("final=%#v error=%v", final, err)
	}
	if len(destination.effects) != 1 || len(eraser.receipts) != len(requiredReceipts) {
		t.Fatalf("missions=%d receipts=%d", len(destination.effects), len(eraser.receipts))
	}
	gateMetrics.ClaimRequests = claimGateRequests
	gateMetrics.ConcurrentWorkers = claimGateConcurrentWorkers + claimGateCleanupWorkers
	gateMetrics.ReplayRequests = claimGateRequests
	gateMetrics.ExpiryCleanupAttempts = claimGateRequests
	gateMetrics.SuccessfulClaims = 1
	gateMetrics.MissionEffects = len(destination.effects)
	gateMetrics.DeletionReceipts = len(final.DeletionReceipts)
	if final.Status == Expired {
		gateMetrics.ExpiredAfterReserved = 1
	}
	if final.Status == ManualReview {
		gateMetrics.ManualReview = 1
	}
}

type claimFaultCase struct {
	name     string
	position string
}

func claimFaultCases() []claimFaultCase {
	cases := []claimFaultCase{
		{name: "reserved", position: "before"},
		{name: "reserved", position: "after"},
		{name: "destination_mission_commit", position: "before"},
		{name: "destination_mission_commit", position: "after"},
		{name: "enter_erasing", position: "before"},
		{name: "enter_erasing", position: "after"},
	}
	for _, surface := range requiredReceipts {
		cases = append(cases,
			claimFaultCase{name: "deletion_receipt:" + surface, position: "before"},
			claimFaultCase{name: "deletion_receipt:" + surface, position: "after"},
		)
	}
	return cases
}

func TestClaimCrashMatrixAutomaticallyReconciles(t *testing.T) {
	metrics := claimGateMetrics{FaultRepetitions: claimGateFaultRepetitions}
	for _, fault := range claimFaultCases() {
		boundary := claimFaultBoundary{Name: fault.name, Position: fault.position, Iterations: claimGateFaultRepetitions}
		for iteration := range claimGateFaultRepetitions {
			result := runClaimFaultCase(t, fault, iteration)
			boundary.Recovered++
			metrics.FaultRuns++
			metrics.ReconcileRedeliveries += result.redeliveries
			metrics.UnknownCommitResults += result.unknownResults
			metrics.DuplicateMissions += result.duplicateMissions
			metrics.IncompleteReceiptRuns += result.incompleteReceipts
			metrics.ExpiredAfterReserved += result.expiredAfterReserved
			metrics.ManualReview += result.manualReview
		}
		metrics.Boundaries = append(metrics.Boundaries, boundary)
	}
	gateMetrics.FaultRepetitions = metrics.FaultRepetitions
	gateMetrics.FaultRuns = metrics.FaultRuns
	gateMetrics.ReconcileRedeliveries = metrics.ReconcileRedeliveries
	gateMetrics.UnknownCommitResults = metrics.UnknownCommitResults
	gateMetrics.DuplicateMissions = metrics.DuplicateMissions
	gateMetrics.IncompleteReceiptRuns = metrics.IncompleteReceiptRuns
	gateMetrics.ExpiredAfterReserved += metrics.ExpiredAfterReserved
	gateMetrics.ManualReview += metrics.ManualReview
	gateMetrics.Boundaries = metrics.Boundaries
}

type faultResult struct {
	redeliveries         int
	unknownResults       int
	duplicateMissions    int
	incompleteReceipts   int
	expiredAfterReserved int
	manualReview         int
}

func runClaimFaultCase(t *testing.T, fault claimFaultCase, iteration int) faultResult {
	t.Helper()
	ctx := context.Background()
	expiresAt := time.Unix(1_800_200_000, int64(iteration)).UTC()
	baseStore := &memoryStore{saga: Saga{ID: "route", AnonymousSubjectID: "subject", Status: Available, Version: 1, ClaimKey: "claim-key", ExpiresAt: expiresAt}}
	store := &faultStore{memoryStore: baseStore, fault: fault, armed: true}
	baseDestination := &idempotentDestination{effects: map[string]string{}}
	destination := &faultDestination{base: baseDestination, fault: fault, armed: true}
	baseEraser := &idempotentEraser{receipts: map[string]DeletionReceipt{}, calls: map[string]int{}}
	service := Service{Store: store, Destination: destination, Eraser: baseEraser, Now: func() time.Time { return expiresAt.Add(-time.Nanosecond) }}
	reservation := Reservation{ClaimID: "route", ClaimKey: "claim-key", TargetTenantID: "tenant", TargetUserID: "user", MissionID: "mission"}
	result := faultResult{}
	for restart := 0; restart < 4; restart++ {
		final, err := service.Claim(ctx, reservation)
		if err == nil {
			if final.Status != Claimed {
				t.Fatalf("fault=%+v iteration=%d final=%#v", fault, iteration, final)
			}
			break
		}
		if !errors.Is(err, errInjectedCrash) {
			t.Fatalf("fault=%+v iteration=%d error=%v", fault, iteration, err)
		}
		result.redeliveries++
		if fault.position == "after" && fault.name == "destination_mission_commit" {
			result.unknownResults++
		}
		state, loadErr := baseStore.Load(ctx, reservation.ClaimID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		cleanupNow := expiresAt.Add(-time.Nanosecond)
		if state.Status != Available {
			cleanupNow = expiresAt
		}
		cleanup := Service{Store: baseStore, Now: func() time.Time { return cleanupNow }}
		cleaned, cleanupErr := cleanup.ExpireAvailable(ctx, reservation.ClaimID)
		if cleanupErr != nil {
			t.Fatalf("fault=%+v cleanup error=%v", fault, cleanupErr)
		}
		if state.Status != Available && cleaned.Status == Expired {
			result.expiredAfterReserved++
		}
	}
	final, err := baseStore.Load(ctx, reservation.ClaimID)
	if err != nil || final.Status != Claimed {
		t.Fatalf("fault=%+v iteration=%d final=%#v error=%v", fault, iteration, final, err)
	}
	if len(baseDestination.effects) != 1 {
		result.duplicateMissions = len(baseDestination.effects) - 1
	}
	if len(final.DeletionReceipts) != len(requiredReceipts) {
		result.incompleteReceipts++
	}
	if final.Status == ManualReview {
		result.manualReview++
	}
	return result
}

type faultStore struct {
	*memoryStore
	fault claimFaultCase
	armed bool
}

func (store *faultStore) CompareAndSwap(ctx context.Context, previous, next Saga) error {
	boundary := claimTransitionBoundary(previous, next)
	if store.armed && boundary == store.fault.name && store.fault.position == "before" {
		store.armed = false
		return errInjectedCrash
	}
	err := store.memoryStore.CompareAndSwap(ctx, previous, next)
	if err == nil && store.armed && boundary == store.fault.name && store.fault.position == "after" {
		store.armed = false
		return errInjectedCrash
	}
	return err
}

func claimTransitionBoundary(previous, next Saga) string {
	switch {
	case previous.Status == Available && next.Status == Reserved:
		return "reserved"
	case previous.Status == DestinationCommitted && next.Status == Erasing:
		return "enter_erasing"
	case previous.Status == Erasing && next.Status == Erasing:
		for _, receipt := range next.DeletionReceipts {
			if !HasDeletionReceipt(previous, receipt.Surface) {
				return "deletion_receipt:" + receipt.Surface
			}
		}
	}
	return ""
}

type faultDestination struct {
	base  *idempotentDestination
	fault claimFaultCase
	armed bool
}

func (destination *faultDestination) CommitDestination(ctx context.Context, saga Saga) (string, error) {
	if destination.armed && destination.fault.name == "destination_mission_commit" && destination.fault.position == "before" {
		destination.armed = false
		return "", errInjectedCrash
	}
	eventID, err := destination.base.CommitDestination(ctx, saga)
	if err == nil && destination.armed && destination.fault.name == "destination_mission_commit" && destination.fault.position == "after" {
		destination.armed = false
		return "", errInjectedCrash
	}
	return eventID, err
}
