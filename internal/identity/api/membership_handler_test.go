package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

type membershipServiceStub struct {
	list       func(context.Context, MembershipsQuery) (MembershipsPage, error)
	deactivate func(context.Context, MembershipDeactivateCommand) (MembershipMutationResult, error)
	importCSV  func(context.Context, MembershipImportCommand) (MembershipMutationResult, error)
}

func (stub membershipServiceStub) ListMemberships(ctx context.Context, query MembershipsQuery) (MembershipsPage, error) {
	if stub.list == nil {
		return MembershipsPage{}, errors.New("unexpected membership list")
	}
	return stub.list(ctx, query)
}

func (stub membershipServiceStub) DeactivateMembership(ctx context.Context, command MembershipDeactivateCommand) (MembershipMutationResult, error) {
	if stub.deactivate == nil {
		return MembershipMutationResult{}, errors.New("unexpected membership deactivation")
	}
	return stub.deactivate(ctx, command)
}

func (stub membershipServiceStub) ImportMemberships(ctx context.Context, command MembershipImportCommand) (MembershipMutationResult, error) {
	if stub.importCSV == nil {
		return MembershipMutationResult{}, errors.New("unexpected membership import")
	}
	return stub.importCSV(ctx, command)
}

func TestMembershipListUsesTenantAdminContextAndOpaqueCursor(t *testing.T) {
	next := "next.membership.cursor"
	stub := membershipServiceStub{list: func(_ context.Context, query MembershipsQuery) (MembershipsPage, error) {
		if query.UserID != "user-auth" || query.TenantID != "tenant-auth" || query.MembershipID != "membership-auth" || query.Cursor != "signed.cursor" || query.Limit != 50 {
			t.Fatalf("query=%#v", query)
		}
		return MembershipsPage{Items: []MembershipItem{{ID: "member-1", UserID: "user-1", Email: "person@example.com", Role: "reviewer", Status: "active", Version: 3, JoinedAt: apiTestNow.Add(-time.Hour), UpdatedAt: apiTestNow}}, NextCursor: &next}, nil
	}}
	recorder := serveAuthenticatedHandler(t, Handler{Memberships: stub}, http.MethodGet, "/v1/memberships?cursor=signed.cursor", "", "", "")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"email":"person@example.com"`) || !strings.Contains(recorder.Body.String(), `"next_cursor":"next.membership.cursor"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestMembershipDeactivateBindsActionReasonAndIfMatch(t *testing.T) {
	called := false
	stub := membershipServiceStub{deactivate: func(_ context.Context, command MembershipDeactivateCommand) (MembershipMutationResult, error) {
		called = true
		if command.MembershipID != "member-123" || command.Action != "deactivate" || command.Reason != "employment ended" || command.ExpectedVersion != 4 {
			t.Fatalf("command=%#v", command)
		}
		return MembershipMutationResult{ID: command.MembershipID, Version: 5, Status: "suspended", UpdatedAt: apiTestNow}, nil
	}}
	body := `{"request_id":"membership-request-001","action":"deactivate","reason":"employment ended","expected_membership_version":4}`
	recorder := serveAuthenticatedHandler(t, Handler{Memberships: stub}, http.MethodDelete, "/v1/memberships/member-123", "membership-key-000001", `"4"`, body)
	if recorder.Code != http.StatusOK || !called || recorder.Header().Get("ETag") != `"5"` {
		t.Fatalf("status=%d called=%v body=%s", recorder.Code, called, recorder.Body.String())
	}
	called = false
	body = `{"request_id":"membership-request-002","action":"deactivate","reason":"employment ended","expected_membership_version":3}`
	recorder = serveAuthenticatedHandler(t, Handler{Memberships: stub}, http.MethodDelete, "/v1/memberships/member-123", "membership-key-000002", `"4"`, body)
	if recorder.Code != http.StatusBadRequest || called {
		t.Fatalf("mismatched version status=%d called=%v", recorder.Code, called)
	}
}

func TestMembershipImportRequiresBoundObjectAndMode(t *testing.T) {
	stub := membershipServiceStub{importCSV: func(_ context.Context, command MembershipImportCommand) (MembershipMutationResult, error) {
		if command.ObjectRef != "s3://imports/members.csv" || command.ContentHash != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || command.ImportKey != "membership-import-0001" || command.Mode != "deactivate_missing" {
			t.Fatalf("command=%#v", command)
		}
		return MembershipMutationResult{ID: "import-1", Version: 1, Status: "queued", UpdatedAt: apiTestNow}, nil
	}}
	body := `{"request_id":"member-import-request","object_ref":"s3://imports/members.csv","content_hash":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","import_key":"membership-import-0001","mode":"deactivate_missing"}`
	recorder := serveAuthenticatedHandler(t, Handler{Memberships: stub}, http.MethodPost, "/v1/membership-imports", "membership-import-key-1", "", body)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status":"queued"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
