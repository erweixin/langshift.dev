package run

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAcceptedEventBuildsRunPayload(t *testing.T) {
	dueAt := time.Date(2026, 7, 6, 1, 2, 3, 0, time.UTC)
	draft, err := AcceptedEvent(AcceptedEventRequest{
		RunID:    "run_1",
		RunType:  "outline_gen",
		InputRef: json.RawMessage(`{"goal":"agent"}`),
		DueAt:    &dueAt,
	})
	if err != nil {
		t.Fatalf("accepted event: %v", err)
	}
	if draft.Type != EventRunAccepted || draft.SchemaVersion != 1 || draft.RunID != "run_1" {
		t.Fatalf("draft = %+v", draft)
	}

	var payload acceptedPayload
	if err := json.Unmarshal(draft.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.RunID != "run_1" || payload.RunType != "outline_gen" || payload.DueAt == nil || !payload.DueAt.Equal(dueAt) {
		t.Fatalf("payload = %+v", payload)
	}
	if string(payload.InputRef) != `{"goal":"agent"}` {
		t.Fatalf("input_ref = %s", payload.InputRef)
	}
}

func TestFailedEventMergesCommonAndBusinessFields(t *testing.T) {
	draft, err := FailedEvent("run_1", []string{"attempt_1"}, "invalid_output", "bad json", Fields{
		"llm_ledger_id": "ledger_1",
	})
	if err != nil {
		t.Fatalf("failed event: %v", err)
	}
	if draft.Type != EventRunFailed || draft.RunID != "run_1" {
		t.Fatalf("draft = %+v", draft)
	}

	var payload map[string]any
	if err := json.Unmarshal(draft.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["run_id"] != "run_1" || payload["llm_ledger_id"] != "ledger_1" {
		t.Fatalf("payload = %+v", payload)
	}
	errorPayload, ok := payload["error"].(map[string]any)
	if !ok || errorPayload["code"] != "invalid_output" || errorPayload["message"] != "bad json" {
		t.Fatalf("error payload = %+v", payload["error"])
	}
}
