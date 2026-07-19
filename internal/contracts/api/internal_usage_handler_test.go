package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/langshift/lites/internal/security/transport"
)

type internalUsageStub struct {
	reserve func(InternalUsageReserveCommand) (InternalUsageResult, error)
	settle  func(InternalUsageSettleCommand) (InternalUsageResult, error)
	release func(InternalUsageReleaseCommand) (InternalUsageResult, error)
}

func (stub internalUsageStub) Reserve(_ context.Context, command InternalUsageReserveCommand) (InternalUsageResult, error) {
	return stub.reserve(command)
}
func (stub internalUsageStub) Settle(_ context.Context, command InternalUsageSettleCommand) (InternalUsageResult, error) {
	return stub.settle(command)
}
func (stub internalUsageStub) Release(_ context.Context, command InternalUsageReleaseCommand) (InternalUsageResult, error) {
	return stub.release(command)
}

func TestInternalUsageHandlerAcceptsOnlyAuthorizedWorkloadAndStrictCommands(t *testing.T) {
	tenantID := "79000000-0000-4000-8000-000000000001"
	userID := "79000000-0000-4000-8000-000000000002"
	subjectID := "79000000-0000-4000-8000-000000000003"
	reservationID := "79000000-0000-4000-8000-000000000004"
	handler := InternalUsageHandler{Authorize: func(*http.Request) (string, bool) { return "spiffe://lites.internal/workload/llm-gateway", true }, Service: internalUsageStub{
		reserve: func(command InternalUsageReserveCommand) (InternalUsageResult, error) {
			if command.TenantID != tenantID || command.UserID != userID || command.SubjectID != subjectID || command.SubjectVersion != 1 || command.RequestedUnits != 100 || command.WorkloadIdentity == "" || command.IdempotencyKey != "internal-usage-key-0001" {
				t.Fatalf("reserve command=%#v", command)
			}
			return InternalUsageResult{ReservationID: reservationID, Status: "reserved", ReservedUnits: 100}, nil
		},
		settle: func(command InternalUsageSettleCommand) (InternalUsageResult, error) {
			if command.TenantID != tenantID || command.ReservationID != reservationID || command.ProviderAttemptID != subjectID || command.ActualUnits != 80 || command.ProviderCostMicrounits != 500 || command.ExpectedVersion != 1 {
				t.Fatalf("settle command=%#v", command)
			}
			return InternalUsageResult{ReservationID: reservationID, Status: "settled", SettledUnits: 80, LedgerEntryID: "79000000-0000-4000-8000-000000000005"}, nil
		},
		release: func(command InternalUsageReleaseCommand) (InternalUsageResult, error) {
			if command.TenantID != tenantID || command.ReservationID != reservationID || command.Reason != "cancelled_before_dispatch" || command.ExpectedVersion != 1 {
				t.Fatalf("release command=%#v", command)
			}
			return InternalUsageResult{ReservationID: reservationID, Status: "released", ReleasedUnits: 100, LedgerEntryID: "79000000-0000-4000-8000-000000000006"}, nil
		},
	}}
	request := internalUsageRequest(http.MethodPost, "/v1/internal/usage/reservations", `{"request_id":"usage-reserve-request","tenant_id":"`+tenantID+`","user_id":"`+userID+`","operation_key":"provider-operation-0001","requested_units":100,"resource_kind":"provider_attempt","subject_id":"`+subjectID+`","subject_version":1,"byok":false}`, "internal-usage-key-0001", "")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status":"reserved"`) {
		t.Fatalf("reserve status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	request = internalUsageRequest(http.MethodPost, "/v1/internal/usage/reservations/"+reservationID+"/settle", `{"request_id":"usage-settle-request","tenant_id":"`+tenantID+`","actual_units":80,"provider_cost_microunits":500,"provider_attempt_id":"`+subjectID+`","expected_reservation_version":1}`, "internal-usage-key-0002", `"1"`)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status":"settled"`) {
		t.Fatalf("settle status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	request = internalUsageRequest(http.MethodPost, "/v1/internal/usage/reservations/"+reservationID+"/release", `{"request_id":"usage-release-request","tenant_id":"`+tenantID+`","reason":"cancelled_before_dispatch","expected_reservation_version":1}`, "internal-usage-key-0003", `"1"`)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status":"released"`) {
		t.Fatalf("release status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestInternalUsageHandlerRejectsBrowserAndMissingPrecondition(t *testing.T) {
	service := internalUsageStub{settle: func(InternalUsageSettleCommand) (InternalUsageResult, error) {
		t.Fatal("service called")
		return InternalUsageResult{}, nil
	}}
	handler := InternalUsageHandler{Authorize: func(*http.Request) (string, bool) { return "workload", true }, Service: service}
	request := internalUsageRequest(http.MethodPost, "/v1/internal/usage/reservations/79000000-0000-4000-8000-000000000004/settle", `{"request_id":"usage-settle-request","tenant_id":"79000000-0000-4000-8000-000000000001","actual_units":1,"provider_cost_microunits":1,"provider_attempt_id":"79000000-0000-4000-8000-000000000003","expected_reservation_version":1}`, "internal-usage-key-0002", "")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match status=%d", recorder.Code)
	}
	request = internalUsageRequest(http.MethodPost, "/v1/internal/usage/reservations", `{}`, "internal-usage-key-0003", "")
	request.Header.Set("Cookie", "__Host-lites_session=browser")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("browser status=%d", recorder.Code)
	}
}

func TestWorkloadAuthorizerUsesVerifiedCertificateIdentityAndLoopbackOnlyDevelopmentFallback(t *testing.T) {
	identity := "spiffe://lites.internal/workload/llm-gateway"
	parsed, err := url.Parse(identity)
	if err != nil {
		t.Fatal(err)
	}
	authorize := NewWorkloadAuthorizer([]string{identity}, false)
	request := httptest.NewRequest(http.MethodPost, "https://contracts.internal/v1/internal/usage/reservations", nil)
	certificate := &x509.Certificate{URIs: []*url.URL{parsed}}
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate}, VerifiedChains: [][]*x509.Certificate{{certificate}}}
	if got, ok := authorize(request); !ok || got != identity {
		t.Fatalf("certificate identity=%q ok=%v", got, ok)
	}
	development := NewWorkloadAuthorizer([]string{identity}, true)
	request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/internal/usage/reservations", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set(DevelopmentWorkloadIdentityHeader, identity)
	if got, ok := development(request); !ok || got != identity {
		t.Fatalf("development identity=%q ok=%v", got, ok)
	}
	request.RemoteAddr = "198.51.100.10:12345"
	if _, ok := development(request); ok {
		t.Fatal("remote plaintext workload identity was accepted")
	}
}

func internalUsageRequest(method, path, body, key, ifMatch string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(transport.IdempotencyHeader, key)
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	return request
}
