package postgres

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestArtifactCommandValidationAndIdentifiers(t *testing.T) {
	create := CreateArtifactCommand{ArtifactID: "artifact", TenantID: "tenant", UserID: "user", ProjectID: "project", ArtifactKind: "writing", Title: "Architecture memo", CorrelationID: "correlation", Actor: json.RawMessage(`{"kind":"user"}`), CreatedEvent: PayloadPointer{Ref: "encrypted://artifact-created", Hash: "created"}}
	if !validCreateArtifact(create) {
		t.Fatal("valid artifact creation rejected")
	}
	create.Title = ""
	if validCreateArtifact(create) {
		t.Fatal("untitled artifact accepted")
	}
	revise := AppendArtifactRevisionCommand{ArtifactID: "artifact", RevisionID: "revision", TenantID: "tenant", UserID: "user", ExpectedArtifactVersion: 1, ContentHash: "content", ObjectRef: "s3://artifact", ObjectVersion: "version", WorkspaceRevision: "git:head", MediaType: "text/markdown", ByteSize: 42, ScanResultHash: "scan", Evidence: []EvidenceBinding{{EvidenceID: "evidence", Version: 1, ContentHash: "evidence-hash"}}, CorrelationID: "correlation", Actor: json.RawMessage(`{"kind":"service"}`), RevisionCreatedEvent: PayloadPointer{Ref: "encrypted://revision-created", Hash: "revision-created"}}
	if !validAppendRevision(revise) {
		t.Fatal("valid artifact revision rejected")
	}
	revise.ScanResultHash = ""
	if validAppendRevision(revise) {
		t.Fatal("artifact revision without scan proof accepted")
	}
	store := ArtifactStore{IDKey: bytes.Repeat([]byte{0x24}, 32)}
	first, err := store.eventIDs("artifact-created", "artifact")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.eventIDs("artifact-created", "artifact")
	if err != nil || first != replay {
		t.Fatalf("artifact identifiers are unstable: %#v %#v err=%v", first, replay, err)
	}
	revision, err := store.eventIDs("artifact-revision-created", "artifact")
	if err != nil || revision == first {
		t.Fatalf("artifact event domains are not separated: %#v %#v err=%v", first, revision, err)
	}
}

func TestArtifactEvidenceManifestIsCanonicalAndContentAddressed(t *testing.T) {
	bindings := []EvidenceBinding{{EvidenceID: "a", Version: 1, ContentHash: "ha"}, {EvidenceID: "b", Version: 2, ContentHash: "hb"}}
	manifest, hash, err := artifactEvidenceManifest("artifact", 3, "git:head", bindings)
	if err != nil || hash == "" || !json.Valid(manifest) {
		t.Fatalf("manifest=%s hash=%s err=%v", manifest, hash, err)
	}
	replay, replayHash, err := artifactEvidenceManifest("artifact", 3, "git:head", bindings)
	if err != nil || replayHash != hash || !bytes.Equal(replay, manifest) {
		t.Fatalf("manifest is not deterministic: %s/%s %s/%s err=%v", manifest, hash, replay, replayHash, err)
	}
	changed := append([]EvidenceBinding(nil), bindings...)
	changed[1].ContentHash = "different"
	_, changedHash, err := artifactEvidenceManifest("artifact", 3, "git:head", changed)
	if err != nil || changedHash == hash {
		t.Fatal("evidence substitution did not change manifest hash")
	}
}

func TestArtifactStoreNowUsesDatabasePrecision(t *testing.T) {
	want := time.Date(2026, time.July, 15, 1, 2, 3, 456789999, time.UTC)
	store := ArtifactStore{Now: func() time.Time { return want }}
	if got := store.now(); got.Nanosecond() != 456789000 {
		t.Fatalf("timestamp=%s", got)
	}
}
