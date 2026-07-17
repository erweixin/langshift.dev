//go:build integration

package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	contractsapi "github.com/langshift/lites/internal/contracts/api"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/payload"
)

func TestControlServiceCommitsIdempotencyEventsContractsAdjustmentsAndUsage(t *testing.T) {
	ctx := context.Background()
	admin := contractPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := contractPool(t, ctx, "LITES_TEST_CONTRACT_DATABASE_URL")
	defer pool.Close()

	const tenantID = "78000000-0000-4000-8000-000000001001"
	const epoch = "78000000-0000-4000-8000-000000001002"
	principals := []servicePrincipal{
		{"78000000-0000-4000-8000-000000001010", "78000000-0000-4000-8000-000000001020", "78000000-0000-4000-8000-000000001030", "contract_admin"},
		{"78000000-0000-4000-8000-000000001011", "78000000-0000-4000-8000-000000001021", "78000000-0000-4000-8000-000000001031", "owner"},
		{"78000000-0000-4000-8000-000000001012", "78000000-0000-4000-8000-000000001022", "78000000-0000-4000-8000-000000001032", "contract_admin"},
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	seedServicePrincipals(t, ctx, admin, tenantID, principals, now)
	blobs := &memoryPayloadStore{values: map[string][]byte{}}
	service := ControlService{Pool: pool, Appender: eventpostgres.Appender{}, Payloads: blobs, IDKey: bytes.Repeat([]byte{0x78}, 32), IdempotencyKeyPepper: bytes.Repeat([]byte{0x79}, 32), RequestDigestPepper: bytes.Repeat([]byte{0x7a}, 32), StoreEpoch: epoch, IdempotencyTTL: 24 * time.Hour, ProposalTTL: 5 * time.Minute, Now: func() time.Time { return time.Now().UTC() }}

	proposalCommand := contractsapi.ContractProposalCommand{CommandMetadata: serviceMetadata(tenantID, principals[0], 1, "contract-propose-key-0001"), Action: "create", ContractNumber: "ENT-SERVICE-2026", StartsAt: timePointer(now.Add(-time.Minute)), EndsAt: timePointer(now.Add(365 * 24 * time.Hour)), SeatLimit: 3, Region: "US", LicenseKind: "enterprise_cloud", Reason: "Signed annual enterprise agreement"}
	proposal, err := service.ProposeContract(ctx, proposalCommand)
	if err != nil || proposal.Status != "proposed" || proposal.Version != 1 || proposal.TargetID == "" || len(proposal.ProposalHash) != 64 {
		t.Fatalf("proposal=%#v err=%v", proposal, err)
	}
	replay, err := service.ProposeContract(ctx, proposalCommand)
	if err != nil || !replay.Replayed || replay.ID != proposal.ID || replay.ProposalHash != proposal.ProposalHash {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
	tampered := proposalCommand
	tampered.Reason = "Different agreement"
	if _, err = service.ProposeContract(ctx, tampered); !errors.Is(err, contractsapi.ErrIdempotencyConflict) {
		t.Fatalf("tampered idempotency err=%v", err)
	}

	first, err := service.DecideContract(ctx, contractsapi.ContractDecisionCommand{CommandMetadata: serviceMetadata(tenantID, principals[1], 2, "contract-decision-key-0001"), ProposalID: proposal.ID, Decision: "approve", ProposalHash: proposal.ProposalHash, TargetVersion: 0, ExpectedProposalVersion: 1})
	if err != nil || first.Status != "proposed" || first.ApprovalCount != 1 || first.Version != 1 {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	second, err := service.DecideContract(ctx, contractsapi.ContractDecisionCommand{CommandMetadata: serviceMetadata(tenantID, principals[2], 3, "contract-decision-key-0002"), ProposalID: proposal.ID, Decision: "approve", ProposalHash: proposal.ProposalHash, TargetVersion: 0, ExpectedProposalVersion: 1})
	if err != nil || second.Status != "executed" || second.ApprovalCount != 2 || second.Version != 2 || second.TargetVersion != 1 {
		t.Fatalf("second=%#v err=%v", second, err)
	}

	var contracts, seats, events, outbox, idempotencyRows int
	err = admin.QueryRow(ctx, `SELECT
      (SELECT count(*) FROM contracts.contracts WHERE tenant_id=$1 AND id=$2 AND status='active'),
      (SELECT count(*) FROM contracts.seat_allocations WHERE tenant_id=$1 AND contract_id=$2 AND status='active'),
      (SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND event_type IN ('ContractProposalCreated','ContractApprovalGranted','ContractProposalExecuted')),
      (SELECT count(*) FROM agent.outbox WHERE tenant_id=$1),
      (SELECT count(*) FROM agent.idempotency_responses WHERE tenant_id=$1 AND status='completed')`, tenantID, proposal.TargetID).Scan(&contracts, &seats, &events, &outbox, &idempotencyRows)
	if err != nil {
		t.Fatal(err)
	}
	if contracts != 1 || seats != 3 || events != 4 || outbox != 4 || idempotencyRows != 3 {
		t.Fatalf("contracts=%d seats=%d events=%d outbox=%d idempotency=%d", contracts, seats, events, outbox, idempotencyRows)
	}

	limit := int64(100)
	entitlementCommand := contractsapi.EntitlementProposalCommand{CommandMetadata: serviceMetadata(tenantID, principals[0], 7, "entitlement-propose-key-0001"), ContractID: proposal.TargetID, TargetContractVersion: 1, EntitlementKey: "audit_export", TargetEntitlementVersion: 0, LimitValue: &limit, Config: []byte(`{"format":"jsonl"}`), Reason: "Enable contracted audit exports"}
	entitlement, err := service.ProposeEntitlement(ctx, entitlementCommand)
	if err != nil || entitlement.Status != "proposed" || entitlement.Version != 1 || entitlement.TargetID != proposal.TargetID || entitlement.TargetVersion != 1 || len(entitlement.ProposalHash) != 64 {
		t.Fatalf("entitlement=%#v err=%v", entitlement, err)
	}
	entitlementReplay, err := service.ProposeEntitlement(ctx, entitlementCommand)
	if err != nil || !entitlementReplay.Replayed || entitlementReplay.ID != entitlement.ID || entitlementReplay.ProposalHash != entitlement.ProposalHash {
		t.Fatalf("entitlement replay=%#v err=%v", entitlementReplay, err)
	}
	if _, err = service.DecideEntitlement(ctx, contractsapi.EntitlementDecisionCommand{CommandMetadata: serviceMetadata(tenantID, principals[0], 8, "entitlement-self-decision-key-0001"), ProposalID: entitlement.ID, Decision: "approve", ProposalHash: entitlement.ProposalHash, TargetContractVersion: 1, TargetEntitlementVersion: 0, ExpectedProposalVersion: 1}); err == nil {
		t.Fatal("entitlement initiator approval unexpectedly succeeded")
	}
	entitlementFirst, err := service.DecideEntitlement(ctx, contractsapi.EntitlementDecisionCommand{CommandMetadata: serviceMetadata(tenantID, principals[1], 9, "entitlement-decision-key-0001"), ProposalID: entitlement.ID, Decision: "approve", ProposalHash: entitlement.ProposalHash, TargetContractVersion: 1, TargetEntitlementVersion: 0, ExpectedProposalVersion: 1})
	if err != nil || entitlementFirst.Status != "proposed" || entitlementFirst.ApprovalCount != 1 {
		t.Fatalf("entitlement first=%#v err=%v", entitlementFirst, err)
	}
	entitlementSecond, err := service.DecideEntitlement(ctx, contractsapi.EntitlementDecisionCommand{CommandMetadata: serviceMetadata(tenantID, principals[2], 10, "entitlement-decision-key-0002"), ProposalID: entitlement.ID, Decision: "approve", ProposalHash: entitlement.ProposalHash, TargetContractVersion: 1, TargetEntitlementVersion: 0, ExpectedProposalVersion: 1})
	if err != nil || entitlementSecond.Status != "executed" || entitlementSecond.ApprovalCount != 2 || entitlementSecond.Version != 2 || entitlementSecond.TargetVersion != 1 {
		t.Fatalf("entitlement second=%#v err=%v", entitlementSecond, err)
	}
	staleEntitlement := entitlementCommand
	staleEntitlement.CommandMetadata = serviceMetadata(tenantID, principals[0], 11, "entitlement-stale-key-0001")
	if _, err = service.ProposeEntitlement(ctx, staleEntitlement); err == nil {
		t.Fatal("stale entitlement version unexpectedly succeeded")
	}
	if _, err = pool.Exec(ctx, `INSERT INTO contracts.contract_entitlements(id,tenant_id,contract_id,version,entitlement_key,config,effective_at,created_at,updated_at) VALUES(gen_random_uuid(),$1,$2,2,'audit_export','{}',statement_timestamp(),statement_timestamp(),statement_timestamp())`, tenantID, proposal.TargetID); err == nil {
		t.Fatal("direct contract-service entitlement mutation unexpectedly succeeded")
	}
	var entitlementRows, entitlementEvents, entitlementAudits int
	if err = admin.QueryRow(ctx, `SELECT
      (SELECT count(*) FROM contracts.contract_entitlements WHERE tenant_id=$1 AND contract_id=$2 AND entitlement_key='audit_export' AND version=1 AND limit_value=100 AND config='{"format":"jsonl"}'::jsonb),
      (SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND event_type IN ('EntitlementProposed','EntitlementApprovalGranted','EntitlementProposalExecuted')),
      (SELECT count(*) FROM contracts.contract_audit_events WHERE tenant_id=$1 AND contract_id=$2 AND event_type='entitlement_set_executed' AND jsonb_array_length(approval_event_ids)=2)`, tenantID, proposal.TargetID).Scan(&entitlementRows, &entitlementEvents, &entitlementAudits); err != nil {
		t.Fatal(err)
	}
	if entitlementRows != 1 || entitlementEvents != 4 || entitlementAudits != 1 {
		t.Fatalf("entitlement-rows=%d entitlement-events=%d entitlement-audits=%d", entitlementRows, entitlementEvents, entitlementAudits)
	}

	bucketID := "78000000-0000-4000-8000-000000001040"
	if _, err = admin.Exec(ctx, `INSERT INTO contracts.credit_buckets(id,tenant_id,contract_id,bucket_kind,granted_units,starts_at,expires_at,created_at,updated_at) VALUES($1,$2,$3,'shared',1000,$4,$5,$6,$6)`, bucketID, tenantID, proposal.TargetID, now.Add(-time.Hour), now.Add(365*24*time.Hour), now); err != nil {
		t.Fatal(err)
	}
	adjustment, err := service.ProposeAdjustment(ctx, contractsapi.AdjustmentProposalCommand{CommandMetadata: serviceMetadata(tenantID, principals[0], 4, "adjustment-propose-key-0001"), BucketID: bucketID, TargetVersion: 1, Units: 250, Reason: "Contracted capacity correction"})
	if err != nil || adjustment.Status != "proposed" {
		t.Fatalf("adjustment=%#v err=%v", adjustment, err)
	}
	if _, err = service.DecideAdjustment(ctx, contractsapi.AdjustmentDecisionCommand{CommandMetadata: serviceMetadata(tenantID, principals[1], 5, "adjustment-decision-key-0001"), ProposalID: adjustment.ID, Decision: "approve", ProposalHash: adjustment.ProposalHash, TargetVersion: 1, ExpectedProposalVersion: 1}); err != nil {
		t.Fatal(err)
	}
	adjusted, err := service.DecideAdjustment(ctx, contractsapi.AdjustmentDecisionCommand{CommandMetadata: serviceMetadata(tenantID, principals[2], 6, "adjustment-decision-key-0002"), ProposalID: adjustment.ID, Decision: "approve", ProposalHash: adjustment.ProposalHash, TargetVersion: 1, ExpectedProposalVersion: 1})
	if err != nil || adjusted.Status != "executed" || adjusted.TargetVersion != 2 {
		t.Fatalf("adjusted=%#v err=%v", adjusted, err)
	}
	usageQuery := contractsapi.UsageQuery{RequestID: "78000000-0000-4000-8000-000000001199", TenantID: tenantID, UserID: principals[0].userID, MembershipID: principals[0].membershipID, SessionID: principals[0].sessionID, Reason: "Quarterly capacity review"}
	usage, err := service.ReadUsage(ctx, usageQuery)
	if err != nil || usage.GrantedUnits != 1250 || usage.AvailableUnits != 1250 || usage.ReservedUnits != 0 || usage.SettledUnits != 0 || usage.ActiveSeats != 3 || usage.SeatLimit != 3 {
		t.Fatalf("usage=%#v err=%v", usage, err)
	}
	if _, err = service.ReadUsage(ctx, usageQuery); err != nil {
		t.Fatalf("usage audit replay err=%v", err)
	}
	auditQuery := contractsapi.AuditQuery{RequestID: "78000000-0000-4000-8000-000000001200", TenantID: tenantID, UserID: principals[0].userID, MembershipID: principals[0].membershipID, SessionID: principals[0].sessionID, Reason: "Quarterly audit review", Limit: 2}
	auditPage, err := service.ReadAudit(ctx, auditQuery)
	if err != nil || len(auditPage.Items) != 2 || auditPage.NextBefore == "" {
		t.Fatalf("audit page=%#v err=%v", auditPage, err)
	}
	auditQuery.RequestID = "78000000-0000-4000-8000-000000001201"
	auditQuery.Before = auditPage.NextBefore
	nextAuditPage, err := service.ReadAudit(ctx, auditQuery)
	if err != nil || len(nextAuditPage.Items) == 0 {
		t.Fatalf("next audit page=%#v err=%v", nextAuditPage, err)
	}
	exportCommand := contractsapi.AuditExportCommand{CommandMetadata: serviceMetadata(tenantID, principals[0], 12, "audit-export-key-0001"), PeriodStart: now.Add(-time.Hour), PeriodEnd: now.Add(time.Hour), Kinds: []string{"contract_change", "accounting_adjustment", "admin_read"}, Format: "jsonl", Reason: "Annual compliance evidence"}
	auditExport, err := service.RequestAuditExport(ctx, exportCommand)
	if err != nil || auditExport.Status != "ready" || auditExport.RecordCount == 0 || auditExport.RecordCount > 100 || auditExport.ByteSize == 0 || len(auditExport.ContentHash) != 64 {
		t.Fatalf("audit export=%#v err=%v", auditExport, err)
	}
	exportReplay, err := service.RequestAuditExport(ctx, exportCommand)
	if err != nil || !exportReplay.Replayed || exportReplay.ID != auditExport.ID || exportReplay.ContentHash != auditExport.ContentHash {
		t.Fatalf("audit export replay=%#v err=%v", exportReplay, err)
	}
	exportDownload, err := service.ReadAuditExport(ctx, contractsapi.AuditExportQuery{RequestID: "78000000-0000-4000-8000-000000001250", TenantID: tenantID, UserID: principals[1].userID, MembershipID: principals[1].membershipID, SessionID: principals[1].sessionID, ExportID: auditExport.ID, Reason: "External auditor delivery"})
	if err != nil || exportDownload.ContentHash != auditExport.ContentHash || int64(len(exportDownload.Content)) != auditExport.ByteSize || bytes.Contains(exportDownload.Content, []byte(exportCommand.Reason)) {
		t.Fatalf("audit export download=%#v error=%v", exportDownload.AuditExport, err)
	}
	staleService := service
	staleService.Now = func() time.Time { return now.Add(6 * time.Minute) }
	staleCommand := exportCommand
	staleCommand.CommandMetadata = serviceMetadata(tenantID, principals[0], 13, "audit-export-key-stale-0001")
	if _, err = staleService.RequestAuditExport(ctx, staleCommand); !errors.Is(err, contractsapi.ErrReauthentication) {
		t.Fatalf("stale reauthentication audit export err=%v", err)
	}
	var exportRows, exportEvents, exportAccesses int
	if err = admin.QueryRow(ctx, `SELECT
      (SELECT count(*) FROM contracts.audit_exports WHERE tenant_id=$1 AND id=$2 AND status='ready' AND content_hash=$3),
      (SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND event_type='AuditExportCreated' AND aggregate_id=$2),
      (SELECT count(*) FROM contracts.admin_access_audit WHERE tenant_id=$1 AND action IN ('audit_export_created','audit_export_downloaded'))`, tenantID, auditExport.ID, auditExport.ContentHash).Scan(&exportRows, &exportEvents, &exportAccesses); err != nil {
		t.Fatal(err)
	}
	if exportRows != 1 || exportEvents != 1 || exportAccesses != 2 {
		t.Fatalf("audit export rows=%d events=%d accesses=%d", exportRows, exportEvents, exportAccesses)
	}
	var manual, adjustmentEvents, accessAudits int
	if err = admin.QueryRow(ctx, `SELECT
      (SELECT count(*) FROM contracts.manual_adjustments WHERE tenant_id=$1 AND bucket_id=$2 AND before_granted_units=1000 AND after_granted_units=1250 AND jsonb_array_length(approval_event_ids)=2),
      (SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND event_type IN ('AccountingAdjustmentProposed','AccountingAdjustmentApprovalGranted','AccountingAdjustmentExecuted')),
      (SELECT count(*) FROM contracts.admin_access_audit WHERE tenant_id=$1 AND ((action='usage_snapshot_read' AND reason_hash=$3) OR (action='audit_export_read' AND reason_hash=$4)))`, tenantID, bucketID, plaintextHash(usageQuery.Reason), plaintextHash(auditQuery.Reason)).Scan(&manual, &adjustmentEvents, &accessAudits); err != nil {
		t.Fatal(err)
	}
	if manual != 1 || adjustmentEvents != 4 || accessAudits != 3 {
		t.Fatalf("manual=%d adjustment-events=%d access-audits=%d", manual, adjustmentEvents, accessAudits)
	}
	t.Log(`contract_service_security={"entitlement_attack_cases":3,"unexpected_successes":0,"entitlement_approval_events":2,"entitlement_audit_completeness_percent":100,"admin_read_audits":3}`)
}

type servicePrincipal struct{ userID, membershipID, sessionID, role string }

func seedServicePrincipals(t *testing.T, ctx context.Context, admin *pgxpool.Pool, tenantID string, principals []servicePrincipal, now time.Time) {
	t.Helper()
	if _, err := admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region) VALUES($1,'enterprise','Contract Service','active','US')`, tenantID); err != nil {
		t.Fatal(err)
	}
	for index, principal := range principals {
		if _, err := admin.Exec(ctx, `INSERT INTO identity.users(id,normalized_email,email_verified_at,locale,status) VALUES($1,$2,$3,'en','active')`, principal.userID, fmt.Sprintf("contract-service-%d@lites.invalid", index), now); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(ctx, `INSERT INTO identity.memberships(id,tenant_id,user_id,role,status,joined_at,created_at,updated_at) VALUES($1,$2,$3,$4,'active',$5,$5,$5)`, principal.membershipID, tenantID, principal.userID, principal.role, now); err != nil {
			t.Fatal(err)
		}
		hash := bytes.Repeat([]byte{byte(0x81 + index)}, 32)
		if _, err := admin.Exec(ctx, `INSERT INTO identity.sessions(id,user_id,active_tenant_id,token_hash,csrf_secret_hash,ip_hash,user_agent_hash,last_seen_at,expires_at,reauthenticated_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$5,$5,$6,$7,$6,$6,$6)`, principal.sessionID, principal.userID, tenantID, hash, bytes.Repeat([]byte{byte(0x91 + index)}, 32), now, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
}

func serviceMetadata(tenantID string, principal servicePrincipal, suffix int, key string) contractsapi.CommandMetadata {
	return contractsapi.CommandMetadata{RequestID: fmt.Sprintf("78000000-0000-4000-8000-%012d", 1100+suffix), ClientRequestID: fmt.Sprintf("contract-client-%04d", suffix), IdempotencyKey: key, TenantID: tenantID, UserID: principal.userID, MembershipID: principal.membershipID, SessionID: principal.sessionID}
}

func timePointer(value time.Time) *time.Time { return &value }

type memoryPayloadStore struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (store *memoryPayloadStore) Put(_ context.Context, descriptor payload.Descriptor, plaintext []byte) (payload.Manifest, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	digest := sha256.Sum256(append([]byte(descriptor.TenantID+"\x00"+descriptor.ObjectID+"\x00"+descriptor.Class+"\x00"), plaintext...))
	hash := hex.EncodeToString(digest[:])
	ref := "memory://" + descriptor.TenantID + "/" + descriptor.Class + "/" + descriptor.ObjectID + "/" + hash
	store.values[ref] = append([]byte(nil), plaintext...)
	return payload.Manifest{Ref: ref, Hash: hash}, nil
}

func (store *memoryPayloadStore) Get(_ context.Context, _ payload.Descriptor, manifest payload.Manifest) ([]byte, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, ok := store.values[manifest.Ref]
	if !ok {
		return nil, errors.New("payload missing")
	}
	return append([]byte(nil), value...), nil
}
