package api

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

type accountServiceStub struct {
	export func(context.Context, AccountExportCreateCommand) (AccountMutationResult, error)
	erase  func(context.Context, AccountErasureCreateCommand) (AccountMutationResult, error)
	cancel func(context.Context, AccountErasureCancelCommand) (AccountMutationResult, error)
}

func (stub accountServiceStub) CreateAccountExport(ctx context.Context, command AccountExportCreateCommand) (AccountMutationResult, error) {
	if stub.export == nil {
		return AccountMutationResult{}, errors.New("unexpected export")
	}
	return stub.export(ctx, command)
}
func (stub accountServiceStub) CreateAccountErasure(ctx context.Context, command AccountErasureCreateCommand) (AccountMutationResult, error) {
	if stub.erase == nil {
		return AccountMutationResult{}, errors.New("unexpected erasure")
	}
	return stub.erase(ctx, command)
}
func (stub accountServiceStub) CancelAccountErasure(ctx context.Context, command AccountErasureCancelCommand) (AccountMutationResult, error) {
	if stub.cancel == nil {
		return AccountMutationResult{}, errors.New("unexpected cancellation")
	}
	return stub.cancel(ctx, command)
}

func TestAccountExportNormalizesScopeAndRejectsDuplicates(t *testing.T) {
	called := false
	service := accountServiceStub{export: func(_ context.Context, command AccountExportCreateCommand) (AccountMutationResult, error) {
		called = true
		if !reflect.DeepEqual(command.Scope, []string{"account", "evidence", "missions"}) || command.Format != "zip" || command.ClientRequestID != "export-request-001" {
			t.Fatalf("command=%#v", command)
		}
		return AccountMutationResult{ID: "export-1", Version: 1, Status: "requested", UpdatedAt: apiTestNow}, nil
	}}
	body := `{"request_id":"export-request-001","scope":["missions","account","evidence"],"format":"zip"}`
	recorder := serveAuthenticatedHandler(t, Handler{Accounts: service}, http.MethodPost, "/v1/account/export-requests", "export-key-000001", "", body)
	if recorder.Code != http.StatusOK || !called || recorder.Header().Get("ETag") != `"1"` {
		t.Fatalf("status=%d called=%v body=%s", recorder.Code, called, recorder.Body.String())
	}
	called = false
	body = `{"request_id":"export-request-002","scope":["account","account"],"format":"json"}`
	recorder = serveAuthenticatedHandler(t, Handler{Accounts: service}, http.MethodPost, "/v1/account/export-requests", "export-key-000002", "", body)
	if recorder.Code != http.StatusBadRequest || called {
		t.Fatalf("duplicate status=%d called=%v body=%s", recorder.Code, called, recorder.Body.String())
	}
}

func TestAccountErasureRequiresExactConfirmationAndPreservesNullableReason(t *testing.T) {
	service := accountServiceStub{erase: func(_ context.Context, command AccountErasureCreateCommand) (AccountMutationResult, error) {
		if command.Reason == nil || *command.Reason != "privacy choice" {
			t.Fatalf("command=%#v", command)
		}
		return AccountMutationResult{ID: "erasure-1", Version: 1, Status: "requested", UpdatedAt: apiTestNow}, nil
	}}
	body := `{"request_id":"erasure-request-001","confirmation":"DELETE MY ACCOUNT","reason":"privacy choice"}`
	recorder := serveAuthenticatedHandler(t, Handler{Accounts: service}, http.MethodPost, "/v1/account/erasure-requests", "erasure-key-0001", "", body)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status":"requested"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	body = `{"request_id":"erasure-request-002","confirmation":"delete my account","reason":null}`
	recorder = serveAuthenticatedHandler(t, Handler{Accounts: service}, http.MethodPost, "/v1/account/erasure-requests", "erasure-key-0002", "", body)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("confirmation status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAccountErasureCancellationRequiresMatchingVersion(t *testing.T) {
	called := false
	service := accountServiceStub{cancel: func(_ context.Context, command AccountErasureCancelCommand) (AccountMutationResult, error) {
		called = true
		if command.ErasureID != "erasure-123" || command.ExpectedVersion != 3 {
			t.Fatalf("command=%#v", command)
		}
		return AccountMutationResult{ID: command.ErasureID, Version: 4, Status: "cancelled", UpdatedAt: apiTestNow}, nil
	}}
	body := `{"request_id":"cancel-request-001","expected_erasure_version":3}`
	recorder := serveAuthenticatedHandler(t, Handler{Accounts: service}, http.MethodDelete, "/v1/account/erasure-requests/erasure-123", "cancel-key-000001", `"3"`, body)
	if recorder.Code != http.StatusOK || !called || recorder.Header().Get("ETag") != `"4"` {
		t.Fatalf("status=%d called=%v body=%s", recorder.Code, called, recorder.Body.String())
	}
	called = false
	recorder = serveAuthenticatedHandler(t, Handler{Accounts: service}, http.MethodDelete, "/v1/account/erasure-requests/erasure-123", "cancel-key-000001", `"2"`, body)
	if recorder.Code != http.StatusBadRequest || called {
		t.Fatalf("mismatch status=%d called=%v body=%s", recorder.Code, called, recorder.Body.String())
	}
}
