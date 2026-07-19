//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	billingpostgres "github.com/langshift/lites/internal/billing/postgres"
	contractsapi "github.com/langshift/lites/internal/contracts/api"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
)

func TestInternalUsageServiceReservesReleasesAndReplaysWithoutAccountingDrift(t *testing.T) {
	ctx := context.Background()
	admin := contractPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := contractPool(t, ctx, "LITES_TEST_CONTRACT_DATABASE_URL")
	defer pool.Close()

	const (
		tenantID  = "7b000000-0000-4000-8000-000000000001"
		userID    = "7b000000-0000-4000-8000-000000000002"
		bucketID  = "7b000000-0000-4000-8000-000000000003"
		subjectID = "7b000000-0000-4000-8000-000000000004"
		epoch     = "7b000000-0000-4000-8000-000000000005"
	)
	now := time.Date(2026, time.July, 18, 4, 0, 0, 0, time.UTC)
	if _, err := admin.Exec(ctx, `INSERT INTO identity.users(id,normalized_email,email_verified_at,locale,status) VALUES($1,'internal-usage@lites.invalid',$2,'en','active')`, userID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'enterprise','Internal Usage','active','US',$2)`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO contracts.credit_buckets(id,tenant_id,bucket_kind,granted_units,starts_at,expires_at,created_at,updated_at) VALUES($1,$2,'shared',100,$3,$4,$5,$5)`, bucketID, tenantID, now.Add(-time.Hour), now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}

	blobs := &memoryPayloadStore{values: map[string][]byte{}}
	appender := eventpostgres.Appender{Now: func() time.Time { return now }}
	billing := billingpostgres.Store{Pool: pool, Appender: appender, Epochs: internalUsageEpochStub{epoch}, StoreEpoch: epoch, IDKey: bytes.Repeat([]byte{0x7b}, 32), Now: func() time.Time { return now }}
	service := InternalUsageService{Pool: pool, Billing: billing, Payloads: blobs, IDKey: bytes.Repeat([]byte{0x7c}, 32), Now: func() time.Time { return now }}
	reserveCommand := contractsapi.InternalUsageReserveCommand{RequestID: "usage-reserve-request-0001", IdempotencyKey: "usage-reserve-key-0001", WorkloadIdentity: "spiffe://lites.internal/workload/tool-runner", TenantID: tenantID, UserID: userID, OperationKey: "tool-operation-0001", RequestedUnits: 40, SubjectKind: "tool_call", SubjectID: subjectID, SubjectVersion: 1}

	reserved, err := service.Reserve(ctx, reserveCommand)
	if err != nil || reserved.Status != "reserved" || reserved.ReservedUnits != 40 || reserved.ReservationID == "" || reserved.Replayed {
		t.Fatalf("reserved=%#v err=%v", reserved, err)
	}
	replayedReserve, err := service.Reserve(ctx, reserveCommand)
	if err != nil || !replayedReserve.Replayed || replayedReserve.ReservationID != reserved.ReservationID {
		t.Fatalf("reserve replay=%#v err=%v", replayedReserve, err)
	}
	tamperedReserve := reserveCommand
	tamperedReserve.RequestedUnits = 41
	if _, err = service.Reserve(ctx, tamperedReserve); !errors.Is(err, contractsapi.ErrStateConflict) {
		t.Fatalf("tampered reserve err=%v", err)
	}

	releaseCommand := contractsapi.InternalUsageReleaseCommand{RequestID: "usage-release-request-0001", IdempotencyKey: "usage-release-key-0001", WorkloadIdentity: reserveCommand.WorkloadIdentity, TenantID: tenantID, ReservationID: reserved.ReservationID, Reason: "cancelled_before_dispatch", ExpectedVersion: 1}
	released, err := service.Release(ctx, releaseCommand)
	if err != nil || released.Status != "released" || released.ReleasedUnits != 40 || released.LedgerEntryID == "" || released.Replayed {
		t.Fatalf("released=%#v err=%v", released, err)
	}
	replayedRelease, err := service.Release(ctx, releaseCommand)
	if err != nil || !replayedRelease.Replayed || replayedRelease.LedgerEntryID != released.LedgerEntryID {
		t.Fatalf("release replay=%#v err=%v", replayedRelease, err)
	}
	tamperedRelease := releaseCommand
	tamperedRelease.Reason = "different_reason"
	if _, err = service.Release(ctx, tamperedRelease); !errors.Is(err, contractsapi.ErrStateConflict) {
		t.Fatalf("tampered release err=%v", err)
	}

	var reservedUnits, settledUnits uint64
	var reservations, ledgerEntries, events, outbox int
	if err = admin.QueryRow(ctx, `SELECT reserved_units,settled_units,
      (SELECT count(*) FROM contracts.usage_reservations WHERE tenant_id=$2),
      (SELECT count(*) FROM contracts.usage_ledger WHERE tenant_id=$2),
      (SELECT count(*) FROM agent.events WHERE tenant_id=$2 AND event_type IN ('UsageReserved','UsageReleased')),
      (SELECT count(*) FROM agent.outbox WHERE tenant_id=$2)
      FROM contracts.credit_buckets WHERE tenant_id=$2 AND id=$1`, bucketID, tenantID).Scan(&reservedUnits, &settledUnits, &reservations, &ledgerEntries, &events, &outbox); err != nil {
		t.Fatal(err)
	}
	if reservedUnits != 0 || settledUnits != 0 || reservations != 1 || ledgerEntries != 1 || events != 2 || outbox != 2 {
		t.Fatalf("reserved=%d settled=%d reservations=%d ledger=%d events=%d outbox=%d", reservedUnits, settledUnits, reservations, ledgerEntries, events, outbox)
	}
}

type internalUsageEpochStub struct{ epoch string }

func (stub internalUsageEpochStub) CurrentStoreEpoch(context.Context) (string, error) {
	return stub.epoch, nil
}
