package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type dailyTaskServiceStub struct {
	list   func(DailyTaskListQuery) (DailyTaskListResult, error)
	update func(UpdateDailyTaskCommand) (DailyTaskMutationResult, error)
}

func (stub dailyTaskServiceStub) List(_ context.Context, query DailyTaskListQuery) (DailyTaskListResult, error) {
	return stub.list(query)
}
func (stub dailyTaskServiceStub) Update(_ context.Context, command UpdateDailyTaskCommand) (DailyTaskMutationResult, error) {
	return stub.update(command)
}

func TestDailyTaskListUsesOwnerScope(t *testing.T) {
	service := dailyTaskServiceStub{list: func(query DailyTaskListQuery) (DailyTaskListResult, error) {
		if query.TenantID == "" || query.UserID == "" || query.Cursor != "signed" {
			t.Fatalf("query=%#v", query)
		}
		return DailyTaskListResult{}, nil
	}}
	request := authenticatedMissionRequest(t, http.MethodGet, "/v1/daily-tasks?cursor=signed", "", "", "", "", false)
	recorder := httptest.NewRecorder()
	request.handler(DailyTaskHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "application/vnd.lites.daily-tasks.v2+json" {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestDailyTaskUpdateRequiresCASAndCSRF(t *testing.T) {
	taskID := "b9000000-0000-4000-8000-000000000001"
	service := dailyTaskServiceStub{update: func(command UpdateDailyTaskCommand) (DailyTaskMutationResult, error) {
		if command.TaskID != taskID || command.Action != "start" || command.ExpectedTaskVersion != 1 || command.IdempotencyKey != "daily-task-key-0001" {
			t.Fatalf("command=%#v", command)
		}
		return DailyTaskMutationResult{ID: taskID, Version: 2, Status: "in_progress"}, nil
	}}
	request := authenticatedMissionRequest(t, http.MethodPatch, "/v1/daily-tasks/"+taskID, "application/vnd.lites.daily-task-update.v2+json", `{"request_id":"daily-task-start-0001","action":"start","expected_task_version":1,"reschedule_for":""}`, "daily-task-key-0001", `"1"`, true)
	recorder := httptest.NewRecorder()
	request.handler(DailyTaskHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"2"` {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestDailyTaskAllowsAuditedDifficultyReduction(t *testing.T) {
	taskID := "b9000000-0000-4000-8000-000000000002"
	service := dailyTaskServiceStub{update: func(command UpdateDailyTaskCommand) (DailyTaskMutationResult, error) {
		if command.TaskID != taskID || command.Action != "lower_difficulty" || command.ExpectedTaskVersion != 3 || command.RescheduleFor != "" {
			t.Fatalf("command=%#v", command)
		}
		return DailyTaskMutationResult{ID: taskID, Version: 4, Status: "in_progress"}, nil
	}}
	request := authenticatedMissionRequest(t, http.MethodPatch, "/v1/daily-tasks/"+taskID, "application/vnd.lites.daily-task-update.v2+json", `{"request_id":"daily-task-easier-0001","action":"lower_difficulty","expected_task_version":3,"reschedule_for":""}`, "daily-task-key-0002", `"3"`, true)
	recorder := httptest.NewRecorder()
	request.handler(DailyTaskHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"4"` {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
