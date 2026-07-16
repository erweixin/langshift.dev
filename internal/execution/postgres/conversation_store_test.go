package postgres

import (
	"encoding/json"
	"testing"
)

func TestConversationAdmissionValidationAndIdentifiers(t *testing.T) {
	title := "Career transition"
	command := CreateConversationCommand{
		ConversationID: "10000000-0000-4000-8000-000000000001",
		TenantID:       "20000000-0000-4000-8000-000000000001",
		UserID:         "30000000-0000-4000-8000-000000000001",
		MissionID:      "40000000-0000-4000-8000-000000000001",
		Title:          &title,
		Mode:           "coach",
		CorrelationID:  "50000000-0000-4000-8000-000000000001",
		Actor:          json.RawMessage(`{"kind":"user"}`),
		CreatedEvent:   PayloadPointer{Ref: "encrypted://conversation/created", Hash: "created-hash"},
	}
	if !validCreateConversation(command) {
		t.Fatal("valid conversation admission rejected")
	}
	store := RunStore{IDKey: []byte("0123456789abcdef0123456789abcdef")}
	first, err := store.conversationIdentifiers(command.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.conversationIdentifiers(command.ConversationID)
	if err != nil || first != second || first.event == first.publishOutbox || first.publishOutbox == first.publishCommand {
		t.Fatalf("non-deterministic or colliding identifiers first=%#v second=%#v err=%v", first, second, err)
	}
	command.Mode = "unsupported"
	if validCreateConversation(command) {
		t.Fatal("unsupported conversation mode accepted")
	}
}
