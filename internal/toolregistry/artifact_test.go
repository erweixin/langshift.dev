package toolregistry

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArtifactRoundTripPinsFileAndCanonicalRegistry(t *testing.T) {
	descriptor := validDescriptor()
	generated := time.Date(2026, time.July, 16, 10, 0, 0, 0, time.UTC)
	encoded, fileHash, err := EncodeArtifact([]Descriptor{descriptor}, strings.Repeat("a", 40), generated)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "tool-registry.json")
	if err = os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	artifact, err := LoadArtifact(path, fileHash)
	if err != nil || artifact.FileHash != fileHash || artifact.RegistryID != "tool-registry-"+artifact.RegistryHash || artifact.SourceCommit != strings.Repeat("a", 40) || !artifact.GeneratedAt.Equal(generated) || len(artifact.Registry.Snapshots()) != 1 {
		t.Fatalf("artifact=%#v error=%v", artifact, err)
	}
	if _, err = artifact.Registry.Resolve("provider_lookup@1.2.3", artifact.Registry.Snapshots()[0].Hash); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactRejectsDeploymentSubstitutionAndUnknownFields(t *testing.T) {
	encoded, fileHash, err := EncodeArtifact([]Descriptor{validDescriptor()}, strings.Repeat("b", 40), time.Date(2026, time.July, 16, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "tool-registry.json")
	if err = os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadArtifact(path, strings.Repeat("f", 64)); !errors.Is(err, ErrArtifactInvalid) {
		t.Fatalf("substitution error=%v", err)
	}
	var envelope map[string]any
	if json.Unmarshal(encoded, &envelope) != nil {
		t.Fatal("decode artifact")
	}
	envelope["unexpected_authority"] = true
	tampered, _ := json.Marshal(envelope)
	if err = os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadArtifact(path, hashBytes(tampered)); !errors.Is(err, ErrArtifactInvalid) {
		t.Fatalf("unknown field error=%v", err)
	}
	_ = fileHash
}

func TestArtifactRejectsNonUTCBuildMetadata(t *testing.T) {
	zone := time.FixedZone("build-local", 8*60*60)
	if _, _, err := EncodeArtifact([]Descriptor{validDescriptor()}, strings.Repeat("c", 40), time.Date(2026, time.July, 16, 18, 0, 0, 0, zone)); !errors.Is(err, ErrArtifactInvalid) {
		t.Fatalf("error=%v", err)
	}
}

func TestEncodeArtifactCanonicalizesDescriptorOrder(t *testing.T) {
	first := validDescriptor()
	second := validDescriptor()
	second.Name, second.Version, second.Handler = "another_lookup", "2.0.0", "another_lookup"
	generated := time.Date(2026, time.July, 16, 10, 0, 0, 0, time.UTC)
	left, leftHash, err := EncodeArtifact([]Descriptor{first, second}, strings.Repeat("d", 40), generated)
	if err != nil {
		t.Fatal(err)
	}
	right, rightHash, err := EncodeArtifact([]Descriptor{second, first}, strings.Repeat("d", 40), generated)
	if err != nil || string(left) != string(right) || leftHash != rightHash {
		t.Fatalf("artifact encoding depends on declaration order: %v", err)
	}
}
