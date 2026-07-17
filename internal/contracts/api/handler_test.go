package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

var contractAPINow = time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)

type serviceStub struct {
	proposeContract    func(ContractProposalCommand) (Resource, error)
	decideContract     func(ContractDecisionCommand) (Resource, error)
	proposeAdjustment  func(AdjustmentProposalCommand) (Resource, error)
	decideAdjustment   func(AdjustmentDecisionCommand) (Resource, error)
	proposeEntitlement func(EntitlementProposalCommand) (Resource, error)
	decideEntitlement  func(EntitlementDecisionCommand) (Resource, error)
	readUsage          func(UsageQuery) (UsageSnapshot, error)
	readAudit          func(AuditQuery) (AuditPage, error)
	requestAuditExport func(AuditExportCommand) (AuditExport, error)
	readAuditExport    func(AuditExportQuery) (AuditExportDownload, error)
}

func (stub serviceStub) ProposeContract(_ context.Context, command ContractProposalCommand) (Resource, error) {
	return stub.proposeContract(command)
}
func (stub serviceStub) DecideContract(_ context.Context, command ContractDecisionCommand) (Resource, error) {
	return stub.decideContract(command)
}
func (stub serviceStub) ProposeAdjustment(_ context.Context, command AdjustmentProposalCommand) (Resource, error) {
	return stub.proposeAdjustment(command)
}
func (stub serviceStub) DecideAdjustment(_ context.Context, command AdjustmentDecisionCommand) (Resource, error) {
	return stub.decideAdjustment(command)
}
func (stub serviceStub) ProposeEntitlement(_ context.Context, command EntitlementProposalCommand) (Resource, error) {
	return stub.proposeEntitlement(command)
}
func (stub serviceStub) DecideEntitlement(_ context.Context, command EntitlementDecisionCommand) (Resource, error) {
	return stub.decideEntitlement(command)
}
func (stub serviceStub) ReadUsage(_ context.Context, query UsageQuery) (UsageSnapshot, error) {
	return stub.readUsage(query)
}
func (stub serviceStub) ReadAudit(_ context.Context, query AuditQuery) (AuditPage, error) {
	return stub.readAudit(query)
}
func (stub serviceStub) RequestAuditExport(_ context.Context, command AuditExportCommand) (AuditExport, error) {
	return stub.requestAuditExport(command)
}
func (stub serviceStub) ReadAuditExport(_ context.Context, query AuditExportQuery) (AuditExportDownload, error) {
	return stub.readAuditExport(query)
}

func TestContractProposalUsesSignedTenantMembershipSessionAndStrictTerms(t *testing.T) {
	proposalID := "78000000-0000-4000-8000-000000000001"
	service := serviceStub{proposeContract: func(command ContractProposalCommand) (Resource, error) {
		if command.TenantID != "78000000-0000-4000-8000-000000000011" || command.UserID != "78000000-0000-4000-8000-000000000010" || command.MembershipID != "78000000-0000-4000-8000-000000000012" || command.SessionID != "78000000-0000-4000-8000-000000000013" {
			t.Fatalf("trusted metadata=%#v", command.CommandMetadata)
		}
		if command.Action != "create" || command.TargetVersion != 0 || command.ContractNumber != "ENT-2026" || command.SeatLimit != 50 || command.Region != "US" || command.LicenseKind != "enterprise_cloud" || command.StartsAt == nil || command.EndsAt == nil || command.Reason != "Annual agreement" {
			t.Fatalf("command=%#v", command)
		}
		return Resource{ID: proposalID, Version: 1, Status: "proposed", ProposalHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", TargetID: "78000000-0000-4000-8000-000000000002", UpdatedAt: contractAPINow}, nil
	}}
	body := `{"request_id":"contract-client-0001","action":"create","target_contract_id":"","target_version":0,"contract_number":" ENT-2026 ","starts_at":"2026-08-01T00:00:00Z","ends_at":"2027-08-01T00:00:00Z","seat_limit":50,"region":" US ","license_kind":"enterprise_cloud","reason":" Annual agreement "}`
	request, handler := signedContractRequest(t, "contract_admin", http.MethodPost, "/v1/admin/contracts", contractProposalMedia, body, "contract-idempotency-key-0001", "", true, Handler{Service: service})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != resourceMedia || recorder.Header().Get("ETag") != `"1"` {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestAdjustmentDecisionRequiresExactProposalCASAndBinding(t *testing.T) {
	proposalID := "78000000-0000-4000-8000-000000000020"
	hash := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	service := serviceStub{decideAdjustment: func(command AdjustmentDecisionCommand) (Resource, error) {
		if command.ProposalID != proposalID || command.Decision != "approve" || command.ProposalHash != hash || command.TargetVersion != 7 || command.ExpectedProposalVersion != 1 {
			t.Fatalf("decision=%#v", command)
		}
		return Resource{ID: proposalID, Version: 2, Status: "executed", ProposalHash: hash, TargetVersion: 8, ApprovalCount: 2, UpdatedAt: contractAPINow}, nil
	}}
	body := `{"request_id":"adjustment-client-0001","decision":"approve","proposal_hash":"` + hash + `","target_version":7,"expected_proposal_version":1}`
	request, handler := signedContractRequest(t, "owner", http.MethodPost, "/v1/admin/usage/adjustments/"+proposalID+"/approval-decisions", adjustmentDecisionMedia, body, "adjustment-idempotency-key-0001", `"1"`, true, Handler{Service: service})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"2"` {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request, handler = signedContractRequest(t, "owner", http.MethodPost, "/v1/admin/usage/adjustments/"+proposalID+"/approval-decisions", adjustmentDecisionMedia, body, "adjustment-idempotency-key-0002", `"2"`, true, Handler{Service: service})
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale CAS status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request, handler = signedContractRequest(t, "owner", http.MethodPost, "/v1/admin/usage/adjustments/"+proposalID+"/approval-decisions", adjustmentDecisionMedia, body, "adjustment-idempotency-key-0003", "", true, Handler{Service: service})
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing CAS status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestContractAdminBoundaryRejectsMemberMissingCSRFAndUnknownFields(t *testing.T) {
	service := serviceStub{proposeAdjustment: func(AdjustmentProposalCommand) (Resource, error) {
		t.Fatal("service called")
		return Resource{}, nil
	}}
	valid := `{"request_id":"adjustment-client-0002","bucket_id":"78000000-0000-4000-8000-000000000030","target_version":1,"units":100,"reason":"Correction"}`
	tests := []struct {
		name, role, body string
		csrf             bool
		want             int
	}{
		{"member", "member", valid, true, http.StatusForbidden},
		{"missing csrf", "contract_admin", valid, false, http.StatusUnauthorized},
		{"unknown field", "contract_admin", `{"request_id":"adjustment-client-0002","bucket_id":"78000000-0000-4000-8000-000000000030","target_version":1,"units":100,"reason":"Correction","tenant_id":"forged"}`, true, http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, handler := signedContractRequest(t, test.role, http.MethodPost, "/v1/admin/usage/adjustments", adjustmentProposalMedia, test.body, "adjustment-idempotency-key-0003", "", test.csrf, Handler{Service: service})
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.want {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestUsageReadRequiresAndBindsAuditReason(t *testing.T) {
	service := serviceStub{readUsage: func(query UsageQuery) (UsageSnapshot, error) {
		if query.TenantID != "78000000-0000-4000-8000-000000000011" || query.UserID != "78000000-0000-4000-8000-000000000010" || query.MembershipID == "" || query.SessionID == "" || query.RequestID == "" || query.Reason != "Quarterly capacity review" {
			t.Fatalf("query=%#v", query)
		}
		return UsageSnapshot{TenantID: query.TenantID, AsOf: contractAPINow}, nil
	}}
	request, handler := signedContractRequest(t, "contract_admin", http.MethodGet, "/v1/admin/usage", "", "", "", "", false, Handler{Service: service})
	request.Header.Set(auditReasonHeader, " Quarterly capacity review ")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != usageMedia {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request, handler = signedContractRequest(t, "contract_admin", http.MethodGet, "/v1/admin/usage", "", "", "", "", false, Handler{Service: service})
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing audit reason status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAuditReadUsesBoundedStrictSignedCursorInput(t *testing.T) {
	service := serviceStub{readAudit: func(query AuditQuery) (AuditPage, error) {
		if query.Reason != "Security review" || query.Limit != 25 || query.Before != "signed-cursor" || query.MembershipID == "" || query.SessionID == "" {
			t.Fatalf("query=%#v", query)
		}
		return AuditPage{Items: []AuditRecord{}}, nil
	}}
	request, handler := signedContractRequest(t, "owner", http.MethodGet, "/v1/admin/audit?limit=25&before=signed-cursor", "", "", "", "", false, Handler{Service: service})
	request.Header.Set(auditReasonHeader, "Security review")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != auditPageMedia {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request, handler = signedContractRequest(t, "owner", http.MethodGet, "/v1/admin/audit?tenant_id=forged", "", "", "", "", false, Handler{Service: service})
	request.Header.Set(auditReasonHeader, "Security review")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown query status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAuditExportRequestAndDownloadBindSensitiveAccessContext(t *testing.T) {
	exportID := "78000000-0000-4000-8000-000000000060"
	service := serviceStub{
		requestAuditExport: func(command AuditExportCommand) (AuditExport, error) {
			if command.TenantID == "" || command.MembershipID == "" || command.SessionID == "" || command.Format != "jsonl" || command.Reason != "Annual compliance evidence" || len(command.Kinds) != 2 || command.Kinds[0] != "contract_change" || command.Kinds[1] != "admin_read" {
				t.Fatalf("command=%#v", command)
			}
			return AuditExport{ID: exportID, Version: 1, Status: "ready", Format: "jsonl", ContentHash: strings.Repeat("a", 64), CreatedAt: contractAPINow}, nil
		},
		readAuditExport: func(query AuditExportQuery) (AuditExportDownload, error) {
			if query.ExportID != exportID || query.Reason != "External auditor delivery" || query.TenantID == "" || query.MembershipID == "" || query.SessionID == "" {
				t.Fatalf("query=%#v", query)
			}
			return AuditExportDownload{AuditExport: AuditExport{ID: exportID, Format: "jsonl", ContentHash: strings.Repeat("a", 64)}, Content: []byte("{\"id\":\"record-1\"}\n")}, nil
		},
	}
	body := `{"request_id":"audit-export-client-0001","period_start":"2026-01-01T00:00:00Z","period_end":"2026-07-01T00:00:00Z","kinds":["contract_change","admin_read"],"format":"jsonl","reason":" Annual compliance evidence "}`
	request, handler := signedContractRequest(t, "owner", http.MethodPost, "/v1/admin/audit-exports", auditExportRequestMedia, body, "audit-export-idempotency-0001", "", true, Handler{Service: service})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != auditExportResourceMedia {
		t.Fatalf("request status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request, handler = signedContractRequest(t, "owner", http.MethodGet, "/v1/admin/audit-exports/"+exportID, "", "", "", "", false, Handler{Service: service})
	request.Header.Set(auditReasonHeader, " External auditor delivery ")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "application/x-ndjson" || !strings.Contains(recorder.Header().Get("Content-Disposition"), exportID+".jsonl") || recorder.Body.String() != "{\"id\":\"record-1\"}\n" {
		t.Fatalf("download status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestEntitlementProposalBindsContractAndEntitlementVersions(t *testing.T) {
	contractID := "78000000-0000-4000-8000-000000000050"
	service := serviceStub{proposeEntitlement: func(command EntitlementProposalCommand) (Resource, error) {
		if command.ContractID != contractID || command.TargetContractVersion != 3 || command.EntitlementKey != "audit_export" || command.TargetEntitlementVersion != 1 || command.LimitValue == nil || *command.LimitValue != 100 || string(command.Config) != `{"format":"jsonl"}` || command.Reason != "Annual entitlement update" {
			t.Fatalf("command=%#v", command)
		}
		return Resource{ID: "78000000-0000-4000-8000-000000000051", Version: 1, Status: "proposed", ProposalHash: strings.Repeat("c", 64), TargetID: contractID, TargetVersion: 3, UpdatedAt: contractAPINow}, nil
	}}
	body := `{"request_id":"entitlement-client-0001","contract_id":"` + contractID + `","expected_contract_version":3,"entitlement_key":"audit_export","expected_entitlement_version":1,"limit_value":100,"config":{"format":"jsonl"},"reason":"Annual entitlement update"}`
	request, handler := signedContractRequest(t, "contract_admin", http.MethodPost, "/v1/admin/entitlements", entitlementProposalMedia, body, "entitlement-idempotency-key-0001", `"3"`, true, Handler{Service: service})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func signedContractRequest(t *testing.T, role, method, target, media, body, key, ifMatch string, csrf bool, application Handler) (*http.Request, http.Handler) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", media)
	request.Header.Set(transport.IdempotencyHeader, key)
	request.Header.Set("If-Match", ifMatch)
	request.Header.Set(transport.RequestIDHeader, "78000000-0000-4000-8000-000000000014")
	fingerprint := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x78}, 32))
	claims := trustedcontext.Claims{PrincipalKind: trustedcontext.AuthenticatedUser, Issuer: "gateway", Audience: "contract-service", SubjectID: "78000000-0000-4000-8000-000000000010", TenantID: "78000000-0000-4000-8000-000000000011", MembershipID: "78000000-0000-4000-8000-000000000012", SessionID: "78000000-0000-4000-8000-000000000013", Roles: []string{role}, RequestID: "78000000-0000-4000-8000-000000000014", RequestMethod: method, RequestTarget: target, ClientIPHash: fingerprint, UserAgentHash: fingerprint, CSRFVerified: csrf, IssuedAt: contractAPINow.Unix(), ExpiresAt: contractAPINow.Add(time.Minute).Unix(), Nonce: "contract-api-nonce"}
	token, err := trustedcontext.Sign(claims, "key-1", privateKey, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(transport.TrustedContextHeader, token)
	wrapped := serviceauth.Middleware{Verifier: trustedcontext.Verifier{Issuer: "gateway", Audience: "contract-service", Keys: map[string]ed25519.PublicKey{"key-1": publicKey}, MaximumTTL: 2 * time.Minute}, Now: func() time.Time { return contractAPINow }}.Wrap(application)
	return request, wrapped
}
