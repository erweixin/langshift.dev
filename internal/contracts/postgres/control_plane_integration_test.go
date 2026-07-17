//go:build integration

package postgres

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	contractTenantID   = "77000000-0000-0000-0000-000000000001"
	contractID         = "77000000-0000-0000-0000-000000000002"
	proposalID         = "77000000-0000-0000-0000-000000000003"
	rejectedProposalID = "77000000-0000-0000-0000-000000000004"
	storeEpochID       = "77000000-0000-0000-0000-000000000005"
	correlationID      = "77000000-0000-0000-0000-000000000006"
	proposalHash       = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	reasonHash         = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	permissionHash     = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

type contractPrincipal struct {
	userID       string
	membershipID string
	sessionID    string
	role         string
}

func TestContractControlPlaneRequiresCurrentTwoPersonApprovalAndSynchronizesSeats(t *testing.T) {
	ctx := context.Background()
	admin := contractPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	service := contractPool(t, ctx, "LITES_TEST_CONTRACT_DATABASE_URL")
	defer service.Close()
	identity := contractPool(t, ctx, "LITES_TEST_IDENTITY_DATABASE_URL")
	defer identity.Close()

	now := time.Now().UTC().Truncate(time.Millisecond)
	seededReauthentication = now
	principals := []contractPrincipal{
		{"77000000-0000-0000-0001-000000000001", "77000000-0000-0000-0002-000000000001", "77000000-0000-0000-0003-000000000001", "contract_admin"},
		{"77000000-0000-0000-0001-000000000002", "77000000-0000-0000-0002-000000000002", "77000000-0000-0000-0003-000000000002", "owner"},
		{"77000000-0000-0000-0001-000000000003", "77000000-0000-0000-0002-000000000003", "77000000-0000-0000-0003-000000000003", "contract_admin"},
		{"77000000-0000-0000-0001-000000000004", "77000000-0000-0000-0002-000000000004", "77000000-0000-0000-0003-000000000004", "contract_admin"},
		{"77000000-0000-0000-0001-000000000005", "77000000-0000-0000-0002-000000000005", "77000000-0000-0000-0003-000000000005", "member"},
	}
	seedContractPrincipals(t, ctx, admin, principals, now)

	insertProposal(t, ctx, service, proposalID, contractID, principals[0], "create", 0, proposalHash, now, true)

	unauthorized := contractDecision{
		id: "77000000-0000-0000-0010-000000000006", eventID: "77000000-0000-0000-0011-000000000006",
		principal: principals[4], decision: "approve", proposalID: proposalID, proposalHash: proposalHash, targetVersion: 0, sequence: 1,
	}
	if err := insertContractDecision(ctx, service, unauthorized, contractNow()); err == nil {
		t.Fatal("member without contract authority unexpectedly approved")
	}
	mismatchedHash := contractDecision{
		id: "77000000-0000-0000-0010-000000000007", eventID: "77000000-0000-0000-0011-000000000007",
		principal: principals[1], decision: "approve", proposalID: proposalID, proposalHash: strings.Repeat("f", 64), targetVersion: 0, sequence: 1,
	}
	if err := insertContractDecision(ctx, service, mismatchedHash, contractNow()); err == nil {
		t.Fatal("approval bound to a different proposal hash unexpectedly succeeded")
	}
	mismatchedVersion := mismatchedHash
	mismatchedVersion.id = "77000000-0000-0000-0010-000000000008"
	mismatchedVersion.eventID = "77000000-0000-0000-0011-000000000008"
	mismatchedVersion.proposalHash = proposalHash
	mismatchedVersion.targetVersion = 1
	if err := insertContractDecision(ctx, service, mismatchedVersion, contractNow()); err == nil {
		t.Fatal("approval bound to a different target version unexpectedly succeeded")
	}

	expiredSession := principals[0]
	expiredSession.sessionID = "77000000-0000-0000-0003-000000000006"
	expiredAt := contractNow().Add(-time.Second)
	expiredCreatedAt := expiredAt.Add(-30 * time.Second)
	expiredHash := strings.Repeat("9", 64)
	seedContractSession(t, ctx, admin, expiredSession, expiredCreatedAt, 9)
	insertExpiredProposal(t, ctx, service, expiredSession, expiredCreatedAt, expiredAt, expiredHash)
	expiredDecision := contractDecision{
		id: "77000000-0000-0000-0010-000000000009", eventID: "77000000-0000-0000-0011-000000000009",
		principal: principals[1], decision: "approve", proposalID: "77000000-0000-0000-0000-000000000009", proposalHash: expiredHash, targetVersion: 0, sequence: 1,
	}
	if err := insertContractDecision(ctx, service, expiredDecision, contractNow()); err == nil {
		t.Fatal("approval of an expired proposal unexpectedly succeeded")
	}

	selfDecision := contractDecision{
		id: "77000000-0000-0000-0010-000000000001", eventID: "77000000-0000-0000-0011-000000000001",
		principal: principals[0], decision: "approve", proposalID: proposalID, proposalHash: proposalHash, targetVersion: 0, sequence: 1,
	}
	if err := insertContractDecision(ctx, service, selfDecision, contractNow()); err == nil {
		t.Fatal("proposal initiator unexpectedly approved their own proposal")
	}

	first := contractDecision{
		id: "77000000-0000-0000-0010-000000000002", eventID: "77000000-0000-0000-0011-000000000002",
		principal: principals[1], decision: "approve", proposalID: proposalID, proposalHash: proposalHash, targetVersion: 0, sequence: 1,
	}
	if err := insertContractDecision(ctx, service, first, contractNow()); err != nil {
		t.Fatal(err)
	}
	duplicatePrincipal := first
	duplicatePrincipal.id = "77000000-0000-0000-0010-000000000010"
	duplicatePrincipal.eventID = "77000000-0000-0000-0011-000000000010"
	duplicatePrincipal.sequence = 2
	if err := insertContractDecision(ctx, service, duplicatePrincipal, contractNow()); err == nil {
		t.Fatal("same approval principal unexpectedly voted twice")
	}
	if _, _, _, err := applyProposal(ctx, service, proposalID, principals[1].userID, "77000000-0000-0000-0012-000000000001", contractNow()); err == nil {
		t.Fatal("proposal executed with only one approval")
	}

	second := contractDecision{
		id: "77000000-0000-0000-0010-000000000003", eventID: "77000000-0000-0000-0011-000000000003",
		principal: principals[2], decision: "approve", proposalID: proposalID, proposalHash: proposalHash, targetVersion: 0, sequence: 1,
	}
	if err := insertContractDecision(ctx, service, second, contractNow()); err != nil {
		t.Fatal(err)
	}
	third := contractDecision{
		id: "77000000-0000-0000-0010-000000000004", eventID: "77000000-0000-0000-0011-000000000004",
		principal: principals[3], decision: "approve", proposalID: proposalID, proposalHash: proposalHash, targetVersion: 0, sequence: 1,
	}
	if err := insertContractDecision(ctx, service, third, contractNow()); err == nil {
		t.Fatal("third approval unexpectedly bypassed the exact-two fence")
	}

	if _, err := admin.Exec(ctx, `UPDATE identity.memberships SET role='member',version=version+1,updated_at=$2 WHERE id=$1`, principals[2].membershipID, contractNow()); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := applyProposal(ctx, service, proposalID, principals[1].userID, "77000000-0000-0000-0012-000000000002", contractNow()); err == nil {
		t.Fatal("proposal executed after an approver lost contract authority")
	}
	if _, err := admin.Exec(ctx, `UPDATE identity.memberships SET role='contract_admin',version=version+1,updated_at=$2 WHERE id=$1`, principals[2].membershipID, contractNow()); err != nil {
		t.Fatal(err)
	}

	createdID, version, status, err := applyProposal(ctx, service, proposalID, principals[2].userID, "77000000-0000-0000-0012-000000000003", contractNow())
	if err != nil {
		t.Fatal(err)
	}
	if createdID != contractID || version != 1 || status != "active" {
		t.Fatalf("contract=(%s,%d,%s)", createdID, version, status)
	}
	if _, _, _, err = applyProposal(ctx, service, proposalID, principals[2].userID, "77000000-0000-0000-0012-000000000004", contractNow()); err == nil {
		t.Fatal("executed proposal replay unexpectedly mutated the contract")
	}

	var seats, approvals, audits int
	var approvalEventCount int
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM contracts.seat_allocations WHERE tenant_id=$1 AND contract_id=$2 AND status='active'`, contractTenantID, contractID).Scan(&seats); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM contracts.contract_approval_decisions WHERE tenant_id=$1 AND proposal_id=$2 AND decision='approve'`, contractTenantID, proposalID).Scan(&approvals); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT count(*),max(jsonb_array_length(approval_event_ids)) FROM contracts.contract_audit_events WHERE tenant_id=$1 AND contract_id=$2`, contractTenantID, contractID).Scan(&audits, &approvalEventCount); err != nil {
		t.Fatal(err)
	}
	if seats != 5 || approvals != 2 || audits != 1 || approvalEventCount != 2 {
		t.Fatalf("seats=%d approvals=%d audits=%d audit-events=%d", seats, approvals, audits, approvalEventCount)
	}

	if err = tenantExec(ctx, service, contractTenantID, `UPDATE contracts.contracts SET status='suspended' WHERE tenant_id=$1 AND id=$2`, contractTenantID, contractID); err == nil {
		t.Fatal("contract service unexpectedly bypassed the proposal-only mutation boundary")
	}

	member := principals[4]
	suspendedAt := contractNow()
	if err = tenantExec(ctx, identity, contractTenantID, `UPDATE identity.memberships SET version=version+1,status='suspended',deactivated_at=$3,updated_at=$3 WHERE tenant_id=$1 AND id=$2`, contractTenantID, member.membershipID, suspendedAt); err != nil {
		t.Fatal(err)
	}
	var seatStatus string
	var seatVersion int64
	if err = admin.QueryRow(ctx, `SELECT status,version FROM contracts.seat_allocations WHERE tenant_id=$1 AND membership_id=$2`, contractTenantID, member.membershipID).Scan(&seatStatus, &seatVersion); err != nil {
		t.Fatal(err)
	}
	if seatStatus != "released" || seatVersion != 2 {
		t.Fatalf("released seat=(%s,%d)", seatStatus, seatVersion)
	}
	reactivatedAt := suspendedAt.Add(time.Millisecond)
	if err = tenantExec(ctx, identity, contractTenantID, `UPDATE identity.memberships SET version=version+1,status='active',deactivated_at=NULL,updated_at=$3 WHERE tenant_id=$1 AND id=$2`, contractTenantID, member.membershipID, reactivatedAt); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT status,version FROM contracts.seat_allocations WHERE tenant_id=$1 AND membership_id=$2`, contractTenantID, member.membershipID).Scan(&seatStatus, &seatVersion); err != nil {
		t.Fatal(err)
	}
	if seatStatus != "active" || seatVersion != 3 {
		t.Fatalf("reactivated seat=(%s,%d)", seatStatus, seatVersion)
	}

	overflowUser := "77000000-0000-0000-0001-000000000006"
	overflowMembership := "77000000-0000-0000-0002-000000000006"
	if _, err = admin.Exec(ctx, `INSERT INTO identity.users(id,normalized_email,email_verified_at,locale,status) VALUES($1,'contract-overflow@lites.invalid',$2,'en','active')`, overflowUser, now); err != nil {
		t.Fatal(err)
	}
	if err = tenantExec(ctx, identity, contractTenantID, `INSERT INTO identity.memberships(id,tenant_id,user_id,role,status,joined_at,created_at,updated_at) VALUES($1,$2,$3,'member','active',$4,$4,$4)`, overflowMembership, contractTenantID, overflowUser, time.Now().UTC()); err == nil {
		t.Fatal("membership transaction unexpectedly exceeded the active contract seat limit")
	}
	var overflowRows int
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM identity.memberships WHERE id=$1`, overflowMembership).Scan(&overflowRows); err != nil || overflowRows != 0 {
		t.Fatalf("overflow rows=%d err=%v", overflowRows, err)
	}

	rejectNow := contractNow()
	insertProposal(t, ctx, service, rejectedProposalID, contractID, principals[0], "suspend", 1, strings.Repeat("d", 64), rejectNow, false)
	rejection := contractDecision{
		id: "77000000-0000-0000-0010-000000000005", eventID: "77000000-0000-0000-0011-000000000005",
		principal: principals[1], decision: "reject", proposalID: rejectedProposalID, proposalHash: strings.Repeat("d", 64), targetVersion: 1, sequence: 2,
	}
	if err = insertContractDecision(ctx, service, rejection, contractNow()); err != nil {
		t.Fatal(err)
	}
	var proposalStatus string
	var proposalVersion int64
	if err = admin.QueryRow(ctx, `SELECT status,version FROM contracts.contract_proposals WHERE id=$1`, rejectedProposalID).Scan(&proposalStatus, &proposalVersion); err != nil {
		t.Fatal(err)
	}
	if proposalStatus != "rejected" || proposalVersion != 2 {
		t.Fatalf("rejected proposal=(%s,%d)", proposalStatus, proposalVersion)
	}
	if _, _, _, err = applyProposal(ctx, service, rejectedProposalID, principals[1].userID, "77000000-0000-0000-0012-000000000005", contractNow()); err == nil {
		t.Fatal("rejected proposal unexpectedly executed")
	}

	staleTargetProposal := "77000000-0000-0000-0000-000000000011"
	staleTargetHash := strings.Repeat("1", 64)
	insertProposal(t, ctx, service, staleTargetProposal, contractID, principals[0], "suspend", 1, staleTargetHash, contractNow(), false)
	approveProposalPair(t, ctx, service, staleTargetProposal, staleTargetHash, 1, principals[1], 3, principals[2], 2, 1)

	winningSuspendProposal := "77000000-0000-0000-0000-000000000012"
	winningSuspendHash := strings.Repeat("2", 64)
	insertProposal(t, ctx, service, winningSuspendProposal, contractID, principals[0], "suspend", 1, winningSuspendHash, contractNow(), false)
	approveProposalPair(t, ctx, service, winningSuspendProposal, winningSuspendHash, 1, principals[1], 4, principals[2], 3, 3)
	_, version, status, err = applyProposal(ctx, service, winningSuspendProposal, principals[2].userID, "77000000-0000-0000-0012-000000000006", contractNow())
	if err != nil || version != 2 || status != "suspended" {
		t.Fatalf("suspend=(%d,%s) err=%v", version, status, err)
	}
	if _, _, _, err = applyProposal(ctx, service, staleTargetProposal, principals[2].userID, "77000000-0000-0000-0012-000000000007", contractNow()); err == nil {
		t.Fatal("approvals bound to a stale contract target version unexpectedly executed")
	}

	if err = tenantExec(ctx, identity, contractTenantID, `INSERT INTO identity.memberships(id,tenant_id,user_id,role,status,joined_at,created_at,updated_at) VALUES($1,$2,$3,'member','active',$4,$4,$4)`, overflowMembership, contractTenantID, overflowUser, contractNow()); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM contracts.seat_allocations WHERE tenant_id=$1 AND contract_id=$2 AND status='active'`, contractTenantID, contractID).Scan(&seats); err != nil {
		t.Fatal(err)
	}
	if seats != 5 {
		t.Fatalf("suspended contract unexpectedly allocated a sixth seat: %d", seats)
	}

	renewProposal := "77000000-0000-0000-0000-000000000013"
	renewHash := strings.Repeat("3", 64)
	insertProposalWithSeatLimit(t, ctx, service, renewProposal, contractID, principals[0], "renew", 2, renewHash, contractNow(), true, 6)
	approveProposalPair(t, ctx, service, renewProposal, renewHash, 2, principals[1], 5, principals[2], 4, 5)
	_, version, status, err = applyProposal(ctx, service, renewProposal, principals[2].userID, "77000000-0000-0000-0012-000000000008", contractNow())
	if err != nil || version != 3 || status != "active" {
		t.Fatalf("renew=(%d,%s) err=%v", version, status, err)
	}
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM contracts.seat_allocations WHERE tenant_id=$1 AND contract_id=$2 AND status='active'`, contractTenantID, contractID).Scan(&seats); err != nil {
		t.Fatal(err)
	}
	if seats != 6 {
		t.Fatalf("renewal did not reconcile every active membership seat: %d", seats)
	}

	stale := principals[0]
	staleProposal := "77000000-0000-0000-0000-000000000007"
	if err = insertProposalError(ctx, service, staleProposal, "77000000-0000-0000-0000-000000000008", stale, rejectNow.Add(-6*time.Minute), rejectNow); err == nil {
		t.Fatal("proposal with stale reauthentication unexpectedly succeeded")
	}
}

func seedContractPrincipals(t *testing.T, ctx context.Context, admin *pgxpool.Pool, principals []contractPrincipal, now time.Time) {
	t.Helper()
	if _, err := admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region) VALUES($1,'enterprise','Contract Control Plane','active','US')`, contractTenantID); err != nil {
		t.Fatal(err)
	}
	for index, principal := range principals {
		email := fmt.Sprintf("contract-%d@lites.invalid", index+1)
		if _, err := admin.Exec(ctx, `INSERT INTO identity.users(id,normalized_email,email_verified_at,locale,status) VALUES($1,$2,$3,'en','active')`, principal.userID, email, now); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(ctx, `INSERT INTO identity.memberships(id,tenant_id,user_id,role,status,joined_at,created_at,updated_at) VALUES($1,$2,$3,$4,'active',$5,$5,$5)`, principal.membershipID, contractTenantID, principal.userID, principal.role, now); err != nil {
			t.Fatal(err)
		}
		seedContractSession(t, ctx, admin, principal, now, byte(index+1))
	}
}

func seedContractSession(t *testing.T, ctx context.Context, admin *pgxpool.Pool, principal contractPrincipal, reauthenticatedAt time.Time, marker byte) {
	t.Helper()
	hash := make([]byte, 32)
	for position := range hash {
		hash[position] = marker
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.sessions(id,user_id,active_tenant_id,token_hash,csrf_secret_hash,ip_hash,user_agent_hash,last_seen_at,expires_at,reauthenticated_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$5,$5,$6,$7,$6,$6,$6)`, principal.sessionID, principal.userID, contractTenantID, hash, append([]byte{99}, hash[1:]...), reauthenticatedAt, contractNow().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func insertExpiredProposal(t *testing.T, ctx context.Context, service *pgxpool.Pool, initiator contractPrincipal, createdAt, expiresAt time.Time, hash string) {
	t.Helper()
	tx, err := beginTenant(ctx, service, contractTenantID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `INSERT INTO contracts.contract_proposals(id,tenant_id,created_at,updated_at,initiator_user_id,initiator_membership_id,initiator_session_id,reauthenticated_at,action,target_contract_id,target_version,status,proposal_hash,reason_ref,reason_hash,contract_number,starts_at,ends_at,seat_limit,region,license_kind,expires_at) VALUES('77000000-0000-0000-0000-000000000009',$1,$2,$2,$3,$4,$5,$2,'create','77000000-0000-0000-0000-000000000010',0,'proposed',$6,'encrypted://contracts/reasons/expired',$7,'ENT-EXPIRED',$2,$8,5,'US','enterprise_cloud',$9)`, contractTenantID, createdAt, initiator.userID, initiator.membershipID, initiator.sessionID, hash, reasonHash, createdAt.Add(24*time.Hour), expiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func insertProposal(t *testing.T, ctx context.Context, service *pgxpool.Pool, id, targetID string, initiator contractPrincipal, action string, targetVersion int64, hash string, now time.Time, withTerms bool) {
	t.Helper()
	insertProposalWithSeatLimit(t, ctx, service, id, targetID, initiator, action, targetVersion, hash, now, withTerms, 5)
}

func insertProposalWithSeatLimit(t *testing.T, ctx context.Context, service *pgxpool.Pool, id, targetID string, initiator contractPrincipal, action string, targetVersion int64, hash string, now time.Time, withTerms bool, termsSeatLimit int) {
	t.Helper()
	tx, err := beginTenant(ctx, service, contractTenantID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var contractNumber any
	var startsAt any
	var endsAt any
	var seatLimit any
	var region any
	var licenseKind any
	if withTerms {
		contractNumber, startsAt, endsAt, seatLimit, region, licenseKind = "ENT-2026-0001", now, now.Add(365*24*time.Hour), termsSeatLimit, "US", "enterprise_cloud"
	}
	_, err = tx.Exec(ctx, `INSERT INTO contracts.contract_proposals(id,tenant_id,created_at,updated_at,initiator_user_id,initiator_membership_id,initiator_session_id,reauthenticated_at,action,target_contract_id,target_version,status,proposal_hash,reason_ref,reason_hash,contract_number,starts_at,ends_at,seat_limit,region,license_kind,expires_at) VALUES($1,$2,$3,$3,$4,$5,$6,$7,$8,$9,$10,'proposed',$11,'encrypted://contracts/reasons/77',$12,$13,$14,$15,$16,$17,$18,$19)`, id, contractTenantID, now, initiator.userID, initiator.membershipID, initiator.sessionID, nowForSession(initiator), action, targetID, targetVersion, hash, reasonHash, contractNumber, startsAt, endsAt, seatLimit, region, licenseKind, now.Add(15*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func approveProposalPair(t *testing.T, ctx context.Context, service *pgxpool.Pool, proposalID, hash string, targetVersion int64, first contractPrincipal, firstSequence int64, second contractPrincipal, secondSequence int64, identifier int) {
	t.Helper()
	decisions := []contractDecision{
		{id: fmt.Sprintf("77000000-0000-0000-0020-%012d", identifier), eventID: fmt.Sprintf("77000000-0000-0000-0021-%012d", identifier), principal: first, decision: "approve", proposalID: proposalID, proposalHash: hash, targetVersion: targetVersion, sequence: firstSequence},
		{id: fmt.Sprintf("77000000-0000-0000-0020-%012d", identifier+1), eventID: fmt.Sprintf("77000000-0000-0000-0021-%012d", identifier+1), principal: second, decision: "approve", proposalID: proposalID, proposalHash: hash, targetVersion: targetVersion, sequence: secondSequence},
	}
	for _, decision := range decisions {
		if err := insertContractDecision(ctx, service, decision, contractNow()); err != nil {
			t.Fatal(err)
		}
	}
}

// The integration fixture stores every session reauthentication at its initial seed instant.
var seededReauthentication time.Time

func nowForSession(_ contractPrincipal) time.Time { return seededReauthentication }

type contractDecision struct {
	id            string
	eventID       string
	principal     contractPrincipal
	decision      string
	proposalID    string
	proposalHash  string
	targetVersion int64
	sequence      int64
}

func insertContractDecision(ctx context.Context, service *pgxpool.Pool, input contractDecision, decidedAt time.Time) error {
	tx, err := beginTenant(ctx, service, contractTenantID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `INSERT INTO agent.events(id,tenant_id,user_id,seq,event_type,event_schema_version,aggregate_kind,aggregate_id,aggregate_version,store_epoch,occurred_at,committed_at,actor,correlation_id,payload_ref,payload_hash) VALUES($1,$2,$3,$4,$5,1,'contract_approval',$6,1,$7,$8,$8,'{"kind":"user"}',$9,'encrypted://contracts/approvals/77',$10)`, input.eventID, contractTenantID, input.principal.userID, input.sequence, "ContractApproval"+strings.ToUpper(input.decision[:1])+input.decision[1:], input.id, storeEpochID, decidedAt, correlationID, input.proposalHash)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO contracts.contract_approval_decisions(id,tenant_id,proposal_id,approver_user_id,membership_id,session_id,decision,proposal_hash,target_version,permission_snapshot,reauthenticated_at,decided_at,event_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$12)`, input.id, contractTenantID, input.proposalID, input.principal.userID, input.principal.membershipID, input.principal.sessionID, input.decision, input.proposalHash, input.targetVersion, permissionHash, seededReauthentication, decidedAt, input.eventID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func applyProposal(ctx context.Context, service *pgxpool.Pool, id, actorID, auditID string, now time.Time) (string, int64, string, error) {
	tx, err := beginTenant(ctx, service, contractTenantID)
	if err != nil {
		return "", 0, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var contractID string
	var version int64
	var status string
	err = tx.QueryRow(ctx, `SELECT contract_id,contract_version,contract_status FROM contracts.apply_approved_contract_proposal($1,$2,$3,$4,$5)`, contractTenantID, id, actorID, auditID, now).Scan(&contractID, &version, &status)
	if err != nil {
		return "", 0, "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", 0, "", err
	}
	return contractID, version, status, nil
}

func insertProposalError(ctx context.Context, service *pgxpool.Pool, id, targetID string, initiator contractPrincipal, reauthenticatedAt, createdAt time.Time) error {
	tx, err := beginTenant(ctx, service, contractTenantID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `INSERT INTO contracts.contract_proposals(id,tenant_id,created_at,updated_at,initiator_user_id,initiator_membership_id,initiator_session_id,reauthenticated_at,action,target_contract_id,target_version,status,proposal_hash,reason_ref,reason_hash,contract_number,starts_at,ends_at,seat_limit,region,license_kind,expires_at) VALUES($1,$2,$3,$3,$4,$5,$6,$7,'create',$8,0,'proposed',$9,'encrypted://contracts/reasons/stale',$10,'ENT-STALE',$3,$11,5,'US','enterprise_cloud',$12)`, id, contractTenantID, createdAt, initiator.userID, initiator.membershipID, initiator.sessionID, reauthenticatedAt, targetID, strings.Repeat("e", 64), reasonHash, createdAt.Add(24*time.Hour), createdAt.Add(15*time.Minute))
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func tenantExec(ctx context.Context, pool *pgxpool.Pool, tenantID, statement string, arguments ...any) error {
	tx, err := beginTenant(ctx, pool, tenantID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, statement, arguments...); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func beginTenant(ctx context.Context, pool *pgxpool.Pool, tenantID string) (pgx.Tx, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func contractPool(t *testing.T, ctx context.Context, variable string) *pgxpool.Pool {
	t.Helper()
	value := os.Getenv(variable)
	if value == "" {
		t.Fatalf("missing %s", variable)
	}
	pool, err := pgxpool.New(ctx, value)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func contractNow() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }
