package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type submissionStub func(CreateSubmissionCommand) (SubmissionMutationResult, error)

func (s submissionStub) Create(_ context.Context, c CreateSubmissionCommand) (SubmissionMutationResult, error) {
	return s(c)
}

type reviewStub struct {
	generate func(GenerateReviewCommand) (ReviewGenerationResult, error)
	get      func(ReviewGetQuery) (ReviewResource, error)
}

func (s reviewStub) Generate(_ context.Context, c GenerateReviewCommand) (ReviewGenerationResult, error) {
	return s.generate(c)
}
func (s reviewStub) Get(_ context.Context, q ReviewGetQuery) (ReviewResource, error) { return s.get(q) }

type evidenceStub struct {
	list   func(EvidenceListQuery) (EvidenceListResult, error)
	record func(RecordEvidenceCommand) (EvidenceMutationResult, error)
}

func (s evidenceStub) List(_ context.Context, q EvidenceListQuery) (EvidenceListResult, error) {
	return s.list(q)
}
func (s evidenceStub) Record(_ context.Context, c RecordEvidenceCommand) (EvidenceMutationResult, error) {
	return s.record(c)
}

func TestSubmissionReviewEvidenceBoundaries(t *testing.T) {
	taskID := "ba000000-0000-4000-8000-000000000001"
	submissionID := "ba000000-0000-4000-8000-000000000002"
	rubricID := "ba000000-0000-4000-8000-000000000003"
	reviewID := "ba000000-0000-4000-8000-000000000004"
	missionID := "ba000000-0000-4000-8000-000000000005"
	t.Run("submission cas", func(t *testing.T) {
		service := submissionStub(func(c CreateSubmissionCommand) (SubmissionMutationResult, error) {
			if c.DailyTaskID != taskID || c.ExpectedTaskVersion != 2 || c.SubmissionKind != "code" {
				t.Fatalf("command=%#v", c)
			}
			return SubmissionMutationResult{ID: submissionID, DailyTaskVersion: 3}, nil
		})
		req := authenticatedMissionRequest(t, http.MethodPost, "/v1/submissions", "application/vnd.lites.submission-create.v2+json", `{"request_id":"submission-request-01","daily_task_id":"`+taskID+`","submission_kind":"code","content":"code","understanding":"reason","expected_task_version":2}`, "submission-key-0001", `"2"`, true)
		rr := httptest.NewRecorder()
		req.handler(SubmissionHandler{Service: service}).ServeHTTP(rr, req.request)
		if rr.Code != 200 || rr.Header().Get("ETag") != `"3"` {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	})
	t.Run("review generate and get", func(t *testing.T) {
		service := reviewStub{generate: func(c GenerateReviewCommand) (ReviewGenerationResult, error) {
			if c.SubmissionID != submissionID || c.RubricVersionID != rubricID || c.ExpectedTaskVersion != 3 {
				return ReviewGenerationResult{}, ErrValidation
			}
			return ReviewGenerationResult{RunID: reviewID, Status: "queued"}, nil
		}, get: func(q ReviewGetQuery) (ReviewResource, error) {
			return ReviewResource{ID: reviewID, Version: 1, Review: []byte(`{"schema_version":1}`), ReviewedAt: time.Now()}, nil
		}}
		body := `{"request_id":"review-request-0001","submission_id":"` + submissionID + `","rubric_version_id":"` + rubricID + `","expected_submission_revision":1,"expected_task_version":3}`
		req := authenticatedMissionRequest(t, http.MethodPost, "/v1/reviews", "application/vnd.lites.review-generate.v2+json", body, "review-key-00000001", `"3"`, true)
		rr := httptest.NewRecorder()
		req.handler(ReviewHandler{Service: service}).ServeHTTP(rr, req.request)
		if rr.Code != 202 {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		get := authenticatedMissionRequest(t, http.MethodGet, "/v1/reviews/"+reviewID, "", "", "", "", false)
		rr = httptest.NewRecorder()
		get.handler(ReviewHandler{Service: service}).ServeHTTP(rr, get.request)
		if rr.Code != 200 {
			t.Fatalf("get=%d body=%s", rr.Code, rr.Body.String())
		}
	})
	t.Run("evidence digest", func(t *testing.T) {
		content := "verified report"
		digest := sha256.Sum256([]byte(content))
		service := evidenceStub{list: func(q EvidenceListQuery) (EvidenceListResult, error) { return EvidenceListResult{}, nil }, record: func(c RecordEvidenceCommand) (EvidenceMutationResult, error) {
			if c.ContentHash != hex.EncodeToString(digest[:]) || c.MissionID != missionID {
				t.Fatalf("command=%#v", c)
			}
			return EvidenceMutationResult{ID: reviewID, Version: 1, Status: "recorded"}, nil
		}}
		body := `{"request_id":"evidence-request-01","mission_id":"` + missionID + `","evidence_type":"test_report","source_kind":"test_result","source_id":null,"content":"` + content + `","content_hash":"` + hex.EncodeToString(digest[:]) + `"}`
		req := authenticatedMissionRequest(t, http.MethodPost, "/v1/capability-evidence", "application/vnd.lites.evidence-record.v2+json", body, "evidence-key-0001", "", true)
		rr := httptest.NewRecorder()
		req.handler(EvidenceHandler{Service: service}).ServeHTTP(rr, req.request)
		if rr.Code != 200 {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	})
}
