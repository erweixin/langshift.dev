package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/langshift/lites/internal/identity/erasure"
)

func TestDecodeAccountErasureWorkIsExactAndBounded(t *testing.T) {
	work, err := decodeAccountErasureWork([]byte(`{"request_id":"request","user_id":"user","scheduled_for":"2026-08-17T00:00:00Z"}`))
	if err != nil || work.RequestID != "request" || work.UserID != "user" {
		t.Fatalf("work=%#v err=%v", work, err)
	}
	for _, body := range []string{
		`{"request_id":"request","user_id":"user","scheduled_for":"2026-08-17T00:00:00Z","extra":true}`,
		`{"request_id":"","user_id":"user","scheduled_for":"2026-08-17T00:00:00Z"}`,
		`{"request_id":"request","user_id":"user","scheduled_for":"bad"}`,
		`{"request_id":"request","user_id":"user","scheduled_for":"2026-08-17T00:00:00Z"}{}`,
	} {
		if _, err = decodeAccountErasureWork([]byte(body)); !errors.Is(err, erasure.ErrInvalid) {
			t.Fatalf("body=%s err=%v", body, err)
		}
	}
}

type erasureResumeStore struct {
	receipt erasure.Receipt
	exists  bool
	err     error
}

func (store erasureResumeStore) Claim(context.Context, string, string, time.Time) (erasure.Request, bool, error) {
	return erasure.Request{}, false, errors.New("unexpected claim")
}
func (store erasureResumeStore) LoadReceipt(context.Context, string, string, erasure.Surface, string) (erasure.Receipt, bool, error) {
	return store.receipt, store.exists, store.err
}
func (store erasureResumeStore) RecordReceipt(context.Context, erasure.Receipt) (erasure.Receipt, bool, error) {
	return erasure.Receipt{}, false, errors.New("unexpected record")
}
func (store erasureResumeStore) Complete(context.Context, erasure.Request, string, []erasure.Receipt, time.Time) (string, error) {
	return "", errors.New("unexpected complete")
}
func (store erasureResumeStore) PendingRestore(context.Context, string, int) ([]erasure.Request, error) {
	return nil, errors.New("unexpected restore")
}

func TestPayloadlessErasureRetryRequiresExactCurrentEpochReceipt(t *testing.T) {
	valid := erasure.Receipt{TenantID: "tenant", RequestID: "request", RecoveryEpoch: "epoch", Surface: erasure.Payload}
	resumable, err := canResumeErasureAfterPayloadPurge(context.Background(), erasureResumeStore{receipt: valid, exists: true}, "tenant", "request", "epoch")
	if err != nil || !resumable {
		t.Fatalf("valid receipt resumable=%t err=%v", resumable, err)
	}
	for name, store := range map[string]erasureResumeStore{
		"missing":       {},
		"wrong epoch":   {receipt: erasure.Receipt{TenantID: "tenant", RequestID: "request", RecoveryEpoch: "old", Surface: erasure.Payload}, exists: true},
		"wrong tenant":  {receipt: erasure.Receipt{TenantID: "other", RequestID: "request", RecoveryEpoch: "epoch", Surface: erasure.Payload}, exists: true},
		"wrong surface": {receipt: erasure.Receipt{TenantID: "tenant", RequestID: "request", RecoveryEpoch: "epoch", Surface: erasure.Memory}, exists: true},
	} {
		resumable, err = canResumeErasureAfterPayloadPurge(context.Background(), store, "tenant", "request", "epoch")
		if err != nil || resumable {
			t.Fatalf("%s resumable=%t err=%v", name, resumable, err)
		}
	}
	expected := errors.New("receipt unavailable")
	if _, err = canResumeErasureAfterPayloadPurge(context.Background(), erasureResumeStore{err: expected}, "tenant", "request", "epoch"); !errors.Is(err, expected) {
		t.Fatalf("receipt error=%v", err)
	}
}
