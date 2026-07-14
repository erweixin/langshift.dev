package postgres

import (
	"context"
	"errors"
	"testing"
	"time"
)

type epochAuthorityStub struct {
	epoch string
	err   error
}

func (stub epochAuthorityStub) CurrentStoreEpoch(context.Context) (string, error) {
	return stub.epoch, stub.err
}

func TestEpochAuthorityFailsClosed(t *testing.T) {
	ctx := t.Context()
	if err := requireCurrentEpoch(ctx, nil, "epoch"); !errors.Is(err, ErrDeliveryConfiguration) {
		t.Fatalf("nil authority error=%v", err)
	}
	if err := requireCurrentEpoch(ctx, epochAuthorityStub{err: errors.New("vault unavailable")}, "epoch"); !errors.Is(err, ErrDeliveryConfiguration) {
		t.Fatalf("unavailable authority error=%v", err)
	}
	if err := requireCurrentEpoch(ctx, epochAuthorityStub{epoch: "other"}, "epoch"); !errors.Is(err, ErrStaleStoreEpoch) {
		t.Fatalf("stale authority error=%v", err)
	}
}

func TestOutboxBackoffIsBounded(t *testing.T) {
	store := OutboxStore{RetryBase: time.Second, RetryLimit: 8 * time.Second}
	wants := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second}
	for index, want := range wants {
		if got := store.backoff(index + 1); got != want {
			t.Fatalf("attempt=%d got=%s want=%s", index+1, got, want)
		}
	}
}

func TestDeliveredCommandRequiresRecoveryAndPayloadIdentity(t *testing.T) {
	valid := DeliveredCommand{TenantID: "tenant", StoreEpoch: "epoch", CommandID: "command", CommandType: "type", AggregateKind: "kind", AggregateID: "aggregate", PayloadRef: "ref", PayloadHash: "hash"}
	if !validDeliveredCommand(valid) {
		t.Fatal("valid command rejected")
	}
	valid.PayloadHash = ""
	if validDeliveredCommand(valid) {
		t.Fatal("payload identity omission accepted")
	}
}

func TestPublishedCommandPreservesConsumerSecurityIdentity(t *testing.T) {
	published := PublishedCommand{TenantID: "tenant", StoreEpoch: "epoch", CommandID: "command", CommandType: "type", AggregateKind: "kind", AggregateID: "aggregate", PayloadRef: "ref", PayloadHash: "hash"}
	if delivered := published.Delivered(); !validDeliveredCommand(delivered) || delivered.AggregateID != published.AggregateID || delivered.PayloadHash != published.PayloadHash || delivered.StoreEpoch != published.StoreEpoch {
		t.Fatalf("delivered=%#v", delivered)
	}
}
