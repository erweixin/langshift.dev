package agentworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"sort"
	"time"

	"github.com/langshift/lites/internal/behavior"
)

var ErrPromptArtifact = errors.New("prompt artifact is invalid")

const maximumPromptArtifactBytes = 32 << 20

var (
	promptSourceCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	promptAssetIDPattern      = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,127}$`)
	promptVersionPattern      = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,127}$`)
)

// PromptDefinition is one immutable prompt in a signed release artifact.
// TenantID is either "*" for a platform prompt or the exact tenant UUID for
// an enterprise override. Overrides still have to match the behavior binding.
type PromptDefinition struct {
	TenantID string `json:"tenant_id"`
	ID       string `json:"id"`
	Version  string `json:"version"`
	Content  string `json:"content"`
	Hash     string `json:"hash"`
}

type promptArtifactEnvelope struct {
	SchemaVersion int                `json:"schema_version"`
	ArtifactID    string             `json:"artifact_id"`
	ArtifactHash  string             `json:"artifact_hash"`
	SourceCommit  string             `json:"source_commit"`
	GeneratedAt   time.Time          `json:"generated_at"`
	Prompts       []PromptDefinition `json:"prompts"`
}

// PromptArtifact is immutable after construction and can be shared by all
// AgentWorker goroutines. No mutable deployment file is read on a Run path.
type PromptArtifact struct {
	ArtifactID, ArtifactHash, FileHash, SourceCommit string
	GeneratedAt                                      time.Time
	prompts                                          map[string]string
}

func LoadPromptArtifact(path, expectedFileHash string) (PromptArtifact, error) {
	if path == "" || !policyDigestPattern.MatchString(expectedFileHash) {
		return PromptArtifact{}, ErrPromptArtifact
	}
	file, err := os.Open(path)
	if err != nil {
		return PromptArtifact{}, ErrPromptArtifact
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, maximumPromptArtifactBytes+1))
	if err != nil || len(encoded) == 0 || len(encoded) > maximumPromptArtifactBytes || sha256Prompt(encoded) != expectedFileHash {
		return PromptArtifact{}, ErrPromptArtifact
	}
	var envelope promptArtifactEnvelope
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return PromptArtifact{}, ErrPromptArtifact
	}
	canonical, artifactHash, prompts, err := canonicalPromptEnvelope(envelope)
	if err != nil || artifactHash != envelope.ArtifactHash || envelope.ArtifactID != "prompt-artifact-"+artifactHash {
		return PromptArtifact{}, ErrPromptArtifact
	}
	_ = canonical
	return PromptArtifact{
		ArtifactID: envelope.ArtifactID, ArtifactHash: artifactHash, FileHash: expectedFileHash,
		SourceCommit: envelope.SourceCommit, GeneratedAt: envelope.GeneratedAt.UTC(), prompts: prompts,
	}, nil
}

func EncodePromptArtifact(definitions []PromptDefinition, sourceCommit string, generatedAt time.Time) ([]byte, string, error) {
	envelope := promptArtifactEnvelope{SchemaVersion: 1, SourceCommit: sourceCommit, GeneratedAt: generatedAt, Prompts: definitions}
	canonical, hash, _, err := canonicalPromptEnvelope(envelope)
	if err != nil {
		return nil, "", err
	}
	canonical.ArtifactHash, canonical.ArtifactID = hash, "prompt-artifact-"+hash
	encoded, err := json.Marshal(canonical)
	if err != nil || len(encoded) > maximumPromptArtifactBytes {
		return nil, "", ErrPromptArtifact
	}
	return encoded, sha256Prompt(encoded), nil
}

func canonicalPromptEnvelope(envelope promptArtifactEnvelope) (promptArtifactEnvelope, string, map[string]string, error) {
	_, offset := envelope.GeneratedAt.Zone()
	if envelope.SchemaVersion != 1 || !promptSourceCommitPattern.MatchString(envelope.SourceCommit) || envelope.GeneratedAt.IsZero() || offset != 0 || len(envelope.Prompts) == 0 || len(envelope.Prompts) > 4096 {
		return promptArtifactEnvelope{}, "", nil, ErrPromptArtifact
	}
	canonical := envelope
	canonical.ArtifactID, canonical.ArtifactHash = "", ""
	canonical.GeneratedAt = canonical.GeneratedAt.UTC().Truncate(time.Microsecond)
	canonical.Prompts = append([]PromptDefinition(nil), envelope.Prompts...)
	sort.Slice(canonical.Prompts, func(left, right int) bool {
		return promptKey(canonical.Prompts[left]) < promptKey(canonical.Prompts[right])
	})
	prompts := make(map[string]string, len(canonical.Prompts))
	for index, definition := range canonical.Prompts {
		key := promptKey(definition)
		if (definition.TenantID != "*" && !uuidTextPattern.MatchString(definition.TenantID)) || !promptAssetIDPattern.MatchString(definition.ID) || !promptVersionPattern.MatchString(definition.Version) || definition.Content == "" || len(definition.Content) > 4<<20 || !policyDigestPattern.MatchString(definition.Hash) || definition.Hash != sha256Prompt([]byte(definition.Content)) || index > 0 && promptKey(canonical.Prompts[index-1]) == key {
			return promptArtifactEnvelope{}, "", nil, ErrPromptArtifact
		}
		prompts[key] = definition.Content
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return promptArtifactEnvelope{}, "", nil, ErrPromptArtifact
	}
	return canonical, sha256Prompt(encoded), prompts, nil
}

var uuidTextPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func promptKey(value PromptDefinition) string {
	return value.TenantID + "\x00" + value.ID + "\x00" + value.Version
}

func (artifact PromptArtifact) LoadPrompt(_ context.Context, tenantID string, binding behavior.Binding) (string, error) {
	if artifact.ArtifactHash == "" || len(artifact.prompts) == 0 || tenantID == "" || binding.ID == "" || binding.Version == "" || !policyDigestPattern.MatchString(binding.Hash) {
		return "", ErrPromptArtifact
	}
	content, exists := artifact.prompts[promptKey(PromptDefinition{TenantID: tenantID, ID: binding.ID, Version: binding.Version})]
	if !exists {
		content, exists = artifact.prompts[promptKey(PromptDefinition{TenantID: "*", ID: binding.ID, Version: binding.Version})]
	}
	if !exists || sha256Prompt([]byte(content)) != binding.Hash {
		return "", ErrContextAsset
	}
	return content, nil
}

func sha256Prompt(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
