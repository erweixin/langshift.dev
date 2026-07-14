package anonymousclaim

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

type memoryStore struct {
	mu     sync.Mutex
	saga   Saga
	writes atomic.Uint64
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
