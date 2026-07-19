package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type reminderServiceStub struct {
	list   func(ReminderListQuery) (ReminderListResult, error)
	create func(CreateReminderCommand) (ReminderResource, error)
	update func(UpdateReminderCommand) (ReminderResource, error)
}

func (stub reminderServiceStub) List(_ context.Context, query ReminderListQuery) (ReminderListResult, error) {
	return stub.list(query)
}

func (stub reminderServiceStub) Create(_ context.Context, command CreateReminderCommand) (ReminderResource, error) {
	return stub.create(command)
}

func (stub reminderServiceStub) Update(_ context.Context, command UpdateReminderCommand) (ReminderResource, error) {
	return stub.update(command)
}

func TestReminderHandlerCreateListAndCASUpdate(t *testing.T) {
	id := "bb000000-0000-4000-8000-000000000001"
	next := time.Date(2026, time.July, 20, 6, 30, 0, 0, time.UTC)
	service := reminderServiceStub{
		list: func(query ReminderListQuery) (ReminderListResult, error) {
			if query.TenantID == "" || query.UserID == "" {
				t.Fatalf("query=%#v", query)
			}
			return ReminderListResult{Items: []ReminderResource{}}, nil
		},
		create: func(command CreateReminderCommand) (ReminderResource, error) {
			if command.Timezone != "America/New_York" || command.LocalTime != "02:30" || command.Channel != "push" || len(command.Weekdays) != 3 || command.IdempotencyKey != "reminder-create-key-01" {
				t.Fatalf("command=%#v", command)
			}
			return ReminderResource{ID: id, Version: 1, Status: "active", NextOccurrenceAt: &next}, nil
		},
		update: func(command UpdateReminderCommand) (ReminderResource, error) {
			if command.ReminderID != id || command.Action != "pause" || command.OperationID != "reminders.update.v2" || command.ExpectedVersion != 1 || command.IdempotencyKey != "reminder-pause-key-001" {
				t.Fatalf("command=%#v", command)
			}
			return ReminderResource{ID: id, Version: 2, Status: "paused"}, nil
		},
	}

	list := authenticatedMissionRequest(t, http.MethodGet, "/v1/reminder-schedules", "", "", "", "", false)
	listRecorder := httptest.NewRecorder()
	list.handler(ReminderHandler{Service: service}).ServeHTTP(listRecorder, list.request)
	if listRecorder.Code != http.StatusOK || listRecorder.Header().Get("Content-Type") != "application/vnd.lites.reminder-list.v2+json" {
		t.Fatalf("list status=%d body=%s", listRecorder.Code, listRecorder.Body.String())
	}

	create := authenticatedMissionRequest(t, http.MethodPost, "/v1/reminder-schedules", "application/vnd.lites.reminder-create.v2+json", `{"request_id":"reminder-create-request-01","timezone":"America/New_York","local_time":"02:30","weekdays":[1,3,7],"channel":"push"}`, "reminder-create-key-01", "", true)
	createRecorder := httptest.NewRecorder()
	create.handler(ReminderHandler{Service: service}).ServeHTTP(createRecorder, create.request)
	if createRecorder.Code != http.StatusCreated || createRecorder.Header().Get("ETag") != `"1"` {
		t.Fatalf("create status=%d body=%s", createRecorder.Code, createRecorder.Body.String())
	}

	pause := authenticatedMissionRequest(t, http.MethodPatch, "/v1/reminder-schedules/"+id, "application/vnd.lites.reminder-update.v2+json", `{"request_id":"reminder-pause-request-01","action":"pause","expected_schedule_version":1,"timezone":"","local_time":"","weekdays":[],"channel":""}`, "reminder-pause-key-001", `"1"`, true)
	pauseRecorder := httptest.NewRecorder()
	pause.handler(ReminderHandler{Service: service}).ServeHTTP(pauseRecorder, pause.request)
	if pauseRecorder.Code != http.StatusOK || pauseRecorder.Header().Get("ETag") != `"2"` {
		t.Fatalf("pause status=%d body=%s", pauseRecorder.Code, pauseRecorder.Body.String())
	}
}

func TestReminderHandlerDeleteUsesDedicatedCancellationScopeAndReason(t *testing.T) {
	id := "bb000000-0000-4000-8000-000000000011"
	service := reminderServiceStub{update: func(command UpdateReminderCommand) (ReminderResource, error) {
		if command.ReminderID != id || command.Action != "cancel" || command.OperationID != "reminders.cancel" || command.Reason != "Career plan completed" || command.ExpectedVersion != 4 || command.IdempotencyKey != "reminder-cancel-key-01" {
			t.Fatalf("command=%#v", command)
		}
		return ReminderResource{ID: id, Version: 5, Status: "cancelled", UpdatedAt: time.Now().UTC()}, nil
	}}
	request := authenticatedMissionRequest(t, http.MethodDelete, "/v1/reminder-schedules/"+id, "application/json", `{"request_id":"reminder-cancel-request-01","reason":" Career plan completed ","expected_schedule_version":4}`, "reminder-cancel-key-01", `"4"`, true)
	recorder := httptest.NewRecorder()
	request.handler(ReminderHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "application/json" || recorder.Header().Get("ETag") != `"5"` {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestReminderHandlerRejectsTrailingJSONAndLookalikePath(t *testing.T) {
	called := false
	service := reminderServiceStub{create: func(command CreateReminderCommand) (ReminderResource, error) {
		called = true
		return ReminderResource{}, nil
	}}
	request := authenticatedMissionRequest(t, http.MethodPost, "/v1/reminder-schedules", "application/vnd.lites.reminder-create.v2+json", `{"request_id":"reminder-create-request-02","timezone":"UTC","local_time":"09:00","weekdays":[1],"channel":"email"} trailing`, "reminder-create-key-02", "", true)
	recorder := httptest.NewRecorder()
	request.handler(ReminderHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusBadRequest || called {
		t.Fatalf("status=%d called=%v body=%s", recorder.Code, called, recorder.Body.String())
	}

	lookalike := authenticatedMissionRequest(t, http.MethodGet, "/v1/reminder-schedules/not-a-uuid", "", "", "", "", false)
	recorder = httptest.NewRecorder()
	lookalike.handler(ReminderHandler{Service: service}).ServeHTTP(recorder, lookalike.request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("lookalike status=%d", recorder.Code)
	}
}
