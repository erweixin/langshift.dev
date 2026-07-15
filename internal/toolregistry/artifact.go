package toolregistry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"time"
)

var ErrArtifactInvalid = errors.New("tool registry artifact is invalid")

const maximumArtifactBytes = 32 << 20

var sourceCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type Artifact struct {
	RegistryID   string
	RegistryHash string
	FileHash     string
	SourceCommit string
	GeneratedAt  time.Time
	Registry     Registry
}

type artifactEnvelope struct {
	SchemaVersion int          `json:"schema_version"`
	RegistryID    string       `json:"registry_id"`
	RegistryHash  string       `json:"registry_hash"`
	SourceCommit  string       `json:"source_commit"`
	GeneratedAt   time.Time    `json:"generated_at"`
	Descriptors   []Descriptor `json:"descriptors"`
}

// LoadArtifact reads a content-pinned deployment artifact. The expected file
// hash is configured independently (for example in a signed release manifest),
// while RegistryHash proves the canonical descriptor set after strict parsing.
func LoadArtifact(path, expectedFileHash string) (Artifact, error) {
	if path == "" || !digestPattern.MatchString(expectedFileHash) {
		return Artifact{}, ErrArtifactInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return Artifact{}, ErrArtifactInvalid
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, maximumArtifactBytes+1))
	if err != nil || len(encoded) == 0 || len(encoded) > maximumArtifactBytes || hashBytes(encoded) != expectedFileHash {
		return Artifact{}, ErrArtifactInvalid
	}
	var envelope artifactEnvelope
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return Artifact{}, ErrArtifactInvalid
	}
	_, offset := envelope.GeneratedAt.Zone()
	if envelope.SchemaVersion != 1 || envelope.GeneratedAt.IsZero() || offset != 0 || !sourceCommitPattern.MatchString(envelope.SourceCommit) || !digestPattern.MatchString(envelope.RegistryHash) || envelope.RegistryID != "tool-registry-"+envelope.RegistryHash {
		return Artifact{}, ErrArtifactInvalid
	}
	registry, err := New(envelope.Descriptors)
	if err != nil || registry.Hash() != envelope.RegistryHash {
		return Artifact{}, errors.Join(ErrArtifactInvalid, err)
	}
	return Artifact{
		RegistryID: envelope.RegistryID, RegistryHash: envelope.RegistryHash,
		FileHash: expectedFileHash, SourceCommit: envelope.SourceCommit,
		GeneratedAt: envelope.GeneratedAt.UTC(), Registry: registry,
	}, nil
}

// EncodeArtifact is used by the release pipeline to create the exact artifact
// consumed by LoadArtifact. Descriptor canonicalization remains centralized in
// New, so build and runtime cannot disagree about the registry hash.
func EncodeArtifact(descriptors []Descriptor, sourceCommit string, generatedAt time.Time) ([]byte, string, error) {
	_, offset := generatedAt.Zone()
	if !sourceCommitPattern.MatchString(sourceCommit) || generatedAt.IsZero() || offset != 0 {
		return nil, "", ErrArtifactInvalid
	}
	registry, err := New(descriptors)
	if err != nil {
		return nil, "", errors.Join(ErrArtifactInvalid, err)
	}
	snapshots := registry.Snapshots()
	canonical := make([]Descriptor, len(snapshots))
	for index := range snapshots {
		canonical[index] = snapshots[index].Descriptor
	}
	encoded, err := json.Marshal(artifactEnvelope{
		SchemaVersion: 1, RegistryID: "tool-registry-" + registry.Hash(), RegistryHash: registry.Hash(),
		SourceCommit: sourceCommit, GeneratedAt: generatedAt.UTC(), Descriptors: canonical,
	})
	if err != nil || len(encoded) > maximumArtifactBytes {
		return nil, "", ErrArtifactInvalid
	}
	digest := sha256.Sum256(encoded)
	return encoded, hex.EncodeToString(digest[:]), nil
}
