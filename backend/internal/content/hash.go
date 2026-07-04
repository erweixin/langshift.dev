package content

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"lites/backend/internal/contracts"
)

func requestHash(input GenerationInput) (string, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("marshal content generation request hash input: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func contentKey(input GenerationInput) string {
	encoded, err := json.Marshal(struct {
		TaskTemplateID string `json:"task_template_id"`
		TargetStack    string `json:"target_stack"`
		LevelBand      string `json:"level_band"`
		ContentVersion int    `json:"content_version"`
		PromptVersion  int    `json:"prompt_version"`
	}{
		TaskTemplateID: input.TaskTemplateID,
		TargetStack:    input.TargetStack,
		LevelBand:      input.LevelBand,
		ContentVersion: contentArtifactVersion,
		PromptVersion:  contentArtifactPromptVersion,
	})
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(encoded)
	return "content_" + hex.EncodeToString(sum[:16])
}

func artifactHash(artifact contracts.ContentArtifact) (string, json.RawMessage, error) {
	encoded, err := json.Marshal(artifact)
	if err != nil {
		return "", nil, fmt.Errorf("marshal content artifact hash input: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), encoded, nil
}
