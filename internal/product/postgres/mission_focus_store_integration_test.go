//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/product/mission"
)

func TestMissionFocusMutationsSerializeAndPreserveInvariant(t *testing.T) {
	ctx := context.Background()
	admin := artifactPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := artifactPool(t, ctx, "LITES_TEST_PRODUCT_DATABASE_URL")
	defer pool.Close()
	now := time.Date(2026, time.July, 16, 10, 0, 0, 0, time.UTC)
	userID := "b4000000-0000-4000-8000-000000000001"
	tenantID := "b4000000-0000-4000-8000-000000000002"
	roleID := "b4000000-0000-4000-8000-000000000003"
	missionA := "b4000000-0000-4000-8000-000000000004"
	missionB := "b4000000-0000-4000-8000-000000000005"
	epoch := "b4000000-0000-4000-8000-000000000006"
	setup := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'mission-focus-owner@example.invalid','en','active')`, []any{userID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Mission Focus Tenant','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO product.role_profiles(id,tenant_id,slug,revision,status,spec,locale,source_manifest) VALUES($1,$2,'mission-focus-role',1,'active','{}','en','{}')`, []any{roleID, tenantID}},
		{`INSERT INTO product.missions(id,tenant_id,user_id,status,target_role_profile_id,route_version,claim_set_hash) VALUES($1,$2,$3,'draft',$4,0,'claims-b4-a')`, []any{missionA, tenantID, userID, roleID}},
		{`INSERT INTO product.missions(id,tenant_id,user_id,status,target_role_profile_id,route_version,claim_set_hash) VALUES($1,$2,$3,'draft',$4,0,'claims-b4-b')`, []any{missionB, tenantID, userID, roleID}},
	}
	for _, statement := range setup {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	clock := now
	store := MissionFocusStore{Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return clock }}, IDKey: bytes.Repeat([]byte{0xb4}, 32), StoreEpoch: epoch, Epochs: artifactEpochStub{epoch}, Now: func() time.Time { return clock }}
	actor := json.RawMessage(`{"kind":"user","user_id":"b4000000-0000-4000-8000-000000000001"}`)
	activateA := ChangeMissionStatusCommand{MutationID: "activate-a", TenantID: tenantID, UserID: userID, MissionID: missionA, NextStatus: mission.Active, ExpectedMissionVersion: 1, ExpectedFocusVersion: 0, CorrelationID: missionA, Actor: actor, StatusChangedEvent: PayloadPointer{Ref: "encrypted://mission-a-active", Hash: "mission-a-active"}, FocusChangedEvent: PayloadPointer{Ref: "encrypted://focus-a", Hash: "focus-a"}}
	result, err := store.ChangeStatus(ctx, activateA)
	if err != nil || result.MissionVersion != 2 || result.FocusedMissionID != missionA || result.FocusVersion != 1 {
		t.Fatalf("activate A result=%#v err=%v", result, err)
	}
	clock = clock.Add(time.Second)
	activateB := ChangeMissionStatusCommand{MutationID: "activate-b", TenantID: tenantID, UserID: userID, MissionID: missionB, NextStatus: mission.Active, ExpectedMissionVersion: 1, ExpectedFocusVersion: 1, CorrelationID: missionB, Actor: actor, StatusChangedEvent: PayloadPointer{Ref: "encrypted://mission-b-active", Hash: "mission-b-active"}}
	result, err = store.ChangeStatus(ctx, activateB)
	if err != nil || result.MissionVersion != 2 || result.FocusedMissionID != missionA || result.FocusVersion != 1 {
		t.Fatalf("activate B result=%#v err=%v", result, err)
	}

	beforeEvents := 0
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND user_id=$2`, tenantID, userID).Scan(&beforeEvents); err != nil || beforeEvents != 3 {
		t.Fatalf("events before rejection=%d err=%v", beforeEvents, err)
	}
	clock = clock.Add(time.Second)
	withoutReplacement := ChangeMissionStatusCommand{MutationID: "pause-a-invalid", TenantID: tenantID, UserID: userID, MissionID: missionA, NextStatus: mission.Paused, ExpectedMissionVersion: 2, ExpectedFocusVersion: 1, CorrelationID: missionA, Actor: actor, StatusChangedEvent: PayloadPointer{Ref: "encrypted://mission-a-paused-invalid", Hash: "mission-a-paused-invalid"}}
	if _, err = store.ChangeStatus(ctx, withoutReplacement); !errors.Is(err, ErrFocusReplacement) {
		t.Fatalf("missing replacement error=%v", err)
	}

	commands := []func() error{
		func() error {
			_, callErr := store.SetFocus(ctx, SetMissionFocusCommand{MutationID: "focus-b", TenantID: tenantID, UserID: userID, MissionID: missionB, ExpectedFocusVersion: 1, CorrelationID: missionB, Actor: actor, FocusChangedEvent: PayloadPointer{Ref: "encrypted://focus-b", Hash: "focus-b"}})
			return callErr
		},
		func() error {
			_, callErr := store.ChangeStatus(ctx, ChangeMissionStatusCommand{MutationID: "pause-a", TenantID: tenantID, UserID: userID, MissionID: missionA, NextStatus: mission.Paused, ExpectedMissionVersion: 2, ExpectedFocusVersion: 1, ReplacementMissionID: missionB, CorrelationID: missionA, Actor: actor, StatusChangedEvent: PayloadPointer{Ref: "encrypted://mission-a-paused", Hash: "mission-a-paused"}, FocusChangedEvent: PayloadPointer{Ref: "encrypted://focus-b-from-pause", Hash: "focus-b-from-pause"}})
			return callErr
		},
	}
	results := make(chan error, len(commands))
	var wait sync.WaitGroup
	for _, command := range commands {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results <- command()
		}()
	}
	wait.Wait()
	close(results)
	winners, conflicts := 0, 0
	for callErr := range results {
		switch {
		case callErr == nil:
			winners++
		case errors.Is(callErr, ErrMissionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent error: %v", callErr)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("winners=%d conflicts=%d", winners, conflicts)
	}

	var focusedID, focusedStatus string
	var focusVersion uint64
	var activeMissions, events int
	err = admin.QueryRow(ctx, `SELECT f.mission_id::text,f.focus_version,m.status,(SELECT count(*) FROM product.missions x WHERE x.tenant_id=f.tenant_id AND x.user_id=f.user_id AND x.status='active'),(SELECT count(*) FROM agent.events e WHERE e.tenant_id=f.tenant_id AND e.user_id=f.user_id) FROM product.mission_focuses f JOIN product.missions m ON m.tenant_id=f.tenant_id AND m.id=f.mission_id WHERE f.tenant_id=$1 AND f.user_id=$2`, tenantID, userID).Scan(&focusedID, &focusVersion, &focusedStatus, &activeMissions, &events)
	if err != nil || focusedID != missionB || focusVersion != 2 || focusedStatus != "active" || activeMissions < 1 || events < 4 || events > 5 {
		t.Fatalf("focus=%s/v%d status=%s active=%d events=%d err=%v", focusedID, focusVersion, focusedStatus, activeMissions, events, err)
	}
}
