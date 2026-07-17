package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
)

type enterpriseAdminStub struct {
	createProgram   func(CreateProgramCommand) (EnterpriseResource, error)
	updateProgram   func(UpdateProgramCommand) (EnterpriseResource, error)
	createCohort    func(CreateCohortCommand) (EnterpriseResource, error)
	enrollCohort    func(EnrollCohortCommand) (EnterpriseResource, error)
	unenrollCohort  func(UnenrollCohortCommand) (EnterpriseResource, error)
	publishRolePack func(PublishRolePackCommand) (EnterpriseResource, error)
	publishTaskPack func(PublishTaskPackCommand) (EnterpriseResource, error)
}

func (stub enterpriseAdminStub) CreateProgram(_ context.Context, command CreateProgramCommand) (EnterpriseResource, error) {
	return stub.createProgram(command)
}
func (stub enterpriseAdminStub) UpdateProgram(_ context.Context, command UpdateProgramCommand) (EnterpriseResource, error) {
	return stub.updateProgram(command)
}
func (stub enterpriseAdminStub) CreateCohort(_ context.Context, command CreateCohortCommand) (EnterpriseResource, error) {
	return stub.createCohort(command)
}
func (stub enterpriseAdminStub) EnrollCohort(_ context.Context, command EnrollCohortCommand) (EnterpriseResource, error) {
	return stub.enrollCohort(command)
}
func (stub enterpriseAdminStub) UnenrollCohort(_ context.Context, command UnenrollCohortCommand) (EnterpriseResource, error) {
	return stub.unenrollCohort(command)
}
func (stub enterpriseAdminStub) PublishRolePack(_ context.Context, command PublishRolePackCommand) (EnterpriseResource, error) {
	return stub.publishRolePack(command)
}
func (stub enterpriseAdminStub) PublishTaskPack(_ context.Context, command PublishTaskPackCommand) (EnterpriseResource, error) {
	return stub.publishTaskPack(command)
}

func TestEnterpriseAdminCreateProgramUsesStrictV2Boundary(t *testing.T) {
	programID := "c4000000-0000-4000-8000-000000000040"
	service := enterpriseAdminStub{createProgram: func(command CreateProgramCommand) (EnterpriseResource, error) {
		if command.Name != "Engineering Mobility" || string(command.Settings) != `{"locale":"en"}` || command.IdempotencyKey != "enterprise-program-key-0001" || command.ClientRequestID != "enterprise-client-0001" {
			t.Fatalf("command=%#v settings=%s", command, command.Settings)
		}
		return EnterpriseResource{ID: programID, Version: 1, Status: "active", UpdatedAt: missionAPINow}, nil
	}}
	request := authenticatedEnterpriseRequest(t, "program_manager", http.MethodPost, "/v1/admin/programs", "application/vnd.lites.program-create.v2+json", `{"request_id":"enterprise-client-0001","name":" Engineering Mobility ","settings":{"locale":"en"}}`, "enterprise-program-key-0001", "", true)
	recorder := httptest.NewRecorder()
	request.handler(EnterpriseAdminHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != enterpriseResourceMediaType || recorder.Header().Get("ETag") != `"1"` || recorder.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestEnterpriseAdminLifecycleRoutesCarryExactCAS(t *testing.T) {
	programID := "c4000000-0000-4000-8000-000000000041"
	cohortID := "c4000000-0000-4000-8000-000000000042"
	memberID := "c4000000-0000-4000-8000-000000000043"
	roleID := "c4000000-0000-4000-8000-000000000044"
	taskID := "c4000000-0000-4000-8000-000000000045"
	service := enterpriseAdminStub{
		updateProgram: func(command UpdateProgramCommand) (EnterpriseResource, error) {
			if command.ProgramID != programID || command.ExpectedVersion != 3 || command.Status != "paused" || command.Name != "Mobility" {
				t.Fatalf("update=%#v", command)
			}
			return EnterpriseResource{ID: programID, Version: 4, Status: "paused", UpdatedAt: missionAPINow}, nil
		},
		createCohort: func(command CreateCohortCommand) (EnterpriseResource, error) {
			if command.ProgramID != programID || command.Name != "July 2026" || command.StartsAt == nil || command.EndsAt == nil {
				t.Fatalf("cohort=%#v", command)
			}
			return EnterpriseResource{ID: cohortID, Version: 1, Status: "scheduled", UpdatedAt: missionAPINow}, nil
		},
		enrollCohort: func(command EnrollCohortCommand) (EnterpriseResource, error) {
			if command.CohortID != cohortID || command.ExpectedVersion != 1 || len(command.UserIDs) != 1 || command.UserIDs[0] != memberID {
				t.Fatalf("enroll=%#v", command)
			}
			return EnterpriseResource{ID: cohortID, Version: 2, Status: "scheduled", UpdatedAt: missionAPINow}, nil
		},
		unenrollCohort: func(command UnenrollCohortCommand) (EnterpriseResource, error) {
			if command.CohortID != cohortID || command.TargetUserID != memberID || command.ExpectedVersion != 2 || command.Reason != "Transferred team" {
				t.Fatalf("unenroll=%#v", command)
			}
			return EnterpriseResource{ID: cohortID, Version: 3, Status: "scheduled", UpdatedAt: missionAPINow}, nil
		},
		publishRolePack: func(command PublishRolePackCommand) (EnterpriseResource, error) {
			if command.ProgramID != programID || command.Revision != 2 || len(command.RoleProfileIDs) != 1 || command.RoleProfileIDs[0] != roleID || len(command.TaskTemplateIDs) != 1 || command.TaskTemplateIDs[0] != taskID {
				t.Fatalf("role pack=%#v", command)
			}
			return EnterpriseResource{ID: "c4000000-0000-4000-8000-000000000046", Version: 1, Status: "published", UpdatedAt: missionAPINow}, nil
		},
		publishTaskPack: func(command PublishTaskPackCommand) (EnterpriseResource, error) {
			if command.ProgramID != programID || command.Revision != 3 || command.Name != "Crash recovery lab" || len(command.TaskTemplateIDs) != 1 || command.TaskTemplateIDs[0] != taskID || string(command.Assignment) != `{"required":true}` {
				t.Fatalf("task pack=%#v", command)
			}
			return EnterpriseResource{ID: "c4000000-0000-4000-8000-000000000047", Version: 1, Status: "published", UpdatedAt: missionAPINow}, nil
		},
	}
	tests := []struct {
		name, method, path, media, body, etag string
		wantVersion                           string
	}{
		{"update program", http.MethodPatch, "/v1/admin/programs/" + programID, "application/vnd.lites.program-update.v2+json", `{"request_id":"enterprise-client-0002","name":"Mobility","status":"paused","settings":{},"expected_program_version":3}`, `"3"`, `"4"`},
		{"create cohort", http.MethodPost, "/v1/admin/cohorts", "application/vnd.lites.cohort-create.v2+json", `{"request_id":"enterprise-client-0003","program_id":"` + programID + `","name":"July 2026","starts_at":"2026-07-20T00:00:00Z","ends_at":"2026-08-20T00:00:00Z"}`, "", `"1"`},
		{"enroll", http.MethodPost, "/v1/admin/cohorts/" + cohortID + "/enrollments", "application/vnd.lites.cohort-enroll.v2+json", `{"request_id":"enterprise-client-0004","user_ids":["` + memberID + `"],"expected_cohort_version":1}`, `"1"`, `"2"`},
		{"unenroll", http.MethodDelete, "/v1/admin/cohorts/" + cohortID + "/enrollments/" + memberID, "application/vnd.lites.cohort-unenroll.v2+json", `{"request_id":"enterprise-client-0005","reason":" Transferred team ","expected_cohort_version":2}`, `"2"`, `"3"`},
		{"publish role pack", http.MethodPost, "/v1/admin/role-packs", "application/vnd.lites.role-pack-publish.v2+json", `{"request_id":"enterprise-client-0006","program_id":"` + programID + `","revision":2,"role_profile_ids":["` + roleID + `"],"task_template_ids":["` + taskID + `"]}`, "", `"1"`},
		{"publish task pack", http.MethodPost, "/v1/admin/task-packs", "application/vnd.lites.task-pack-publish.v2+json", `{"request_id":"enterprise-client-0009","program_id":"` + programID + `","revision":3,"name":" Crash recovery lab ","task_template_ids":["` + taskID + `"],"assignment":{"required":true}}`, "", `"1"`},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := authenticatedEnterpriseRequest(t, "admin", test.method, test.path, test.media, test.body, "enterprise-lifecycle-key-000"+string(rune('1'+index)), test.etag, true)
			recorder := httptest.NewRecorder()
			request.handler(EnterpriseAdminHandler{Service: service}).ServeHTTP(recorder, request.request)
			if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != test.wantVersion {
				t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
			}
		})
	}
}

func TestEnterpriseAdminRejectsMemberMissingCSRFAndStaleCAS(t *testing.T) {
	service := enterpriseAdminStub{createProgram: func(CreateProgramCommand) (EnterpriseResource, error) {
		t.Fatal("service called")
		return EnterpriseResource{}, nil
	}, updateProgram: func(UpdateProgramCommand) (EnterpriseResource, error) {
		t.Fatal("service called")
		return EnterpriseResource{}, nil
	}}
	body := `{"request_id":"enterprise-client-0007","name":"Program","settings":{}}`
	tests := []struct {
		name, role, media string
		csrf              bool
		want              int
	}{
		{"member", "member", "application/vnd.lites.program-create.v2+json", true, http.StatusForbidden},
		{"missing csrf", "owner", "application/vnd.lites.program-create.v2+json", false, http.StatusUnauthorized},
		{"wrong media", "owner", "application/json", true, http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := authenticatedEnterpriseRequest(t, test.role, http.MethodPost, "/v1/admin/programs", test.media, body, "enterprise-reject-key-0001", "", test.csrf)
			recorder := httptest.NewRecorder()
			request.handler(EnterpriseAdminHandler{Service: service}).ServeHTTP(recorder, request.request)
			if recorder.Code != test.want {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
	programID := "c4000000-0000-4000-8000-000000000041"
	request := authenticatedEnterpriseRequest(t, "owner", http.MethodPatch, "/v1/admin/programs/"+programID, "application/vnd.lites.program-update.v2+json", `{"request_id":"enterprise-client-0008","name":"Program","status":"active","settings":{},"expected_program_version":2}`, "enterprise-reject-key-0002", `"1"`, true)
	recorder := httptest.NewRecorder()
	request.handler(EnterpriseAdminHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("cas status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func authenticatedEnterpriseRequest(t *testing.T, role, method, target, contentType, body, key, etag string, csrf bool) signedMissionRequest {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", contentType)
	request.Header.Set(transport.IdempotencyHeader, key)
	request.Header.Set("If-Match", etag)
	request.Header.Set(transport.RequestIDHeader, "c4000000-0000-4000-8000-000000000014")
	fingerprint := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x43}, 32))
	claims := trustedcontext.Claims{PrincipalKind: trustedcontext.AuthenticatedUser, Issuer: "gateway", Audience: "product-service", SubjectID: "c4000000-0000-4000-8000-000000000010", TenantID: "c4000000-0000-4000-8000-000000000011", MembershipID: "c4000000-0000-4000-8000-000000000012", SessionID: "c4000000-0000-4000-8000-000000000013", Roles: []string{role}, RequestID: "c4000000-0000-4000-8000-000000000014", RequestMethod: method, RequestTarget: target, ClientIPHash: fingerprint, UserAgentHash: fingerprint, CSRFVerified: csrf, IssuedAt: missionAPINow.Unix(), ExpiresAt: missionAPINow.Add(time.Minute).Unix(), Nonce: "enterprise-nonce"}
	token, err := trustedcontext.Sign(claims, "key-1", privateKey, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(transport.TrustedContextHeader, token)
	return signedMissionRequest{request: request, keys: map[string]ed25519.PublicKey{"key-1": publicKey}}
}
