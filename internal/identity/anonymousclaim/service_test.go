package anonymousclaim

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memoryStore struct {
	mu     sync.Mutex
	saga   Saga
	writes atomic.Uint64
}

type idempotentDestination struct {
	mu      sync.Mutex
	effects map[string]string
	calls   int
}

func (destination *idempotentDestination) CommitDestination(_ context.Context, saga Saga) (string, error) {
	destination.mu.Lock()
	defer destination.mu.Unlock()
	destination.calls++
	if eventID := destination.effects[saga.ClaimKey]; eventID != "" {
		return eventID, nil
	}
	eventID := "event-" + saga.ClaimKey
	destination.effects[saga.ClaimKey] = eventID
	return eventID, nil
}

type idempotentEraser struct {
	mu       sync.Mutex
	receipts map[string]DeletionReceipt
	calls    map[string]int
}

func (eraser *idempotentEraser) Erase(_ context.Context, saga Saga, surface string) (DeletionReceipt, error) {
	eraser.mu.Lock()
	defer eraser.mu.Unlock()
	key := saga.ClaimKey + ":" + surface
	eraser.calls[key]++
	if receipt := eraser.receipts[key]; receipt.ID != "" {
		return receipt, nil
	}
	receipt := DeletionReceipt{ID: "receipt-" + surface, Surface: surface, Hash: "hash-" + surface, ErasedAt: time.Unix(1_800_000_100, 0).UTC(), Details: json.RawMessage(`{"verified":true}`)}
	eraser.receipts[key] = receipt
	return receipt, nil
}

type uncertainStore struct {
	*memoryStore
	unknownEveryWrite bool
}

func (store *uncertainStore) CompareAndSwap(ctx context.Context, previous, next Saga) error {
	err := store.memoryStore.CompareAndSwap(ctx, previous, next)
	if err == nil && store.unknownEveryWrite {
		return ErrVersionConflict
	}
	return err
}

func (store *memoryStore) Load(_ context.Context, _ string) (Saga, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.saga, nil
}
func (store *memoryStore) CompareAndSwap(_ context.Context, previous, next Saga) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.saga.Version != previous.Version {
		return ErrVersionConflict
	}
	store.saga = next
	store.writes.Add(1)
	return nil
}

func TestTenThousandConcurrentReservationReplaysCreateOneDurableEffect(t *testing.T) {
	store := &memoryStore{saga: Saga{ID: "anonymous-route-1", Status: Available, Version: 1}}
	service := Service{Store: store}
	reservation := Reservation{ClaimID: "anonymous-route-1", ClaimKey: "claim-key-1", TargetTenantID: "tenant-1", TargetUserID: "user-1", MissionID: "mission-1"}
	const requests = 10_000
	start := make(chan struct{})
	errorsFound := make(chan error, requests)
	var wait sync.WaitGroup
	for range requests {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			saga, err := service.Reserve(context.Background(), reservation)
			if err != nil {
				errorsFound <- err
				return
			}
			if saga.MissionID != "mission-1" || saga.Status != Reserved {
				errorsFound <- ErrInvariant
			}
		}()
	}
	close(start)
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("reservation failed: %v", err)
	}
	if writes := store.writes.Load(); writes != 1 {
		t.Fatalf("durable reservation writes=%d, want 1", writes)
	}
	final, _ := store.Load(context.Background(), reservation.ClaimID)
	if final.MissionID != "mission-1" || final.ClaimKey != "claim-key-1" {
		t.Fatalf("unexpected final binding: %#v", final)
	}
}

func TestDifferentClaimCannotStealReservation(t *testing.T) {
	store := &memoryStore{saga: Saga{ID: "route", Status: Reserved, Version: 2, ClaimKey: "first", TargetTenantID: "t", TargetUserID: "u", MissionID: "m"}}
	_, err := (Service{Store: store}).Reserve(context.Background(), Reservation{ClaimID: "route", ClaimKey: "second", TargetTenantID: "t", TargetUserID: "u", MissionID: "other"})
	if !errors.Is(err, ErrClaimTaken) {
		t.Fatalf("error=%v", err)
	}
	if store.writes.Load() != 0 {
		t.Fatal("competing claim wrote state")
	}
}

func TestReconcileConvergesAcrossUnknownCASResultsWithoutDuplicateEffects(t *testing.T) {
	base := &memoryStore{saga: Saga{ID: "route", Status: Reserved, Version: 2, ClaimKey: "claim-key", TargetTenantID: "tenant", TargetUserID: "user", MissionID: "mission"}}
	store := &uncertainStore{memoryStore: base, unknownEveryWrite: true}
	destination := &idempotentDestination{effects: map[string]string{}}
	eraser := &idempotentEraser{receipts: map[string]DeletionReceipt{}, calls: map[string]int{}}
	service := Service{Store: store, Destination: destination, Eraser: eraser}
	final, err := service.Reconcile(context.Background(), "route")
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != Claimed || final.MissionID != "mission" || len(final.DeletionReceipts) != len(requiredReceipts) {
		t.Fatalf("final=%#v", final)
	}
	if len(destination.effects) != 1 {
		t.Fatalf("destination effects=%d", len(destination.effects))
	}
	if len(eraser.receipts) != len(requiredReceipts) {
		t.Fatalf("erasure effects=%d", len(eraser.receipts))
	}
	replayed, err := service.Reconcile(context.Background(), "route")
	if err != nil || replayed.Status != Claimed || len(destination.effects) != 1 || len(eraser.receipts) != len(requiredReceipts) {
		t.Fatalf("replay=%#v error=%v", replayed, err)
	}
}
