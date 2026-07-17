package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

type controlServiceStub struct {
	createConversation func(CreateConversationCommand) (ConversationResult, error)
	getConversation    func(GetConversationCommand) (ConversationDetailResult, error)
	createMessage      func(CreateMessageCommand) (MessageResult, error)
	getRun             func(GetRunCommand) (RunResult, error)
	cancelRun          func(CancelRunCommand) (RunResult, error)
}

func (stub controlServiceStub) GetConversation(_ context.Context, command GetConversationCommand) (ConversationDetailResult, error) {
	if stub.getConversation == nil {
		return ConversationDetailResult{}, errors.New("unexpected GetConversation")
	}
	return stub.getConversation(command)
}

func (stub controlServiceStub) CreateConversation(_ context.Context, command CreateConversationCommand) (ConversationResult, error) {
	if stub.createConversation == nil {
		return ConversationResult{}, errors.New("unexpected CreateConversation")
	}
	return stub.createConversation(command)
}

func (stub controlServiceStub) CreateMessage(_ context.Context, command CreateMessageCommand) (MessageResult, error) {
	if stub.createMessage == nil {
		return MessageResult{}, errors.New("unexpected CreateMessage")
	}
	return stub.createMessage(command)
}

func (stub controlServiceStub) GetRun(_ context.Context, command GetRunCommand) (RunResult, error) {
	if stub.getRun == nil {
		return RunResult{}, errors.New("unexpected GetRun")
	}
	return stub.getRun(command)
}

func (stub controlServiceStub) CancelRun(_ context.Context, command CancelRunCommand) (RunResult, error) {
	if stub.cancelRun == nil {
		return RunResult{}, errors.New("unexpected CancelRun")
	}
	return stub.cancelRun(command)
}

func TestControlHandlerPublicContract(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 30, 0, 0, time.UTC)
	t.Run("create Conversation", func(t *testing.T) {
		title := "Role transition"
		handler := ControlHandler{Service: controlServiceStub{createConversation: func(command CreateConversationCommand) (ConversationResult, error) {
			if command.TenantID != "tenant-1" || command.UserID != "user-1" || command.MissionID != "mission-1" || command.Title == nil || *command.Title != title || command.Mode != "coach" || command.IdempotencyKey != "control-key-0000000001" {
				t.Fatalf("command=%#v", command)
			}
			return ConversationResult{ID: "conversation-1", Version: 1, Status: "active", UpdatedAt: now}, nil
		}}}
		response := serveControlRequest(t, handler, http.MethodPost, "/v1/conversations", `{"request_id":"client-conversation-1","mission_id":"mission-1","title":"Role transition","mode":"coach"}`, true, "", "control-key-0000000001")
		if response.Code != http.StatusOK || response.Header().Get("ETag") != `"1"` || !strings.Contains(response.Body.String(), `"id":"conversation-1"`) {
			t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
		}
	})

	t.Run("get Conversation returns only service-projected messages", func(t *testing.T) {
		handler := ControlHandler{Service: controlServiceStub{getConversation: func(command GetConversationCommand) (ConversationDetailResult, error) {
			if command.ConversationID != "conversation-1" || command.TenantID != "tenant-1" || command.UserID != "user-1" || command.Limit != 25 || command.BeforeCreatedAt != nil {
				t.Fatalf("command=%#v", command)
			}
			return ConversationDetailResult{ID: "conversation-1", MissionID: "mission-1", Version: 2, Mode: "coach", Status: "active", Messages: []ConversationMessageResult{{ID: "message-1", RunID: "run-1", Role: "assistant", Content: "Keep the scope narrow.", CreatedAt: now}}, UpdatedAt: now}, nil
		}}}
		response := serveControlRequest(t, handler, http.MethodGet, "/v1/conversations/conversation-1?limit=25", ``, false, "", "")
		if response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` || !strings.Contains(response.Body.String(), `"content":"Keep the scope narrow."`) {
			t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
		}
	})

	t.Run("create Message", func(t *testing.T) {
		handler := ControlHandler{Service: controlServiceStub{createMessage: func(command CreateMessageCommand) (MessageResult, error) {
			if command.ConversationID != "conversation-1" || command.Content != "Help me plan" || command.ExpectedConversationVersion != 1 || command.Mode != "enqueue" {
				t.Fatalf("command=%#v", command)
			}
			return MessageResult{RunID: "run-1", Status: "queued", AcceptedAt: now, ConversationVersion: 2}, nil
		}}}
		response := serveControlRequest(t, handler, http.MethodPost, "/v1/messages", `{"request_id":"client-message-0001","conversation_id":"conversation-1","content":"Help me plan","mode":"enqueue","expected_conversation_version":1}`, true, `"1"`, "control-key-0000000001")
		if response.Code != http.StatusAccepted || response.Header().Get("ETag") != `"2"` || strings.Contains(response.Body.String(), "conversation_version") || !strings.Contains(response.Body.String(), `"run_id":"run-1"`) {
			t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
		}
	})

	t.Run("get Run does not require CSRF", func(t *testing.T) {
		handler := ControlHandler{Service: controlServiceStub{getRun: func(command GetRunCommand) (RunResult, error) {
			if command.RunID != "run-1" || command.TenantID != "tenant-1" || command.UserID != "user-1" {
				t.Fatalf("command=%#v", command)
			}
			return RunResult{ID: "run-1", Version: 3, Status: "executing", UpdatedAt: now}, nil
		}}}
		response := serveControlRequest(t, handler, http.MethodGet, "/v1/runs/run-1", ``, false, "", "")
		if response.Code != http.StatusOK || response.Header().Get("ETag") != `"3"` {
			t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
		}
	})

	t.Run("cancel Run", func(t *testing.T) {
		handler := ControlHandler{Service: controlServiceStub{cancelRun: func(command CancelRunCommand) (RunResult, error) {
			if command.RunID != "run-1" || command.ExpectedRunVersion != 3 || command.Reason != "user changed direction" {
				t.Fatalf("command=%#v", command)
			}
			return RunResult{ID: "run-1", Version: 4, Status: "cancelled", UpdatedAt: now}, nil
		}}}
		response := serveControlRequest(t, handler, http.MethodPost, "/v1/runs/run-1/cancel", `{"request_id":"client-cancel-0001","reason":"user changed direction","expected_run_version":3}`, true, `"3"`, "control-key-0000000001")
		if response.Code != http.StatusOK || response.Header().Get("ETag") != `"4"` {
			t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
		}
	})
}

func TestControlHandlerRejectsBoundaryViolations(t *testing.T) {
	service := controlServiceStub{createMessage: func(CreateMessageCommand) (MessageResult, error) {
		t.Fatal("service must not be called")
		return MessageResult{}, nil
	}}
	tests := []struct {
		name, ifMatch, key, body string
		csrf                     bool
		status                   int
		code                     string
	}{
		{"missing precondition", "", "control-key-0000000001", `{"request_id":"client-message-0001","conversation_id":"c","content":"x","mode":"enqueue","expected_conversation_version":1}`, true, http.StatusPreconditionRequired, "precondition_required"},
		{"body header mismatch", `"2"`, "control-key-0000000001", `{"request_id":"client-message-0001","conversation_id":"c","content":"x","mode":"enqueue","expected_conversation_version":1}`, true, http.StatusConflict, "version_conflict"},
		{"missing CSRF", `"1"`, "control-key-0000000001", `{"request_id":"client-message-0001","conversation_id":"c","content":"x","mode":"enqueue","expected_conversation_version":1}`, false, http.StatusUnauthorized, "authentication_required"},
		{"invalid key", `"1"`, "short", `{"request_id":"client-message-0001","conversation_id":"c","content":"x","mode":"enqueue","expected_conversation_version":1}`, true, http.StatusBadRequest, "validation_failed"},
		{"unknown field", `"1"`, "control-key-0000000001", `{"request_id":"client-message-0001","conversation_id":"c","content":"x","mode":"enqueue","expected_conversation_version":1,"extra":true}`, true, http.StatusBadRequest, "validation_failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := serveControlRequest(t, ControlHandler{Service: service}, http.MethodPost, "/v1/messages", test.body, test.csrf, test.ifMatch, test.key)
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func serveControlRequest(t *testing.T, handler http.Handler, method, target, body string, csrf bool, ifMatch, key string) *httptest.ResponseRecorder {
	t.Helper()
	now := time.Date(2026, time.July, 16, 12, 30, 0, 0, time.UTC)
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x33}, ed25519.SeedSize))
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set(transport.RequestIDHeader, "server-control-request")
	if key != "" {
		request.Header.Set(transport.IdempotencyHeader, key)
	}
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	claims := trustedcontext.Claims{
		PrincipalKind: trustedcontext.AuthenticatedUser, Issuer: "gateway", Audience: "agent-control",
		SubjectID: "user-1", TenantID: "tenant-1", MembershipID: "membership-1", SessionID: "session-1", Roles: []string{"member"},
		RequestID: "server-control-request", RequestMethod: method, RequestTarget: target,
		ClientIPHash: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x31}, 32)), UserAgentHash: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x32}, 32)),
		CSRFVerified: csrf, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Minute).Unix(), Nonce: "control-nonce",
	}
	token, err := trustedcontext.Sign(claims, "control-key", privateKey, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(transport.TrustedContextHeader, token)
	response := httptest.NewRecorder()
	verified := serviceauth.Middleware{Verifier: trustedcontext.Verifier{Issuer: "gateway", Audience: "agent-control", Keys: map[string]ed25519.PublicKey{"control-key": privateKey.Public().(ed25519.PublicKey)}, MaximumTTL: 2 * time.Minute}, Now: func() time.Time { return now }}.Wrap(handler)
	verified.ServeHTTP(response, request)
	return response
}
