package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type artifactServiceStub struct {
	create func(CreateArtifactCommand) (ArtifactMutationResult, error)
	revise func(CreateArtifactRevisionCommand) (ArtifactRevisionResult, error)
}

func (stub artifactServiceStub) Create(_ context.Context, command CreateArtifactCommand) (ArtifactMutationResult, error) {
	return stub.create(command)
}

func (stub artifactServiceStub) CreateRevision(_ context.Context, command CreateArtifactRevisionCommand) (ArtifactRevisionResult, error) {
	return stub.revise(command)
}

func TestArtifactHandlerCreatesArtifactAndExactEvidenceBoundRevision(t *testing.T) {
	projectID := "a1000000-0000-4000-8000-000000000001"
	artifactID := "a1000000-0000-4000-8000-000000000002"
	revisionID := "a1000000-0000-4000-8000-000000000003"
	evidenceID := "a1000000-0000-4000-8000-000000000004"
	now := time.Date(2026, time.July, 17, 11, 0, 0, 0, time.UTC)
	handler := ArtifactHandler{Service: artifactServiceStub{
		create: func(command CreateArtifactCommand) (ArtifactMutationResult, error) {
			if command.ProjectID != projectID || command.ArtifactKind != "code" || command.Title != "Recovery service" || command.IdempotencyKey != "artifact-key-00000001" {
				t.Fatalf("create command=%#v", command)
			}
			return ArtifactMutationResult{ID: artifactID, Version: 1, Status: "draft", CurrentRevision: 0, UpdatedAt: now}, nil
		},
		revise: func(command CreateArtifactRevisionCommand) (ArtifactRevisionResult, error) {
			if command.ArtifactID != artifactID || command.Content != "# Exact revision" || command.MediaType != "text/markdown" || command.WorkspaceRevision != "inline:sha256:abc" || command.ExpectedArtifactVersion != 1 || len(command.EvidenceIDs) != 1 || command.EvidenceIDs[0] != evidenceID {
				t.Fatalf("revision command=%#v", command)
			}
			return ArtifactRevisionResult{ID: revisionID, ArtifactID: artifactID, ArtifactVersion: 2, Revision: 1, Status: "ready", ContentHash: "content-hash", EvidenceManifestHash: "manifest-hash", UpdatedAt: now}, nil
		},
	}}

	request := authenticatedMissionRequest(t, http.MethodPost, "/v1/artifacts", artifactCreateMediaType, `{"request_id":"artifact-client-0001","project_id":"`+projectID+`","artifact_kind":"code","title":"Recovery service"}`, "artifact-key-00000001", "", true)
	response := httptest.NewRecorder()
	request.handler(handler).ServeHTTP(response, request.request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("create status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}

	request = authenticatedMissionRequest(t, http.MethodPost, "/v1/artifacts/"+artifactID+"/revisions", artifactRevisionMediaType, `{"request_id":"artifact-revision-client-0001","content":"# Exact revision","media_type":"text/markdown","workspace_revision":"inline:sha256:abc","evidence_ids":["`+evidenceID+`"],"expected_artifact_version":1}`, "artifact-revision-key-0001", `"1"`, true)
	response = httptest.NewRecorder()
	request.handler(handler).ServeHTTP(response, request.request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("revision status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestArtifactHandlerRejectsUnscopedOrStaleRevision(t *testing.T) {
	artifactID := "a2000000-0000-4000-8000-000000000002"
	evidenceID := "a2000000-0000-4000-8000-000000000004"
	service := artifactServiceStub{revise: func(CreateArtifactRevisionCommand) (ArtifactRevisionResult, error) {
		t.Fatal("service must not be called")
		return ArtifactRevisionResult{}, nil
	}}
	tests := []struct {
		name, media, body, match string
		want                     int
	}{
		{"wrong media", "application/json", `{}`, `"1"`, http.StatusUnsupportedMediaType},
		{"stale version", artifactRevisionMediaType, `{"request_id":"artifact-revision-client-0001","content":"x","media_type":"text/plain","workspace_revision":"inline:sha256:abc","evidence_ids":["` + evidenceID + `"],"expected_artifact_version":2}`, `"1"`, http.StatusConflict},
		{"missing evidence", artifactRevisionMediaType, `{"request_id":"artifact-revision-client-0001","content":"x","media_type":"text/plain","workspace_revision":"inline:sha256:abc","evidence_ids":[],"expected_artifact_version":1}`, `"1"`, http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := authenticatedMissionRequest(t, http.MethodPost, "/v1/artifacts/"+artifactID+"/revisions", test.media, test.body, "artifact-revision-key-0001", test.match, true)
			response := httptest.NewRecorder()
			request.handler(ArtifactHandler{Service: service}).ServeHTTP(response, request.request)
			if response.Code != test.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestArtifactHandlerRejectsOversizedRevisionAsPayloadTooLarge(t *testing.T) {
	artifactID := "a3000000-0000-4000-8000-000000000002"
	request := authenticatedMissionRequest(t, http.MethodPost, "/v1/artifacts/"+artifactID+"/revisions", artifactRevisionMediaType, `{"request_id":"artifact-revision-client-0001","content":"`+strings.Repeat("x", maximumArtifactBodyBytes)+`","media_type":"text/plain","workspace_revision":"inline:sha256:abc","evidence_ids":["a3000000-0000-4000-8000-000000000004"],"expected_artifact_version":1}`, "artifact-revision-key-0001", `"1"`, true)
	response := httptest.NewRecorder()
	request.handler(ArtifactHandler{Service: artifactServiceStub{}}).ServeHTTP(response, request.request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
