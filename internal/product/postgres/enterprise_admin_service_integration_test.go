//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	productapi "github.com/langshift/lites/internal/product/api"
)

func TestEnterpriseAdminProgramCohortRolePackAndTaskPackProtocol(t *testing.T) {
	ctx := context.Background()
	admin := artifactPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := artifactPool(t, ctx, "LITES_TEST_PRODUCT_DATABASE_URL")
	defer pool.Close()

	now := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	tenantID := "e1000000-0000-4000-8000-000000000001"
	otherTenantID := "e1000000-0000-4000-8000-000000000002"
	personalTenantID := "e1000000-0000-4000-8000-000000000003"
	ownerID := "e1000000-0000-4000-8000-000000000010"
	memberID := "e1000000-0000-4000-8000-000000000011"
	learnerID := "e1000000-0000-4000-8000-000000000012"
	inactiveID := "e1000000-0000-4000-8000-000000000013"
	otherID := "e1000000-0000-4000-8000-000000000014"
	personalOwnerID := "e1000000-0000-4000-8000-000000000015"
	roleProfileID := "e1000000-0000-4000-8000-000000000020"
	taskTemplateID := "e1000000-0000-4000-8000-000000000021"
	epoch := "e1000000-0000-4000-8000-000000000022"

	setup := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES
			($1,'enterprise-owner@example.invalid','en','active'),
			($2,'enterprise-member@example.invalid','en','active'),
			($3,'enterprise-learner@example.invalid','en','active'),
			($4,'enterprise-inactive@example.invalid','en','active'),
			($5,'enterprise-other@example.invalid','en','active'),
			($6,'personal-owner@example.invalid','en','active')`, []any{ownerID, memberID, learnerID, inactiveID, otherID, personalOwnerID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region) VALUES
			($1,'enterprise','Enterprise Admin Test','active','US'),
			($2,'enterprise','Other Enterprise','active','US'),
			($3,'personal','Personal Tenant','active','US')`, []any{tenantID, otherTenantID, personalTenantID}},
		{`INSERT INTO identity.memberships(id,tenant_id,user_id,role,status,joined_at) VALUES
			('e1000000-0000-4000-8000-000000000030',$1,$2,'owner','active',$8),
			('e1000000-0000-4000-8000-000000000031',$1,$3,'member','active',$8),
			('e1000000-0000-4000-8000-000000000032',$1,$4,'member','active',$8),
			('e1000000-0000-4000-8000-000000000033',$1,$5,'member','suspended',$8),
			('e1000000-0000-4000-8000-000000000034',$6,$7,'owner','active',$8),
			('e1000000-0000-4000-8000-000000000035',$9,$10,'owner','active',$8)`, []any{tenantID, ownerID, memberID, learnerID, inactiveID, otherTenantID, otherID, now, personalTenantID, personalOwnerID}},
		{`INSERT INTO product.role_profiles(id,tenant_id,slug,revision,status,spec,locale,source_manifest) VALUES($1,$2,'enterprise-role',1,'active','{}','en','{}')`, []any{roleProfileID, tenantID}},
		{`INSERT INTO product.task_templates(id,tenant_id,slug,revision,status,spec,practice_kind,estimated_minutes,instructions_ref,validation_spec) VALUES($1,$2,'enterprise-task',1,'active','{}','writing',30,'encrypted://enterprise-task','{}')`, []any{taskTemplateID, tenantID}},
	}
	for _, statement := range setup {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	payloads := &missionPayloadStore{values: map[string][]byte{}}
	service := EnterpriseAdminService{
		Pool: pool, Appender: eventAppender(now), Payloads: payloads,
		IDKey: bytes.Repeat([]byte{0xfa}, 32), IdempotencyKeyPepper: bytes.Repeat([]byte{0xfb}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xfc}, 32),
		StoreEpoch: epoch, IdempotencyTTL: 24 * time.Hour, Now: func() time.Time { return now },
	}
	metadata := func(userID, key, clientID string) productapi.CommandMetadata {
		return productapi.CommandMetadata{RequestID: "e1000000-0000-4000-8000-000000000040", ClientRequestID: clientID, IdempotencyKey: key, TenantID: tenantID, UserID: userID, SessionID: "e1000000-0000-4000-8000-000000000041"}
	}

	denied := productapi.CreateProgramCommand{CommandMetadata: metadata(memberID, "enterprise-admin-denied-key-0001", "enterprise-denied-client-0001"), Name: "Denied", Settings: []byte(`{}`)}
	if _, err := service.CreateProgram(ctx, denied); !errors.Is(err, productapi.ErrPermissionDenied) {
		t.Fatalf("member create error=%v", err)
	}
	personalDenied := denied
	personalDenied.CommandMetadata = productapi.CommandMetadata{RequestID: denied.RequestID, ClientRequestID: "enterprise-denied-client-0002", IdempotencyKey: "enterprise-admin-denied-key-0002", TenantID: personalTenantID, UserID: personalOwnerID, SessionID: denied.SessionID}
	if _, err := service.CreateProgram(ctx, personalDenied); !errors.Is(err, productapi.ErrPermissionDenied) {
		t.Fatalf("personal tenant create error=%v", err)
	}

	create := productapi.CreateProgramCommand{CommandMetadata: metadata(ownerID, "enterprise-program-create-key-0001", "enterprise-program-client-0001"), Name: "Engineering Mobility", Settings: []byte(`{"locale":"en","weekly_minutes":180}`)}
	program, err := service.CreateProgram(ctx, create)
	if err != nil || program.Version != 1 || program.Status != "active" || program.Replayed {
		t.Fatalf("program=%#v err=%v", program, err)
	}
	replayedProgram, err := service.CreateProgram(ctx, create)
	if err != nil || !replayedProgram.Replayed || replayedProgram.ID != program.ID {
		t.Fatalf("replayed program=%#v err=%v", replayedProgram, err)
	}
	substitution := create
	substitution.Name = "Substituted Program"
	if _, err = service.CreateProgram(ctx, substitution); !errors.Is(err, productapi.ErrIdempotencyConflict) {
		t.Fatalf("idempotency substitution error=%v", err)
	}

	update := productapi.UpdateProgramCommand{CommandMetadata: metadata(ownerID, "enterprise-program-update-key-0001", "enterprise-program-client-0002"), ProgramID: program.ID, Name: "Engineering Mobility 2026", Status: "active", Settings: []byte(`{"locale":"en","weekly_minutes":210}`), ExpectedVersion: 1}
	updated, err := service.UpdateProgram(ctx, update)
	if err != nil || updated.Version != 2 || updated.Status != "active" {
		t.Fatalf("updated=%#v err=%v", updated, err)
	}
	stale := update
	stale.CommandMetadata = metadata(ownerID, "enterprise-program-update-key-0002", "enterprise-program-client-0003")
	if _, err = service.UpdateProgram(ctx, stale); !errors.Is(err, productapi.ErrStateConflict) {
		t.Fatalf("stale update error=%v", err)
	}

	startsAt, endsAt := now.Add(72*time.Hour), now.Add(31*24*time.Hour)
	cohort, err := service.CreateCohort(ctx, productapi.CreateCohortCommand{CommandMetadata: metadata(ownerID, "enterprise-cohort-create-key-0001", "enterprise-cohort-client-0001"), ProgramID: program.ID, Name: "July 2026", StartsAt: &startsAt, EndsAt: &endsAt})
	if err != nil || cohort.Version != 1 || cohort.Status != "scheduled" {
		t.Fatalf("cohort=%#v err=%v", cohort, err)
	}

	enroll := productapi.EnrollCohortCommand{CommandMetadata: metadata(ownerID, "enterprise-cohort-enroll-key-0001", "enterprise-enroll-client-0001"), CohortID: cohort.ID, UserIDs: []string{learnerID}, ExpectedVersion: 1}
	enrolled, err := service.EnrollCohort(ctx, enroll)
	if err != nil || enrolled.Version != 2 || enrolled.Replayed {
		t.Fatalf("enrolled=%#v err=%v", enrolled, err)
	}
	replayedEnrollment, err := service.EnrollCohort(ctx, enroll)
	if err != nil || !replayedEnrollment.Replayed || replayedEnrollment.Version != 2 {
		t.Fatalf("replayed enrollment=%#v err=%v", replayedEnrollment, err)
	}
	for index, target := range []string{inactiveID, otherID} {
		cross := productapi.EnrollCohortCommand{CommandMetadata: metadata(ownerID, "enterprise-cohort-denied-key-000"+string(rune('2'+index)), "enterprise-enroll-denied-000"+string(rune('1'+index))), CohortID: cohort.ID, UserIDs: []string{target}, ExpectedVersion: 2}
		if _, err = service.EnrollCohort(ctx, cross); !errors.Is(err, productapi.ErrPermissionDenied) {
			t.Fatalf("target=%s enrollment error=%v", target, err)
		}
	}

	left, err := service.UnenrollCohort(ctx, productapi.UnenrollCohortCommand{CommandMetadata: metadata(ownerID, "enterprise-cohort-unenroll-key-0001", "enterprise-unenroll-client-0001"), CohortID: cohort.ID, TargetUserID: learnerID, Reason: "Transferred team", ExpectedVersion: 2})
	if err != nil || left.Version != 3 {
		t.Fatalf("left=%#v err=%v", left, err)
	}
	if _, err = service.EnrollCohort(ctx, productapi.EnrollCohortCommand{CommandMetadata: metadata(ownerID, "enterprise-cohort-reenroll-key-0001", "enterprise-reenroll-client-0001"), CohortID: cohort.ID, UserIDs: []string{learnerID}, ExpectedVersion: 3}); !errors.Is(err, productapi.ErrStateConflict) {
		t.Fatalf("re-enroll error=%v", err)
	}

	rolePackCommand := productapi.PublishRolePackCommand{CommandMetadata: metadata(ownerID, "enterprise-role-pack-key-0001", "enterprise-role-pack-client-0001"), ProgramID: program.ID, Revision: 1, RoleProfileIDs: []string{roleProfileID}, TaskTemplateIDs: []string{taskTemplateID}}
	rolePack, err := service.PublishRolePack(ctx, rolePackCommand)
	if err != nil || rolePack.Version != 1 || rolePack.Status != "published" {
		t.Fatalf("role pack=%#v err=%v", rolePack, err)
	}
	duplicateRolePack := rolePackCommand
	duplicateRolePack.CommandMetadata = metadata(ownerID, "enterprise-role-pack-key-0002", "enterprise-role-pack-client-0002")
	if _, err = service.PublishRolePack(ctx, duplicateRolePack); !errors.Is(err, productapi.ErrStateConflict) {
		t.Fatalf("duplicate role pack error=%v", err)
	}
	taskPackCommand := productapi.PublishTaskPackCommand{CommandMetadata: metadata(ownerID, "enterprise-task-pack-key-0001", "enterprise-task-pack-client-0001"), ProgramID: program.ID, Revision: 1, Name: "Crash recovery lab", TaskTemplateIDs: []string{taskTemplateID}, Assignment: []byte(`{"required":true,"due_days":14}`)}
	taskPack, err := service.PublishTaskPack(ctx, taskPackCommand)
	if err != nil || taskPack.Version != 1 || taskPack.Status != "published" {
		t.Fatalf("task pack=%#v err=%v", taskPack, err)
	}
	taskPackReplay, err := service.PublishTaskPack(ctx, taskPackCommand)
	if err != nil || !taskPackReplay.Replayed || taskPackReplay.ID != taskPack.ID {
		t.Fatalf("task pack replay=%#v err=%v", taskPackReplay, err)
	}
	duplicateTaskPack := taskPackCommand
	duplicateTaskPack.CommandMetadata = metadata(ownerID, "enterprise-task-pack-key-0002", "enterprise-task-pack-client-0002")
	if _, err = service.PublishTaskPack(ctx, duplicateTaskPack); !errors.Is(err, productapi.ErrStateConflict) {
		t.Fatalf("duplicate task pack error=%v", err)
	}

	var programVersion, cohortVersion, activeEnrollments, leftEnrollments, rolePacks, taskPacks, events int
	err = admin.QueryRow(ctx, `SELECT
		(SELECT version FROM product.programs WHERE tenant_id=$1 AND id=$2),
		(SELECT version FROM product.cohorts WHERE tenant_id=$1 AND id=$3),
		(SELECT count(*) FROM product.enrollments WHERE tenant_id=$1 AND cohort_id=$3 AND status='active'),
		(SELECT count(*) FROM product.enrollments WHERE tenant_id=$1 AND cohort_id=$3 AND status='left' AND left_at=$5),
		(SELECT count(*) FROM product.role_packs WHERE tenant_id=$1 AND id=$4 AND status='published'),
		(SELECT count(*) FROM product.task_packs WHERE tenant_id=$1 AND id=$6 AND status='published' AND assignment='{"required":true,"due_days":14}'::jsonb),
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_id IN ($2,$3,$4,$6) AND event_type IN ('ProgramCreated','ProgramUpdated','CohortCreated','EnrollmentChanged','RolePackPublished','TaskPackPublished'))`, tenantID, program.ID, cohort.ID, rolePack.ID, now, taskPack.ID).Scan(&programVersion, &cohortVersion, &activeEnrollments, &leftEnrollments, &rolePacks, &taskPacks, &events)
	if err != nil || programVersion != 2 || cohortVersion != 3 || activeEnrollments != 0 || leftEnrollments != 1 || rolePacks != 1 || taskPacks != 1 || events != 7 {
		t.Fatalf("program=%d cohort=%d active=%d left=%d role_packs=%d task_packs=%d events=%d err=%v", programVersion, cohortVersion, activeEnrollments, leftEnrollments, rolePacks, taskPacks, events, err)
	}
	if _, err = admin.Exec(ctx, `UPDATE product.role_packs SET status='draft' WHERE id=$1`, rolePack.ID); err == nil {
		t.Fatal("role pack append-only trigger accepted update")
	}
	if _, err = admin.Exec(ctx, `UPDATE product.task_packs SET status='draft' WHERE id=$1`, taskPack.ID); err == nil {
		t.Fatal("task pack append-only trigger accepted update")
	}
	if _, err = admin.Exec(ctx, `DELETE FROM product.enrollments WHERE tenant_id=$1 AND cohort_id=$2 AND user_id=$3`, tenantID, cohort.ID, learnerID); err == nil {
		t.Fatal("enrollment lifecycle accepted delete")
	}
}
