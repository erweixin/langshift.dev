package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type byokServiceStub struct {
	list   func(BYOKListQuery) (BYOKListResult, error)
	create func(BYOKCreateCommand) (BYOKCredentialResource, error)
	delete func(BYOKDeleteCommand) (BYOKCredentialResource, error)
}

func (stub byokServiceStub) List(_ context.Context, q BYOKListQuery) (BYOKListResult, error) {
	return stub.list(q)
}
func (stub byokServiceStub) Create(_ context.Context, c BYOKCreateCommand) (BYOKCredentialResource, error) {
	return stub.create(c)
}
func (stub byokServiceStub) Delete(_ context.Context, c BYOKDeleteCommand) (BYOKCredentialResource, error) {
	return stub.delete(c)
}

type memoryPolicyServiceStub struct {
	get    func(MemoryPolicyQuery) (MemoryPolicyResource, error)
	update func(MemoryPolicyUpdateCommand) (MemoryPolicyResource, error)
}

func (stub memoryPolicyServiceStub) Get(_ context.Context, q MemoryPolicyQuery) (MemoryPolicyResource, error) {
	return stub.get(q)
}
func (stub memoryPolicyServiceStub) Update(_ context.Context, c MemoryPolicyUpdateCommand) (MemoryPolicyResource, error) {
	return stub.update(c)
}

func TestSettingsHandlerCreatesHostBoundBYOKAndUpdatesMemoryPolicy(t *testing.T) {
	now := time.Now().UTC()
	credentialID := "be000000-0000-4000-8000-000000000001"
	policyID := "be000000-0000-4000-8000-000000000002"
	handler := SettingsHandler{
		BYOK: byokServiceStub{create: func(command BYOKCreateCommand) (BYOKCredentialResource, error) {
			if command.ProviderID != "openai_compatible" || command.Endpoint != "https://models.example.com/v1" || command.APIKey != "provider-secret-value" || command.IdempotencyKey != "settings-byok-key-0001" {
				t.Fatalf("command=%#v", command)
			}
			return BYOKCredentialResource{ID: credentialID, Version: 1, Status: "active", ProviderID: command.ProviderID, BoundHost: "models.example.com", SecretHint: "••••alue", LastValidatedAt: &now, UpdatedAt: now}, nil
		}},
		Memory: memoryPolicyServiceStub{update: func(command MemoryPolicyUpdateCommand) (MemoryPolicyResource, error) {
			if !command.Enabled || command.ExpectedVersion != 1 || len(command.AllowedKinds) != 2 {
				t.Fatalf("command=%#v", command)
			}
			retention := 90
			return MemoryPolicyResource{ID: policyID, Version: 2, Status: "enabled", Enabled: true, RetentionDays: &retention, AllowedKinds: command.AllowedKinds, UpdatedAt: now}, nil
		}},
	}
	request := authenticatedMissionRequest(t, http.MethodPost, "/v1/byok-credentials", "application/json", `{"request_id":"settings-byok-request-01","provider_id":"openai_compatible","endpoint":"https://models.example.com/v1","api_key":"provider-secret-value"}`, "settings-byok-key-0001", "", true)
	recorder := httptest.NewRecorder()
	request.handler(handler).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"1"` {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	request = authenticatedMissionRequest(t, http.MethodPut, "/v1/memory-policy", "application/json", `{"request_id":"memory-policy-request-01","enabled":true,"retention_days":90,"allowed_kinds":["preference","goal"],"expected_policy_version":1}`, "memory-policy-key-0001", `"1"`, true)
	recorder = httptest.NewRecorder()
	request.handler(handler).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"2"` {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestSettingsHandlerRejectsUnsafeHostAndMapsReauthentication(t *testing.T) {
	called := false
	handler := SettingsHandler{BYOK: byokServiceStub{create: func(BYOKCreateCommand) (BYOKCredentialResource, error) {
		called = true
		return BYOKCredentialResource{}, nil
	}, list: func(BYOKListQuery) (BYOKListResult, error) { return BYOKListResult{}, ErrReauthentication }}}
	for _, endpoint := range []string{"http://models.example.com/v1", "https://127.0.0.1/v1", "https://10.0.0.1/v1", "https://[::1]/v1", "https://metadata.google.internal/v1", "https://user@models.example.com/v1", "https://models.example.com:8443/v1"} {
		request := authenticatedMissionRequest(t, http.MethodPost, "/v1/byok-credentials", "application/json", `{"request_id":"settings-byok-request-02","provider_id":"openai_compatible","endpoint":"`+endpoint+`","api_key":"provider-secret-value"}`, "settings-byok-key-0002", "", true)
		recorder := httptest.NewRecorder()
		request.handler(handler).ServeHTTP(recorder, request.request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("endpoint=%s status=%d", endpoint, recorder.Code)
		}
	}
	if called {
		t.Fatal("unsafe endpoint reached service")
	}
	request := authenticatedMissionRequest(t, http.MethodPost, "/v1/byok-credentials", "application/json", `{"request_id":"settings-byok-request-03","provider_id":"google","endpoint":null,"api_key":"provider-secret-value"}`, "settings-byok-key-0003", "", true)
	recorder := httptest.NewRecorder()
	request.handler(handler).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusBadRequest || called {
		t.Fatalf("deferred provider status=%d called=%v body=%s", recorder.Code, called, recorder.Body.String())
	}
	request = authenticatedMissionRequest(t, http.MethodGet, "/v1/byok-credentials", "", "", "", "", false)
	recorder = httptest.NewRecorder()
	request.handler(handler).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), `"code":"reauthentication_required"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestMemoryPolicyDisabledRequiresEmptyRetentionAndKinds(t *testing.T) {
	if !validMemoryPolicy(false, nil, []string{}) || validMemoryPolicy(false, intTestPointer(30), []string{}) || validMemoryPolicy(true, intTestPointer(30), []string{"goal", "goal"}) {
		t.Fatal("memory policy validation contract drifted")
	}
}
func intTestPointer(value int) *int { return &value }
