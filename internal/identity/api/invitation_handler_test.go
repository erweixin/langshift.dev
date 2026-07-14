package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

type invitationServiceStub struct {
	create    func(context.Context, InvitationCreateCommand) (InvitationMutationResult, error)
	importCSV func(context.Context, InvitationImportCommand) (InvitationMutationResult, error)
	accept    func(context.Context, InvitationAcceptCommand) (InvitationMutationResult, error)
}

func (s invitationServiceStub) CreateInvitation(ctx context.Context, c InvitationCreateCommand) (InvitationMutationResult, error) {
	if s.create == nil {
		return InvitationMutationResult{}, errors.New("unexpected create")
	}
	return s.create(ctx, c)
}
func (s invitationServiceStub) ImportInvitations(ctx context.Context, c InvitationImportCommand) (InvitationMutationResult, error) {
	if s.importCSV == nil {
		return InvitationMutationResult{}, errors.New("unexpected import")
	}
	return s.importCSV(ctx, c)
}
func (s invitationServiceStub) AcceptInvitation(ctx context.Context, c InvitationAcceptCommand) (InvitationMutationResult, error) {
	if s.accept == nil {
		return InvitationMutationResult{}, errors.New("unexpected accept")
	}
	return s.accept(ctx, c)
}

func TestInvitationCreateNormalizesEmailAndValidatesRole(t *testing.T) {
	called := false
	stub := invitationServiceStub{create: func(_ context.Context, c InvitationCreateCommand) (InvitationMutationResult, error) {
		called = true
		if c.NormalizedEmail != "invitee@example.com" || c.Role != "reviewer" || c.ExpiresInDays != 7 {
			t.Fatalf("command=%#v", c)
		}
		return InvitationMutationResult{"invite-1", 1, "pending", apiTestNow}, nil
	}}
	body := `{"request_id":"invite-request-001","email":" INVITEE@Example.com ","role":"reviewer","expires_in_days":7}`
	recorder := serveAuthenticatedHandler(t, Handler{Invitations: stub}, http.MethodPost, "/v1/invitations", "invite-key-000001", "", body)
	if recorder.Code != http.StatusOK || !called || recorder.Header().Get("ETag") != `"1"` {
		t.Fatalf("status=%d called=%v body=%s", recorder.Code, called, recorder.Body.String())
	}
	called = false
	body = `{"request_id":"invite-request-002","email":"person@example.com","role":"owner","expires_in_days":7}`
	recorder = serveAuthenticatedHandler(t, Handler{Invitations: stub}, http.MethodPost, "/v1/invitations", "invite-key-000002", "", body)
	if recorder.Code != http.StatusBadRequest || called {
		t.Fatalf("invalid role status=%d called=%v", recorder.Code, called)
	}
}

func TestInvitationImportRequiresBoundObjectMetadata(t *testing.T) {
	stub := invitationServiceStub{importCSV: func(_ context.Context, c InvitationImportCommand) (InvitationMutationResult, error) {
		if c.ObjectRef != "s3://imports/batch.csv" || c.ContentHash != "sha256:abcdef" || c.ImportKey != "import-key-000001" || c.DefaultRole != "member" {
			t.Fatalf("command=%#v", c)
		}
		return InvitationMutationResult{"import-1", 1, "queued", apiTestNow}, nil
	}}
	body := `{"request_id":"import-request-001","object_ref":"s3://imports/batch.csv","content_hash":"sha256:abcdef","import_key":"import-key-000001","default_role":"member"}`
	recorder := serveAuthenticatedHandler(t, Handler{Invitations: stub}, http.MethodPost, "/v1/invitation-imports", "import-idempotency-key-1", "", body)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status":"queued"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestInvitationAcceptRequiresTokenDecisionAndIfMatch(t *testing.T) {
	token := strings.Repeat("a", 43)
	called := false
	stub := invitationServiceStub{accept: func(_ context.Context, c InvitationAcceptCommand) (InvitationMutationResult, error) {
		called = true
		if c.InvitationID != "invite-123" || c.Token != token || c.ExpectedVersion != 1 {
			t.Fatalf("command=%#v", c)
		}
		return InvitationMutationResult{c.InvitationID, 2, "accepted", apiTestNow}, nil
	}}
	body := `{"request_id":"accept-request-001","token":"` + token + `","decision":"accept"}`
	recorder := serveAuthenticatedHandler(t, Handler{Invitations: stub}, http.MethodPost, "/v1/invitations/invite-123/accept", "accept-key-000001", `"1"`, body)
	if recorder.Code != http.StatusOK || !called || recorder.Header().Get("ETag") != `"2"` {
		t.Fatalf("status=%d called=%v body=%s", recorder.Code, called, recorder.Body.String())
	}
	called = false
	body = `{"request_id":"accept-request-002","token":"` + token + `","decision":"reject"}`
	recorder = serveAuthenticatedHandler(t, Handler{Invitations: stub}, http.MethodPost, "/v1/invitations/invite-123/accept", "accept-key-000002", `"1"`, body)
	if recorder.Code != http.StatusBadRequest || called {
		t.Fatalf("decision status=%d called=%v", recorder.Code, called)
	}
}
