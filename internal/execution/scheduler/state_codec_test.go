package scheduler

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestStateEncodingIsCanonicalAndStrict(t *testing.T) {
	config := testConfig(10)
	now := time.Date(2026, time.July, 14, 19, 0, 0, 123456000, time.UTC)
	state := State{
		Deficit:          map[TenantResource]int64{{Tenant: "tenant-b", Resource: "llm"}: 3, {Tenant: "tenant-a", Resource: "llm"}: 2},
		Buckets:          map[TenantResource]BucketState{{Tenant: "tenant-a", Resource: "llm"}: {Tokens: 9, LastRefill: now, RefillRemainder: 4}},
		CursorByResource: map[string]string{"llm": "tenant-b"},
	}
	first, err := EncodeState(config, state)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EncodeState(config, State{Deficit: map[TenantResource]int64{{Tenant: "tenant-a", Resource: "llm"}: 2, {Tenant: "tenant-b", Resource: "llm"}: 3}, Buckets: state.Buckets, CursorByResource: state.CursorByResource})
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("non-canonical state: %s != %s error=%v", first, second, err)
	}
	decoded, err := DecodeState(config, first)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := EncodeState(config, decoded)
	if err != nil || !bytes.Equal(first, roundTrip) {
		t.Fatalf("round trip: %s != %s error=%v", first, roundTrip, err)
	}
	for _, invalid := range [][]byte{
		nil,
		[]byte(`{"version":2,"deficits":[],"buckets":[],"cursors":[]}`),
		[]byte(`{"version":1,"deficits":[],"buckets":[],"cursors":[],"unknown":1}`),
		[]byte(`{"version":1,"deficits":[{"tenant":"a","resource":"llm","units":1},{"tenant":"a","resource":"llm","units":2}],"buckets":[],"cursors":[]}`),
		[]byte(`{"version":1,"deficits":[],"buckets":[{"tenant":"a","resource":"llm","tokens":-1,"last_refill":"2026-07-14T19:00:00Z","remainder":0}],"cursors":[]}`),
	} {
		if _, decodeErr := DecodeState(config, invalid); !errors.Is(decodeErr, ErrInvalidStateEncoding) {
			t.Fatalf("invalid encoding accepted: %s error=%v", invalid, decodeErr)
		}
	}
}
