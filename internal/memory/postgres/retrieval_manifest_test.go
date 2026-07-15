package postgres

import (
	"bytes"
	"testing"
)

func TestRetrievalManifestValidationAndIdentifiers(t *testing.T) {
	manifest := RetrievalManifest{
		QueryHMAC: [32]byte{1}, RetrievalModelID: "hybrid", RetrievalModelVersion: "v1",
		PolicySnapshotID: "policy", GuardrailSnapshotID: "guardrail", IndexGeneration: 2, TokenBudget: 1024,
		Chunks:         []RetrievalChunk{{MemoryID: "memory", MemoryVersion: 3, IndexGeneration: 2, ChunkID: "chunk", ContentRef: "encrypted://chunk", ContentHMAC: [32]byte{2}, Score: 0.75, TrustLabel: "derived"}},
		CommittedEvent: EventPointer{Ref: "encrypted://retrieval-event", Hash: "retrieval-event"},
	}
	handler := RetrievalManifestHandler{IDKey: bytes.Repeat([]byte{0x81}, 32)}
	if !handler.Validate(manifest) {
		t.Fatal("valid retrieval manifest rejected")
	}
	duplicate := manifest
	duplicate.Chunks = append(append([]RetrievalChunk(nil), manifest.Chunks...), manifest.Chunks[0])
	if handler.Validate(duplicate) {
		t.Fatal("duplicate retrieval chunk accepted")
	}
	wrongGeneration := manifest
	wrongGeneration.Chunks = append([]RetrievalChunk(nil), manifest.Chunks...)
	wrongGeneration.Chunks[0].IndexGeneration++
	if handler.Validate(wrongGeneration) {
		t.Fatal("mixed index generation accepted")
	}
	first, err := handler.retrievalEventIDs("manifest")
	if err != nil {
		t.Fatal(err)
	}
	second, err := handler.retrievalEventIDs("manifest")
	if err != nil || first != second || first.event == first.outbox || first.outbox == first.publish {
		t.Fatalf("unstable or colliding retrieval identifiers: %#v %#v error=%v", first, second, err)
	}
}

func TestProjectionCommandValidation(t *testing.T) {
	command := MarkIndexedCommand{TenantID: "tenant", UserID: "user", MemoryID: "memory", MemoryVersion: 1, IndexGeneration: 1, EmbeddingRef: "vector://memory", LexicalRef: "lexical://memory", CorrelationID: "correlation", Actor: []byte(`{"kind":"service"}`), IndexedEvent: EventPointer{Ref: "encrypted://indexed", Hash: "indexed"}}
	if !validMarkIndexed(command) {
		t.Fatal("valid projection completion rejected")
	}
	command.EmbeddingRef = ""
	if validMarkIndexed(command) {
		t.Fatal("projection completion without vector reference accepted")
	}
}
