package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncodeRegistryIsCanonicalAndRuntimeLoadable(t *testing.T) {
	var document RegistryDocument
	if err := json.Unmarshal([]byte(registryFixture), &document); err != nil {
		t.Fatal(err)
	}
	first, firstHash, err := EncodeRegistry(document)
	if err != nil {
		t.Fatal(err)
	}
	document.Providers[0], document.Providers[1] = document.Providers[1], document.Providers[0]
	capabilities := document.Providers[1].Models[0].Capabilities
	for left, right := 0, len(capabilities)-1; left < right; left, right = left+1, right-1 {
		capabilities[left], capabilities[right] = capabilities[right], capabilities[left]
	}
	second, secondHash, err := EncodeRegistry(document)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || firstHash != secondHash {
		t.Fatalf("registry encoding is not canonical: %s/%s", firstHash, secondHash)
	}
	registry, err := LoadRegistry(strings.NewReader(string(first)))
	if err != nil {
		t.Fatal(err)
	}
	if _, version, _ := registry.Snapshot(); version != 7 {
		t.Fatalf("version=%d", version)
	}
	document.Providers[0].Endpoint = "http://api.anthropic.com/v1/"
	if _, _, err = EncodeRegistry(document); !errors.Is(err, ErrRegistryInvalid) {
		t.Fatalf("invalid registry encoded: %v", err)
	}
}

func TestLoadRegistryFileRejectsDeploymentSubstitution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.json")
	if err := os.WriteFile(path, []byte(registryFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(registryFixture))
	fileHash := hex.EncodeToString(digest[:])
	registry, err := LoadRegistryFile(path, fileHash)
	if err != nil {
		t.Fatal(err)
	}
	if id, version, hash := registry.Snapshot(); id == "" || version == 0 || hash == "" {
		t.Fatalf("snapshot=%q/%d/%q", id, version, hash)
	}
	if _, err = LoadRegistryFile(path, strings.Repeat("f", 64)); !errors.Is(err, ErrRegistryInvalid) {
		t.Fatalf("substitution error=%v", err)
	}
}

const registryFixture = `{
  "schema_version":1,
  "snapshot_id":"provider-registry:production",
  "version":7,
  "providers":[
    {
      "id":"openai-primary","type":"openai","status":"active",
      "endpoint":"https://api.openai.com/v1/","bound_host":"api.openai.com","region":"us-east-1",
      "credential":{"mode":"bearer","secret_ref":"lites/providers/openai","secret_version":"4"},
      "request_timeout":"2m","maximum_request_bytes":8388608,"maximum_response_bytes":33554432,
      "models":[{
        "id":"reasoning-high","version":"2026-07-01","wire_model":"gpt-5.6-2026-07-01",
        "pricing_version":"openai-2026-07","capabilities":["text","vision","tool_use","streaming"],
        "maximum_input_tokens":1000000,"maximum_output_tokens":128000,
        "input_microunits_per_million":5000000,"output_microunits_per_million":30000000,
        "input_credit_units_per_million":1000000,"output_credit_units_per_million":4000000
      }]
    },
    {
      "id":"anthropic-fallback","type":"anthropic","status":"degraded",
      "endpoint":"https://api.anthropic.com/v1/","bound_host":"api.anthropic.com","region":"us-west-2",
      "anthropic_version":"2023-06-01",
      "credential":{"mode":"x-api-key","secret_ref":"lites/providers/anthropic","secret_version":"9"},
      "request_timeout":"3m","maximum_request_bytes":8388608,"maximum_response_bytes":33554432,
      "models":[{
        "id":"reasoning-high","version":"2026-06-15","wire_model":"claude-opus-4-8-20260615",
        "pricing_version":"anthropic-2026-06","capabilities":["text","vision","tool_use","streaming"],
        "maximum_input_tokens":1000000,"maximum_output_tokens":128000,
        "input_microunits_per_million":5000000,"output_microunits_per_million":25000000,
        "input_credit_units_per_million":1000000,"output_credit_units_per_million":4000000
      }]
    }
  ]
}`

func TestRegistryPinsProviderModelPricingAndCredential(t *testing.T) {
	registry, err := LoadRegistry(strings.NewReader(registryFixture))
	if err != nil {
		t.Fatal(err)
	}
	id, version, hash := registry.Snapshot()
	if id != "provider-registry:production" || version != 7 || len(hash) != 64 {
		t.Fatalf("snapshot = %q %d %q", id, version, hash)
	}
	resolved, err := registry.Resolve("openai-primary", "reasoning-high", "2026-07-01", "api.openai.com", "openai-2026-07")
	if err != nil || resolved.Model.WireModel != "gpt-5.6-2026-07-01" || resolved.Provider.RequestTimeout.String() != "2m0s" {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	managed, err := resolved.Credential("", "")
	if err != nil || managed.SecretRef != "lites/providers/openai" || managed.SecretVersion != "4" || managed.Mode != "bearer" {
		t.Fatalf("managed credential=%#v err=%v", managed, err)
	}
	byok, err := resolved.Credential("lites/byok/tenant/credential", "11")
	if err != nil || byok.SecretRef != "lites/byok/tenant/credential" || byok.SecretVersion != "11" || byok.Mode != "bearer" {
		t.Fatalf("BYOK credential=%#v err=%v", byok, err)
	}
}

func TestRegistryRejectsDriftAliasesAndAmbiguousDefinitions(t *testing.T) {
	mutations := []string{
		strings.Replace(registryFixture, "https://api.openai.com/v1/", "http://api.openai.com/v1/", 1),
		strings.Replace(registryFixture, `"bound_host":"api.openai.com"`, `"bound_host":"evil.example"`, 1),
		strings.Replace(registryFixture, "gpt-5.6-2026-07-01", "gpt-5.6-latest", 1),
		strings.Replace(registryFixture, `"mode":"bearer"`, `"mode":"x-api-key"`, 1),
		strings.Replace(registryFixture, `"id":"anthropic-fallback"`, `"id":"openai-primary"`, 1),
		strings.Replace(registryFixture, `"request_timeout":"2m"`, `"request_timeout":"10m"`, 1),
	}
	for index, mutation := range mutations {
		if _, err := LoadRegistry(strings.NewReader(mutation)); !errors.Is(err, ErrRegistryInvalid) {
			t.Fatalf("mutation %d accepted: %v", index, err)
		}
	}
}

func TestRegistryResolutionFailsClosedOnEveryPinnedDimension(t *testing.T) {
	registry, _ := LoadRegistry(strings.NewReader(registryFixture))
	for _, values := range [][]string{
		{"missing", "reasoning-high", "2026-07-01", "api.openai.com", "openai-2026-07"},
		{"openai-primary", "other", "2026-07-01", "api.openai.com", "openai-2026-07"},
		{"openai-primary", "reasoning-high", "wrong", "api.openai.com", "openai-2026-07"},
		{"openai-primary", "reasoning-high", "2026-07-01", "evil.example", "openai-2026-07"},
		{"openai-primary", "reasoning-high", "2026-07-01", "api.openai.com", "wrong"},
	} {
		if _, err := registry.Resolve(values[0], values[1], values[2], values[3], values[4]); err == nil {
			t.Fatalf("unpinned resolution accepted: %v", values)
		}
	}
}
