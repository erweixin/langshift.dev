//go:build integration

package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/identity/erasure"
	"github.com/langshift/lites/internal/objectstore/s3store"
	"github.com/langshift/lites/internal/payload"
)

type erasureTestPayloadStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (store *erasureTestPayloadStore) Put(_ context.Context, descriptor payload.Descriptor, body []byte) (payload.Manifest, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	digest := sha256.Sum256(body)
	ref := "s3://lites-payloads/restricted/" + descriptor.TenantID + "/" + descriptor.Class + "/" + descriptor.ObjectID
	store.objects[ref] = append([]byte(nil), body...)
	return payload.Manifest{Ref: ref, Hash: hex.EncodeToString(digest[:]), KeyID: "test", AADHash: hex.EncodeToString(digest[:])}, nil
}

func (store *erasureTestPayloadStore) Get(_ context.Context, _ payload.Descriptor, manifest payload.Manifest) ([]byte, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]byte(nil), store.objects[manifest.Ref]...), nil
}

type emptyErasurePurger struct{}

func (emptyErasurePurger) Purge(_ context.Context, ref string) (s3store.PurgeReceipt, error) {
	digest := sha256.Sum256([]byte(ref))
	return s3store.PurgeReceipt{VersionsDeleted: 1, Checksum: hex.EncodeToString(digest[:])}, nil
}
func (emptyErasurePurger) PurgeKey(context.Context, string) (string, error) {
	return "", fmt.Errorf("unexpected key purge")
}
func (emptyErasurePurger) PurgeSubject(_ context.Context, tenantID, userID, epoch string) (int, string, error) {
	digest := sha256.Sum256([]byte(tenantID + userID + epoch))
	return 0, hex.EncodeToString(digest[:]), nil
}

func TestAccountErasureHundredSubjectsAndRestoreEpoch(t *testing.T) {
	adminURL, workerURL := os.Getenv("LITES_TEST_ADMIN_DATABASE_URL"), os.Getenv("LITES_TEST_ERASURE_DATABASE_URL")
	if adminURL == "" || workerURL == "" {
		t.Skip("erasure integration databases are not configured")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	worker, err := pgxpool.New(ctx, workerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	tenantID := "84000000-0000-4000-8000-000000000001"
	if _, err = admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region) VALUES($1,'enterprise','Erasure Gate','active','US')`, tenantID); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 100; index++ {
		userID := fmt.Sprintf("84000000-0000-4000-8000-%012d", index+1000)
		requestID := fmt.Sprintf("84000000-0000-4001-8000-%012d", index+1000)
		membershipID := fmt.Sprintf("84000000-0000-4002-8000-%012d", index+1000)
		if _, err = admin.Exec(ctx, `INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,$2,'en','active')`, userID, fmt.Sprintf("erasure-%03d@example.invalid", index)); err != nil {
			t.Fatal(err)
		}
		if _, err = admin.Exec(ctx, `INSERT INTO identity.memberships(id,tenant_id,user_id,role,status,joined_at) VALUES($1,$2,$3,'member','active',$4)`, membershipID, tenantID, userID, now.Add(-48*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err = admin.Exec(ctx, `INSERT INTO identity.account_erasure_requests(id,tenant_id,user_id,status,requested_at,scheduled_for) VALUES($1,$2,$3,'requested',$4,$5)`, requestID, tenantID, userID, now.Add(-48*time.Hour), now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	payloads := &erasureTestPayloadStore{objects: map[string][]byte{}}
	store := AccountErasureStore{Pool: worker, IdentityKey: bytes.Repeat([]byte{0x84}, 32), Payloads: payloads, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, StoreEpoch: "84000000-0000-4003-8000-000000000001"}
	cache := emptyErasurePurger{}
	index := ObjectBackedIndexPurger{Pool: worker}
	erasors := map[erasure.Surface]erasure.SurfaceEraser{}
	for _, erasureSurface := range erasure.RequiredSurfaces {
		erasors[erasureSurface] = AccountErasureSurface{Pool: worker, Surface: erasureSurface, Objects: emptyErasurePurger{}, Keys: emptyErasurePurger{}, Indexes: index, Caches: cache, Now: func() time.Time { return now }}
	}
	service := erasure.Service{Store: store, Erasers: erasors, RecoveryEpoch: store.StoreEpoch, ReceiptKey: bytes.Repeat([]byte{0x85}, 32), Now: func() time.Time { return now }}
	for index := 0; index < 100; index++ {
		requestID := fmt.Sprintf("84000000-0000-4001-8000-%012d", index+1000)
		result, processErr := service.Process(ctx, tenantID, requestID)
		if processErr != nil || len(result.Receipts) != 6 || !result.Completed {
			t.Fatalf("request=%s receipts=%d err=%v", requestID, len(result.Receipts), processErr)
		}
	}
	assertErasureCounts(t, ctx, admin, tenantID, 600, 100, 100)
	restoreEpoch := "84000000-0000-4003-8000-000000000002"
	restore := erasure.Service{Store: store, Erasers: erasors, RecoveryEpoch: restoreEpoch, ReceiptKey: bytes.Repeat([]byte{0x85}, 32), Now: func() time.Time { return now.Add(time.Hour) }}
	replayed := 0
	for {
		results, reconcileErr := restore.ReconcileRestore(ctx, 25)
		if reconcileErr != nil {
			t.Fatal(reconcileErr)
		}
		replayed += len(results)
		if len(results) == 0 {
			break
		}
	}
	if replayed != 100 {
		t.Fatalf("restore replayed=%d", replayed)
	}
	assertErasureCounts(t, ctx, admin, tenantID, 1200, 100, 100)
}

func assertErasureCounts(t *testing.T, ctx context.Context, admin *pgxpool.Pool, tenantID string, receipts, tombstones, events int) {
	t.Helper()
	var actualReceipts, actualTombstones, actualEvents, readableUsers, activeMemberships, invalidRequestVersions, invalidEventVersions int
	err := admin.QueryRow(ctx, `SELECT
      (SELECT count(*) FROM identity.account_erasure_receipts WHERE tenant_id=$1),
      (SELECT count(*) FROM identity.subject_erasure_tombstones WHERE tenant_id=$1),
      (SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND event_type='SubjectErasureCompleted'),
      (SELECT count(*) FROM identity.users WHERE normalized_email LIKE 'erasure-%@example.invalid'),
	  (SELECT count(*) FROM identity.memberships WHERE tenant_id=$1 AND status<>'left'),
	  (SELECT count(*) FROM identity.account_erasure_requests WHERE tenant_id=$1 AND (status<>'completed' OR version<>2)),
	  (SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND event_type='SubjectErasureCompleted' AND aggregate_version<>2)`, tenantID).Scan(&actualReceipts, &actualTombstones, &actualEvents, &readableUsers, &activeMemberships, &invalidRequestVersions, &invalidEventVersions)
	if err != nil || actualReceipts != receipts || actualTombstones != tombstones || actualEvents != events || readableUsers != 0 || activeMemberships != 0 || invalidRequestVersions != 0 || invalidEventVersions != 0 {
		t.Fatalf("receipts=%d tombstones=%d events=%d readable=%d memberships=%d request_versions=%d event_versions=%d err=%v", actualReceipts, actualTombstones, actualEvents, readableUsers, activeMemberships, invalidRequestVersions, invalidEventVersions, err)
	}
}
