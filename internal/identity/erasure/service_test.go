package erasure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type memoryStore struct {
	mu       sync.Mutex
	requests map[string]Request
	receipts map[string]Receipt
}

func (store *memoryStore) Claim(_ context.Context, tenantID, requestID string, now time.Time) (Request, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	request := store.requests[requestID]
	if request.TenantID != tenantID || now.Before(request.ScheduledFor) {
		return Request{}, false, ErrNotDue
	}
	completed := request.Status == "completed"
	if request.Status == "requested" {
		request.Status = "processing"
		store.requests[requestID] = request
	}
	return request, completed, nil
}
func (store *memoryStore) LoadReceipt(_ context.Context, _ string, id string, surface Surface, epoch string) (Receipt, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	receipt, ok := store.receipts[id+":"+string(surface)+":"+epoch]
	return receipt, ok, nil
}
func (store *memoryStore) RecordReceipt(_ context.Context, receipt Receipt) (Receipt, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := receipt.RequestID + ":" + string(receipt.Surface) + ":" + receipt.RecoveryEpoch
	if current, exists := store.receipts[key]; exists {
		return current, true, nil
	}
	receipt.ID = fmt.Sprintf("receipt-%d", len(store.receipts)+1)
	store.receipts[key] = receipt
	return receipt, false, nil
}
func (store *memoryStore) Complete(_ context.Context, request Request, _ string, receipts []Receipt, _ time.Time) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	request.Status = "completed"
	request.Version++
	store.requests[request.ID] = request
	return manifestHash(receipts), nil
}
func (store *memoryStore) PendingRestore(_ context.Context, epoch string, _ int) ([]Request, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var result []Request
	for _, request := range store.requests {
		if request.Status != "completed" {
			continue
		}
		missing := false
		for _, surface := range RequiredSurfaces {
			if _, ok := store.receipts[request.ID+":"+string(surface)+":"+epoch]; !ok {
				missing = true
			}
		}
		if missing {
			result = append(result, request)
		}
	}
	return result, nil
}

type countingEraser struct {
	mu    sync.Mutex
	calls map[string]int
}

type faultEraser struct {
	mu       sync.Mutex
	calls    int
	failOnce bool
}

func (eraser *faultEraser) Erase(_ context.Context, _ Request, _ string) (json.RawMessage, error) {
	eraser.mu.Lock()
	defer eraser.mu.Unlock()
	eraser.calls++
	if eraser.failOnce {
		eraser.failOnce = false
		return nil, errors.New("injected surface failure")
	}
	return json.RawMessage(`{"verified_absent":true}`), nil
}

func (eraser *countingEraser) Erase(_ context.Context, request Request, epoch string) (json.RawMessage, error) {
	eraser.mu.Lock()
	defer eraser.mu.Unlock()
	eraser.calls[request.ID+":"+epoch]++
	return json.RawMessage(`{"verified_absent":true}`), nil
}

func TestEverySurfaceFailureResumesFromDurableReceipts(t *testing.T) {
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	for failedIndex, failedSurface := range RequiredSurfaces {
		t.Run(string(failedSurface), func(t *testing.T) {
			store := &memoryStore{
				requests: map[string]Request{"request": {ID: "request", TenantID: "tenant", UserID: "user", Version: 1, Status: "requested", ScheduledFor: now.Add(-time.Hour)}},
				receipts: map[string]Receipt{},
			}
			erasers := make(map[Surface]SurfaceEraser, len(RequiredSurfaces))
			faults := make(map[Surface]*faultEraser, len(RequiredSurfaces))
			for _, surface := range RequiredSurfaces {
				fault := &faultEraser{failOnce: surface == failedSurface}
				faults[surface] = fault
				erasers[surface] = fault
			}
			service := Service{Store: store, Erasers: erasers, RecoveryEpoch: "epoch", ReceiptKey: make([]byte, 32), Now: func() time.Time { return now }}
			if _, err := service.Process(context.Background(), "tenant", "request"); err == nil {
				t.Fatal("injected failure was accepted")
			}
			if len(store.receipts) != failedIndex {
				t.Fatalf("receipts before retry=%d want=%d", len(store.receipts), failedIndex)
			}
			result, err := service.Process(context.Background(), "tenant", "request")
			if err != nil || !result.Completed || len(result.Receipts) != len(RequiredSurfaces) || len(store.receipts) != len(RequiredSurfaces) {
				t.Fatalf("result=%#v receipts=%d err=%v", result, len(store.receipts), err)
			}
			for index, surface := range RequiredSurfaces {
				want := 1
				if index == failedIndex {
					want = 2
				}
				if faults[surface].calls != want {
					t.Fatalf("surface=%s calls=%d want=%d", surface, faults[surface].calls, want)
				}
			}
		})
	}
}

func TestHundredAccountsReplayAndRestoreProduceCompleteReceipts(t *testing.T) {
	now := time.Date(2026, 7, 17, 7, 0, 0, 0, time.UTC)
	store := &memoryStore{requests: map[string]Request{}, receipts: map[string]Receipt{}}
	eraser := &countingEraser{calls: map[string]int{}}
	erasers := map[Surface]SurfaceEraser{}
	for _, surface := range RequiredSurfaces {
		erasers[surface] = eraser
	}
	for index := 0; index < 100; index++ {
		id := fmt.Sprintf("request-%03d", index)
		store.requests[id] = Request{ID: id, TenantID: "tenant", UserID: fmt.Sprintf("user-%03d", index), Version: 1, Status: "requested", ScheduledFor: now.Add(-time.Hour)}
	}
	service := Service{Store: store, Erasers: erasers, RecoveryEpoch: "epoch-a", ReceiptKey: make([]byte, 32), Now: func() time.Time { return now }}
	for id := range store.requests {
		first, err := service.Process(context.Background(), "tenant", id)
		if err != nil || len(first.Receipts) != 6 || !first.Completed {
			t.Fatalf("first %s result=%#v err=%v", id, first, err)
		}
		second, err := service.Process(context.Background(), "tenant", id)
		if err != nil || !second.Replayed || len(second.Receipts) != 6 || second.ManifestHash != first.ManifestHash {
			t.Fatalf("replay %s result=%#v err=%v", id, second, err)
		}
	}
	if len(store.receipts) != 600 {
		t.Fatalf("initial receipts=%d", len(store.receipts))
	}
	restore := Service{Store: store, Erasers: erasers, RecoveryEpoch: "epoch-b", ReceiptKey: make([]byte, 32)}
	results, err := restore.ReconcileRestore(context.Background(), 100)
	if err != nil || len(results) != 100 {
		t.Fatalf("restore=%d err=%v", len(results), err)
	}
	if len(store.receipts) != 1200 {
		t.Fatalf("restore receipts=%d", len(store.receipts))
	}
	again, err := restore.ReconcileRestore(context.Background(), 100)
	if err != nil || len(again) != 0 {
		t.Fatalf("second restore=%d err=%v", len(again), err)
	}
}
