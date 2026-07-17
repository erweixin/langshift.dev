//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	productapi "github.com/langshift/lites/internal/product/api"
)

func TestSupportCasesAreTenantScopedIdempotentEncryptedReferencesWithFrozenSLA(t *testing.T) {
	ctx := context.Background()
	admin := artifactPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := artifactPool(t, ctx, "LITES_TEST_PRODUCT_DATABASE_URL")
	defer pool.Close()

	now := time.Date(2026, time.July, 17, 13, 0, 0, 0, time.UTC)
	tenantID := "e2000000-0000-4000-8000-000000000001"
	otherTenantID := "e2000000-0000-4000-8000-000000000002"
	memberID := "e2000000-0000-4000-8000-000000000010"
	ownerID := "e2000000-0000-4000-8000-000000000011"
	otherID := "e2000000-0000-4000-8000-000000000012"
	memberMembership := "e2000000-0000-4000-8000-000000000020"
	ownerMembership := "e2000000-0000-4000-8000-000000000021"
	otherMembership := "e2000000-0000-4000-8000-000000000022"
	epoch := "e2000000-0000-4000-8000-000000000030"
	setup := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'support-member@example.invalid','en','active'),($2,'support-owner@example.invalid','en','active'),($3,'support-other@example.invalid','en','active')`, []any{memberID, ownerID, otherID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region) VALUES($1,'enterprise','Support Tenant','active','US'),($2,'enterprise','Other Support Tenant','active','US')`, []any{tenantID, otherTenantID}},
		{`INSERT INTO identity.memberships(id,tenant_id,user_id,role,status,joined_at) VALUES($1,$2,$3,'member','active',$9),($4,$2,$5,'owner','active',$9),($6,$7,$8,'owner','active',$9)`, []any{memberMembership, tenantID, memberID, ownerMembership, ownerID, otherMembership, otherTenantID, otherID, now}},
	}
	for _, statement := range setup {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	payloads := &missionPayloadStore{values: map[string][]byte{}}
	service := SupportService{
		Pool: pool, Appender: eventAppender(now), Payloads: payloads,
		IDKey: bytes.Repeat([]byte{0xe2}, 32), IdempotencyKeyPepper: bytes.Repeat([]byte{0xe3}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xe4}, 32), CursorKey: bytes.Repeat([]byte{0xe5}, 32),
		StoreEpoch: epoch, IdempotencyTTL: 24 * time.Hour, Now: func() time.Time { return now },
	}
	metadata := func(userID, key, clientID string) productapi.CommandMetadata {
		return productapi.CommandMetadata{RequestID: "e2000000-0000-4000-8000-000000000040", ClientRequestID: clientID, IdempotencyKey: key, TenantID: tenantID, UserID: userID, SessionID: "e2000000-0000-4000-8000-000000000041"}
	}
	create := productapi.CreateSupportCaseCommand{CommandMetadata: metadata(memberID, "support-create-key-0001", "support-client-0001"), MembershipID: memberMembership, Category: "availability", Priority: "urgent", Subject: "Production scheduling is unavailable", Body: "Every run stops before a scheduler claim is recorded."}
	created, err := service.Create(ctx, create)
	if err != nil || created.Version != 1 || created.Status != "open" || created.SupportTier != "community" || created.ResponseDueAt == nil || !created.ResponseDueAt.Equal(now.Add(72*time.Hour)) || created.ResolutionDueAt != nil || len(created.Messages) != 1 {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	replayed, err := service.Create(ctx, create)
	if err != nil || !replayed.Replayed || replayed.ID != created.ID || replayed.Messages[0].Body != create.Body {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	substitution := create
	substitution.Body = "Substituted body"
	if _, err = service.Create(ctx, substitution); !errors.Is(err, productapi.ErrIdempotencyConflict) {
		t.Fatalf("substitution err=%v", err)
	}

	ownerCreate := productapi.CreateSupportCaseCommand{CommandMetadata: metadata(ownerID, "support-create-key-0002", "support-client-0002"), MembershipID: ownerMembership, Category: "contract", Priority: "normal", Subject: "Contract scope question", Body: "Please confirm the contracted deployment region."}
	ownerCase, err := service.Create(ctx, ownerCreate)
	if err != nil {
		t.Fatal(err)
	}
	memberQuery := productapi.SupportQuery{TenantID: tenantID, UserID: memberID, MembershipID: memberMembership, Limit: 50}
	memberPage, err := service.List(ctx, memberQuery)
	if err != nil || len(memberPage.Items) != 1 || memberPage.Items[0].ID != created.ID {
		t.Fatalf("member page=%#v err=%v", memberPage, err)
	}
	if _, err = service.Get(ctx, memberQuery, ownerCase.ID); !errors.Is(err, productapi.ErrResourceNotFound) {
		t.Fatalf("cross-requester get err=%v", err)
	}
	ownerQuery := productapi.SupportQuery{TenantID: tenantID, UserID: ownerID, MembershipID: ownerMembership, TenantWide: true, Limit: 50}
	ownerPage, err := service.List(ctx, ownerQuery)
	if err != nil || len(ownerPage.Items) != 2 {
		t.Fatalf("owner page=%#v err=%v", ownerPage, err)
	}
	unauthorizedWide := memberQuery
	unauthorizedWide.TenantWide = true
	if _, err = service.List(ctx, unauthorizedWide); !errors.Is(err, productapi.ErrPermissionDenied) {
		t.Fatalf("member tenant-wide err=%v", err)
	}

	replyCommand := productapi.ReplySupportCaseCommand{CommandMetadata: metadata(memberID, "support-reply-key-0001", "support-client-0003"), MembershipID: memberMembership, CaseID: created.ID, Body: "The failure also reproduces after a fresh session.", ExpectedCaseVersion: 1}
	replied, err := service.Reply(ctx, replyCommand)
	if err != nil || replied.Version != 2 || replied.Status != "waiting_on_support" || len(replied.Messages) != 2 || replied.Messages[1].Body != replyCommand.Body {
		t.Fatalf("replied=%#v err=%v", replied, err)
	}
	replayedReply, err := service.Reply(ctx, replyCommand)
	if err != nil || !replayedReply.Replayed || replayedReply.Version != 2 {
		t.Fatalf("replayed reply=%#v err=%v", replayedReply, err)
	}
	stale := replyCommand
	stale.IdempotencyKey = "support-reply-key-0002"
	stale.ClientRequestID = "support-client-0004"
	if _, err = service.Reply(ctx, stale); !errors.Is(err, productapi.ErrStateConflict) {
		t.Fatalf("stale reply err=%v", err)
	}

	var subjectRef, bodyRef string
	var supportCases, messages, events int
	err = admin.QueryRow(ctx, `SELECT
		(SELECT subject_ref FROM product.support_cases WHERE tenant_id=$1 AND id=$2),
		(SELECT body_ref FROM product.support_case_messages WHERE tenant_id=$1 AND case_id=$2 ORDER BY created_at,id LIMIT 1),
		(SELECT count(*) FROM product.support_cases WHERE tenant_id=$1),
		(SELECT count(*) FROM product.support_case_messages WHERE tenant_id=$1 AND case_id=$2),
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_id IN ($2,$3) AND event_type IN ('SupportCaseCreated','SupportCaseReplied'))`, tenantID, created.ID, ownerCase.ID).Scan(&subjectRef, &bodyRef, &supportCases, &messages, &events)
	if err != nil || supportCases != 2 || messages != 2 || events != 3 || strings.Contains(subjectRef, create.Subject) || strings.Contains(bodyRef, create.Body) {
		t.Fatalf("subject_ref=%q body_ref=%q cases=%d messages=%d events=%d err=%v", subjectRef, bodyRef, supportCases, messages, events, err)
	}
	if _, err = admin.Exec(ctx, `DELETE FROM product.support_cases WHERE tenant_id=$1 AND id=$2`, tenantID, created.ID); err == nil {
		t.Fatal("support lifecycle accepted deletion")
	}
}
