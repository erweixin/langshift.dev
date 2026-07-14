//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/identity/anonymousclaim"
)

type claimDestination struct {
	mu      sync.Mutex
	effects map[string]string
}

func (destination *claimDestination) CommitDestination(_ context.Context, saga anonymousclaim.Saga) (string, error) {
	destination.mu.Lock()
	defer destination.mu.Unlock()
	if eventID := destination.effects[saga.ClaimKey]; eventID != "" {
		return eventID, nil
	}
	eventID := "76000000-0000-4000-8000-000000000001"
	destination.effects[saga.ClaimKey] = eventID
	return eventID, nil
}

type claimEraser struct {
	mu      sync.Mutex
	effects map[string]anonymousclaim.DeletionReceipt
}

func (eraser *claimEraser) Erase(_ context.Context, saga anonymousclaim.Saga, surface string) (anonymousclaim.DeletionReceipt, error) {
	eraser.mu.Lock()
	defer eraser.mu.Unlock()
	key := saga.ClaimKey + ":" + surface
	if receipt := eraser.effects[key]; receipt.ID != "" {
		return receipt, nil
	}
	ids := map[string]string{"body_payload": "77000000-0000-4000-8000-000000000001", "preview_projection": "77000000-0000-4000-8000-000000000002", "principal_mapping": "77000000-0000-4000-8000-000000000003"}
	receipt := anonymousclaim.DeletionReceipt{ID: ids[surface], Surface: surface, Hash: "sha256:" + fmt.Sprintf("%064d", len(eraser.effects)+1), ErasedAt: time.Unix(1_800_003_000, 0).UTC(), Details: json.RawMessage(`{"verified":true}`)}
	eraser.effects[key] = receipt
	return receipt, nil
}

func TestAnonymousClaimStoreConvergesWithRLSAndProtectsReservationFromExpiry(t *testing.T) {
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, requiredEnv(t, "LITES_TEST_ADMIN_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	pool, err := pgxpool.New(ctx, requiredEnv(t, "LITES_TEST_IDENTITY_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	now := time.Unix(1_800_002_900, 0).UTC()
	const systemTenant = "70000000-0000-4000-8000-000000000001"
	const otherTenant = "70000000-0000-4000-8000-000000000002"
	const subjectID = "71000000-0000-4000-8000-000000000001"
	const ephemeralUser = "72000000-0000-4000-8000-000000000001"
	const sessionID = "73000000-0000-4000-8000-000000000001"
	const claimID = "74000000-0000-4000-8000-000000000001"
	const missionID = "75000000-0000-4000-8000-000000000001"
	const expiredSubjectID = "71000000-0000-4000-8000-000000000002"
	const expiredUserID = "72000000-0000-4000-8000-000000000002"
	const expiredSessionID = "73000000-0000-4000-8000-000000000002"
	const expiredClaimID = "74000000-0000-4000-8000-000000000002"
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.tenants(id,kind,name,status,region) VALUES($1,'anonymous_system','Anonymous','active','US'),($2,'anonymous_system','Other','active','US')`, []any{systemTenant, otherTenant}},
		{`INSERT INTO identity.anonymous_subjects(id,anonymous_subject_hash,ephemeral_user_id,system_tenant_id,expires_at) VALUES($1,decode(repeat('ab',32),'hex'),$2,$3,$4)`, []any{subjectID, ephemeralUser, systemTenant, now.Add(time.Hour)}},
		{`INSERT INTO identity.onboarding_sessions(id,tenant_id,user_id,anonymous_subject_id,status,locale,current_role_input,target_role_input,confirmed_claim_ids,expires_at) VALUES($1,$2,$3,$4,'route_ready','en','{}','{}','[]',$5)`, []any{sessionID, systemTenant, ephemeralUser, subjectID, now.Add(time.Hour)}},
		{`INSERT INTO identity.onboarding_claims(id,tenant_id,user_id,anonymous_subject_id,onboarding_session_id,claim_key,status,source_route_revision_id,expires_at) VALUES($1,$2,$3,$4,$5,'claim-key-1','available',$6,$7)`, []any{claimID, systemTenant, ephemeralUser, subjectID, sessionID, "78000000-0000-4000-8000-000000000001", now.Add(time.Hour)}},
		{`INSERT INTO identity.anonymous_subjects(id,anonymous_subject_hash,ephemeral_user_id,system_tenant_id,expires_at) VALUES($1,decode(repeat('cd',32),'hex'),$2,$3,$4)`, []any{expiredSubjectID, expiredUserID, systemTenant, now.Add(-time.Second)}},
		{`INSERT INTO identity.onboarding_sessions(id,tenant_id,user_id,anonymous_subject_id,status,locale,current_role_input,target_role_input,confirmed_claim_ids,expires_at) VALUES($1,$2,$3,$4,'route_ready','en','{}','{}','[]',$5)`, []any{expiredSessionID, systemTenant, expiredUserID, expiredSubjectID, now.Add(-time.Second)}},
		{`INSERT INTO identity.onboarding_claims(id,tenant_id,user_id,anonymous_subject_id,onboarding_session_id,claim_key,status,source_route_revision_id,expires_at) VALUES($1,$2,$3,$4,$5,'claim-key-expired','available',$6,$7)`, []any{expiredClaimID, systemTenant, expiredUserID, expiredSubjectID, expiredSessionID, "78000000-0000-4000-8000-000000000002", now.Add(-time.Second)}},
	} {
		if _, err = admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	store := AnonymousClaimStore{Pool: pool, SystemTenantID: systemTenant, Now: func() time.Time { return now }}
	reservation := anonymousclaim.Reservation{ClaimID: claimID, ClaimKey: "claim-key-1", TargetTenantID: "79000000-0000-4000-8000-000000000001", TargetUserID: "79000000-0000-4000-8000-000000000002", MissionID: missionID}
	claimService := anonymousclaim.Service{Store: store, Now: func() time.Time { return now }}
	if _, err = claimService.Reserve(ctx, anonymousclaim.Reservation{ClaimID: expiredClaimID, ClaimKey: "claim-key-expired", TargetTenantID: reservation.TargetTenantID, TargetUserID: reservation.TargetUserID, MissionID: "75000000-0000-4000-8000-000000000002"}); !errors.Is(err, anonymousclaim.ErrInvalidTransition) {
		t.Fatalf("expired reservation error=%v", err)
	}
	if expired, expireErr := claimService.ExpireAvailable(ctx, expiredClaimID); expireErr != nil || expired.Status != anonymousclaim.Expired {
		t.Fatalf("expired cleanup=%#v error=%v", expired, expireErr)
	}
	var expiredStatus string
	var expiredReservedAt *time.Time
	if err = admin.QueryRow(ctx, `SELECT status,reserved_at FROM identity.onboarding_claims WHERE id=$1`, expiredClaimID).Scan(&expiredStatus, &expiredReservedAt); err != nil || expiredStatus != "expired" || expiredReservedAt != nil {
		t.Fatalf("expired status=%s reserved_at=%v error=%v", expiredStatus, expiredReservedAt, err)
	}
	const contenders = 64
	results := make(chan error, contenders)
	var wait sync.WaitGroup
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, reserveErr := claimService.Reserve(ctx, reservation)
			results <- reserveErr
		}()
	}
	wait.Wait()
	close(results)
	for reserveErr := range results {
		if reserveErr != nil {
			t.Fatalf("reserve: %v", reserveErr)
		}
	}
	var reservedAt *time.Time
	if err = admin.QueryRow(ctx, `SELECT reserved_at FROM identity.anonymous_subjects WHERE id=$1`, subjectID).Scan(&reservedAt); err != nil || reservedAt == nil {
		t.Fatalf("subject reservation=%v error=%v", reservedAt, err)
	}
	destination := &claimDestination{effects: map[string]string{}}
	eraser := &claimEraser{effects: map[string]anonymousclaim.DeletionReceipt{}}
	claimService.Destination = destination
	claimService.Eraser = eraser
	const reconcilers = 16
	reconcileResults := make(chan error, reconcilers)
	for range reconcilers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, reconcileErr := claimService.Reconcile(ctx, claimID)
			reconcileResults <- reconcileErr
		}()
	}
	wait.Wait()
	close(reconcileResults)
	for reconcileErr := range reconcileResults {
		if reconcileErr != nil {
			t.Fatalf("reconcile: %v", reconcileErr)
		}
	}
	final, err := store.Load(ctx, claimID)
	if err != nil || final.Status != anonymousclaim.Claimed || len(final.DeletionReceipts) != 3 {
		t.Fatalf("final=%#v error=%v", final, err)
	}
	if len(destination.effects) != 1 || len(eraser.effects) != 3 {
		t.Fatalf("destination=%d erasures=%d", len(destination.effects), len(eraser.effects))
	}
	var receiptCount int
	var deletedAt *time.Time
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM identity.anonymous_erasure_receipts WHERE claim_id=$1`, claimID).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT deleted_at FROM identity.anonymous_subjects WHERE id=$1`, subjectID).Scan(&deletedAt); err != nil || deletedAt == nil || receiptCount != 3 {
		t.Fatalf("deleted_at=%v receipts=%d error=%v", deletedAt, receiptCount, err)
	}
	if _, err = (AnonymousClaimStore{Pool: pool, SystemTenantID: otherTenant}).Load(ctx, claimID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-tenant load error=%v", err)
	}
}
