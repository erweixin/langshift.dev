package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

var defaultContextManifest = json.RawMessage(`{}`)

func normalizeContextManifest(raw json.RawMessage) (json.RawMessage, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return append(json.RawMessage(nil), defaultContextManifest...), nil
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("context_manifest must be a valid json object: %w", err)
	}
	if decoded == nil {
		return nil, fmt.Errorf("context_manifest must be a json object")
	}

	encoded, err := json.Marshal(decoded)
	if err != nil {
		return nil, fmt.Errorf("normalize context_manifest: %w", err)
	}
	return encoded, nil
}
