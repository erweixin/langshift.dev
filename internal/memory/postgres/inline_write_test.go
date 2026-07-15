package postgres

import (
	"bytes"
	"testing"

	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
)

func TestMemoryWriteValidationAndIdentifiers(t *testing.T) {
	write := MemoryWrite{
		MemoryID: "memory", ScopeKind: "user", ScopeID: "user", MemoryKind: "preference",
		ContentRef: "encrypted://memory", ContentType: "text", SensitivityLabels: []string{"private"},
		SourceKind: "user_stated", Sources: []SourceReference{{Kind: "user_statement", Ref: "event:1", Version: 1}},
		DataSubjectIDs: []string{"user"}, DerivationKind: "direct", Confidence: 1,
		EncryptionSubjectID: "user", KeyRef: "vault://memory/user", EmbeddingModelID: "embedding-model",
		EmbeddingModelVersion: "2026-07-01", VectorDimensions: 1536, Tags: []string{"preference"},
		PolicyVersion: 1, IndexGeneration: 1, GuardrailSnapshotID: "guardrail:1",
		UpsertedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://event", Hash: "event-hash"},
	}
	handler := InlineWriteHandler{IDKey: bytes.Repeat([]byte{0x71}, 32)}
	if !handler.Validate(write) {
		t.Fatal("valid memory write rejected")
	}
	duplicateSubject := write
	duplicateSubject.DataSubjectIDs = []string{"user", "user"}
	if handler.Validate(duplicateSubject) {
		t.Fatal("duplicate data subject lineage accepted")
	}
	selfDerived := write
	selfDerived.DerivedFrom = []RevisionReference{{MemoryID: "memory", Version: 1}}
	if handler.Validate(selfDerived) {
		t.Fatal("self-derived revision accepted")
	}
	first, err := handler.eventIDs("memory", 1, "tool-event")
	if err != nil {
		t.Fatal(err)
	}
	second, err := handler.eventIDs("memory", 1, "tool-event")
	if err != nil || first != second || first.event == first.outbox || first.outbox == first.publish {
		t.Fatalf("unstable or colliding event identifiers: %#v %#v error=%v", first, second, err)
	}
}
