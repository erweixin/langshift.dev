package postgres

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/langshift/lites/internal/behavior"
)

func TestAcceptMessageRunValidationAndIdentifiers(t *testing.T) {
	actor := json.RawMessage(`{"kind":"user"}`)
	command := AcceptMessageRunCommand{
		Run: AcceptRunCommand{
			RunID: "10000000-0000-4000-8000-000000000001", TenantID: "20000000-0000-4000-8000-000000000001", UserID: "30000000-0000-4000-8000-000000000001", ConversationID: "40000000-0000-4000-8000-000000000001", CorrelationID: "50000000-0000-4000-8000-000000000001",
			DueAt: time.Now().Add(time.Hour), BehaviorProfile: behavior.Coach, BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_steps":32}`), Actor: actor,
			AcceptedEvent: pointerWithHash("accepted"), QueuedEvent: pointerWithHash("queued"), StartCommand: pointerWithHash("start"),
			QueueClass: "interactive", ResourceClass: "llm", Priority: 100, CostUnits: 1, MaxAttempts: 5,
		},
		MessageID: "60000000-0000-4000-8000-000000000001", ExpectedConversationVersion: 1, ExpectedConversationMode: "coach", ExpectedConversationProfile: behavior.Coach,
		Message: pointerWithHash("message"), ContentHash: hashValue("content"), AppendedEvent: pointerWithHash("appended"), Actor: actor,
	}
	if !validAcceptMessageRun(command) {
		t.Fatal("valid Message-to-Run admission rejected")
	}
	store := RunStore{IDKey: bytes.Repeat([]byte{0x74}, 32)}
	first, err := store.conversationMessageIdentifiers(command.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.conversationMessageIdentifiers(command.MessageID)
	if err != nil || first != second || first.event == first.publishOutbox || first.publishOutbox == first.publishCommand {
		t.Fatalf("message identifiers first=%#v second=%#v error=%v", first, second, err)
	}
	command.ExpectedConversationVersion = 0
	if validAcceptMessageRun(command) {
		t.Fatal("zero Conversation version accepted")
	}
}

func TestConversationModesSelectOnlyTheirProductionProfile(t *testing.T) {
	tests := map[string]behavior.Profile{"coach": behavior.Coach, "task": behavior.DailyPlanner, "project": behavior.ArtifactBuilder}
	for mode, expected := range tests {
		profile, ok := behaviorForConversationMode(mode)
		if !ok || profile != expected {
			t.Fatalf("mode=%s profile=%s valid=%t", mode, profile, ok)
		}
	}
	if profile, ok := behaviorForConversationMode("route_planner"); ok || profile != "" {
		t.Fatalf("unsupported public mode selected profile=%s valid=%t", profile, ok)
	}
}

func pointerWithHash(name string) PayloadPointer {
	return PayloadPointer{Ref: "encrypted://test/" + name, Hash: hashValue(name)}
}

func hashValue(value string) string {
	const digits = "0123456789abcdef"
	result := make([]byte, 64)
	for index := range result {
		result[index] = digits[(index+len(value))%len(digits)]
	}
	return string(result)
}
