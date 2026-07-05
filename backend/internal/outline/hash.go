package outline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

func requestHash(input GenerationInput) (string, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("marshal outline generation request hash input: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func outlineID(runID string) string {
	return "outline_" + runID
}
