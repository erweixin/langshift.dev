package api

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

type accountServiceStub struct {
	get            func(context.Context, AccountQuery) (AccountResource, error)
	update         func(context.Context, AccountUpdateCommand) (AccountMutationResult, error)
	export         func(context.Context, AccountExportCreateCommand) (AccountMutationResult, error)
	exportGet      func(context.Context, AccountExportQuery) (AccountExportResource, error)
	exportDownload func(context.Context, AccountExportQuery) (AccountExportDownload, error)
	erase          func(context.Context, AccountErasureCreateCommand) (AccountMutationResult, error)
	cancel         func(context.Context, AccountErasureCancelCommand) (AccountMutationResult, error)
}

func (stub accountServiceStub) GetAccountExport(ctx context.Context, query AccountExportQuery) (AccountExportResource, error) {
	if stub.exportGet == nil {
		return AccountExportResource{}, errors.New("unexpected export get")
	}
	return stub.exportGet(ctx, query)
}
func (stub accountServiceStub) DownloadAccountExport(ctx context.Context, query AccountExportQuery) (AccountExportDownload, error) {
	if stub.exportDownload == nil {
		return AccountExportDownload{}, errors.New("unexpected export download")
	}
	return stub.exportDownload(ctx, query)
}

func (stub accountServiceStub) GetAccount(ctx context.Context, query AccountQuery) (AccountResource, error) {
	if stub.get == nil {
		return AccountResource{}, errors.New("unexpected account get")
	}
	return stub.get(ctx, query)
}
func (stub accountServiceStub) UpdateAccount(ctx context.Context, command AccountUpdateCommand) (AccountMutationResult, error) {
	if stub.update == nil {
		return AccountMutationResult{}, errors.New("unexpected account update")
	}
	return stub.update(ctx, command)
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

func TestAccountExportStatusAndDownloadStayOwnerScopedAndNoStore(t *testing.T) {
	expires := apiTestNow.Add(24 * time.Hour)
	service := accountServiceStub{
		exportGet: func(_ context.Context, query AccountExportQuery) (AccountExportResource, error) {
			if query.ExportID != "10000000-0000-4000-8000-000000000777" || query.UserID == "" || query.TenantID == "" {
				t.Fatalf("query=%#v", query)
			}
			return AccountExportResource{ID: query.ExportID, Version: 2, Status: "ready", Scope: []string{"account"}, Format: "json", CreatedAt: apiTestNow, UpdatedAt: apiTestNow, CompletedAt: &apiTestNow, ExpiresAt: &expires}, nil
		},
		exportDownload: func(_ context.Context, query AccountExportQuery) (AccountExportDownload, error) {
			if query.ExportID != "10000000-0000-4000-8000-000000000777" {
				t.Fatalf("query=%#v", query)
			}
			return AccountExportDownload{Filename: "lites-account-export.json", MediaType: "application/json", ContentHash: strings.Repeat("a", 64), Body: []byte(`{"schema_version":1}`)}, nil
		},
	}
	base := "/v1/account/export-requests/10000000-0000-4000-8000-000000000777"
	recorder := serveAuthenticatedHandler(t, Handler{Accounts: service}, http.MethodGet, base, "", "", "")
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"2"` || recorder.Header().Get("Cache-Control") != "private, no-store" || !strings.Contains(recorder.Body.String(), `"status":"ready"`) {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	recorder = serveAuthenticatedHandler(t, Handler{Accounts: service}, http.MethodGet, base+"/download", "", "", "")
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Disposition") != `attachment; filename="lites-account-export.json"` || recorder.Header().Get("Digest") == "" || recorder.Body.String() != `{"schema_version":1}` {
		t.Fatalf("download status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestAccountExportDownloadRejectsInvalidInternalDigest(t *testing.T) {
	service := accountServiceStub{exportDownload: func(_ context.Context, _ AccountExportQuery) (AccountExportDownload, error) {
		return AccountExportDownload{Filename: "lites-account-export.json", MediaType: "application/json", ContentHash: "not-a-sha256", Body: []byte(`{"schema_version":1}`)}, nil
	}}
	recorder := serveAuthenticatedHandler(t, Handler{Accounts: service}, http.MethodGet, "/v1/account/export-requests/10000000-0000-4000-8000-000000000777/download", "", "", "")
	if recorder.Code != http.StatusInternalServerError || recorder.Header().Get("Digest") != "" || strings.Contains(recorder.Body.String(), "schema_version") {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestAccountGetAndUpdateUseAuthenticatedOwnerAndCAS(t *testing.T) {
	name := "Ada Lovelace"
	service := accountServiceStub{
		get: func(_ context.Context, query AccountQuery) (AccountResource, error) {
			return AccountResource{UserID: query.UserID, NormalizedEmail: "owner@example.invalid", EmailVerified: true, Locale: "en", Timezone: "UTC", Version: 2, UpdatedAt: apiTestNow}, nil
		},
		update: func(_ context.Context, command AccountUpdateCommand) (AccountMutationResult, error) {
			if command.ExpectedVersion != 2 || command.Changes.Locale == nil || *command.Changes.Locale != "zh-CN" || command.Changes.Timezone == nil || *command.Changes.Timezone != "Asia/Shanghai" || !command.Changes.DisplayNameSet || command.Changes.DisplayName == nil || *command.Changes.DisplayName != name {
				t.Fatalf("command=%#v changes=%#v", command, command.Changes)
			}
			return AccountMutationResult{ID: command.UserID, Version: 3, Status: "updated", UpdatedAt: apiTestNow}, nil
		},
	}
	recorder := serveAuthenticatedHandler(t, Handler{Accounts: service}, http.MethodGet, "/v1/account", "", "", "")
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"2"` || !strings.Contains(recorder.Body.String(), `"normalized_email":"owner@example.invalid"`) {
		t.Fatalf("get status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	body := `{"request_id":"account-update-request-01","changes":{"locale":"zh-CN","timezone":"Asia/Shanghai","display_name":"Ada Lovelace"}}`
	recorder = serveAuthenticatedHandler(t, Handler{Accounts: service}, http.MethodPatch, "/v1/account", "account-update-key-01", `"2"`, body)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"3"` {
		t.Fatalf("update status=%d body=%s", recorder.Code, recorder.Body.String())
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
