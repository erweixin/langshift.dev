package main

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/langshift/lites/internal/agentworker"
	"github.com/langshift/lites/internal/llmgateway/provider"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/statuspage"
	"github.com/langshift/lites/internal/toolregistry"
)

func TestGenerateCreatesInternallyConsistentShortLivedArtifacts(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "artifacts")
	now := time.Date(2026, time.July, 18, 10, 0, 0, 0, time.UTC)
	if err := generate(directory, now); err != nil {
		t.Fatal(err)
	}
	keys, windows, err := trustedcontext.LoadPublicKeyring(filepath.Join(directory, "trusted-context-keyring.json"))
	if err != nil || len(keys) != 1 || !windows[trustedContextKeyID].NotAfter.After(now) {
		t.Fatalf("trusted keyring keys=%d windows=%#v err=%v", len(keys), windows, err)
	}
	document, _, err := (statuspage.FileReader{DocumentFile: filepath.Join(directory, "public-status.json"), KeyringFile: filepath.Join(directory, "public-status-keyring.json"), Now: func() time.Time { return now }}).Read(context.Background())
	if err != nil || document.Overall != statuspage.Operational {
		t.Fatalf("status=%#v err=%v", document, err)
	}
	promptHash := readTrimmed(t, directory, "prompt-artifact-hash")
	routeHash := readTrimmed(t, directory, "model-route-artifact-hash")
	toolHash := readTrimmed(t, directory, "tool-registry-artifact-hash")
	providerHash := readTrimmed(t, directory, "provider-registry-artifact-hash")
	if _, err = agentworker.LoadPromptArtifact(filepath.Join(directory, "prompts.json"), promptHash); err != nil {
		t.Fatal(err)
	}
	if _, err = agentworker.LoadRouteArtifact(filepath.Join(directory, "routes.json"), routeHash); err != nil {
		t.Fatal(err)
	}
	if _, err = toolregistry.LoadArtifact(filepath.Join(directory, "tools.json"), toolHash); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.LoadRegistryFile(filepath.Join(directory, "providers.json"), providerHash); err != nil {
		t.Fatal(err)
	}
	if value := readTrimmed(t, directory, "database-url-agent"); !strings.HasPrefix(value, "postgresql://lites_agent_service:") || !strings.HasSuffix(value, "@postgres:5432/lites?sslmode=disable") {
		t.Fatalf("agent database URL is not purpose scoped: %q", value)
	}
	if value := readTrimmed(t, directory, "database-url-behavior"); !strings.HasPrefix(value, "postgresql://lites_behavior_service:") || !strings.HasSuffix(value, "@postgres:5432/lites?sslmode=disable") {
		t.Fatalf("behavior database URL is not purpose scoped: %q", value)
	}
	var behaviorKeyring struct {
		Version string `json:"version"`
		Keys    []struct {
			ID        string    `json:"id"`
			Purpose   string    `json:"purpose"`
			PublicKey string    `json:"public_key"`
			NotBefore time.Time `json:"not_before"`
			NotAfter  time.Time `json:"not_after"`
		} `json:"keys"`
	}
	decodeJSONFile(t, filepath.Join(directory, "behavior-signing-keyring.json"), &behaviorKeyring)
	purposes := map[string]bool{}
	for _, key := range behaviorKeyring.Keys {
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(key.PublicKey)
		if decodeErr != nil || len(decoded) != ed25519.PublicKeySize || key.ID == "" || now.Before(key.NotBefore) || !now.Before(key.NotAfter) {
			t.Fatalf("invalid behavior key %#v: %v", key, decodeErr)
		}
		purposes[key.Purpose] = true
	}
	if behaviorKeyring.Version != "1.0.0" || len(behaviorKeyring.Keys) != 3 || !purposes["risk_owner"] || !purposes["release_owner"] || !purposes["rollback_automation"] {
		t.Fatalf("behavior keyring is not purpose complete: %#v", behaviorKeyring)
	}
	for _, name := range []string{"agent-control-plane", "behavior-control-plane", "contract-service"} {
		certificatePEM, readErr := os.ReadFile(filepath.Join(directory, name+".pem"))
		block, _ := pem.Decode(certificatePEM)
		if readErr != nil || block == nil {
			t.Fatalf("%s certificate: %v", name, readErr)
		}
		certificate, parseErr := x509.ParseCertificate(block.Bytes)
		if parseErr != nil || certificate.VerifyHostname(name) != nil {
			t.Fatalf("%s certificate host binding: parse=%v", name, parseErr)
		}
	}
	payloadSeed, err := base64.StdEncoding.DecodeString(readTrimmed(t, directory, "payload-key-seed"))
	if err != nil || len(payloadSeed) != 32 {
		t.Fatalf("payload key seed bytes=%d err=%v", len(payloadSeed), err)
	}
	kmsParts := strings.Split(readTrimmed(t, directory, "minio-kms-secret-key"), ":")
	if len(kmsParts) != 2 || kmsParts[0] != "lites-local" {
		t.Fatalf("invalid local MinIO KMS key envelope")
	}
	kmsKey, err := base64.StdEncoding.DecodeString(kmsParts[1])
	if err != nil || len(kmsKey) != 32 {
		t.Fatalf("MinIO KMS key bytes=%d err=%v", len(kmsKey), err)
	}
	certificatePEM, err := os.ReadFile(filepath.Join(directory, "local-model-adapter.pem"))
	block, _ := pem.Decode(certificatePEM)
	if err != nil || block == nil {
		t.Fatalf("model certificate: %v", err)
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if verifyErr := certificate.VerifyHostname("model-adapter.lites.test"); verifyErr != nil {
		t.Fatalf("model certificate host binding: %v", verifyErr)
	}
	mailCertificatePEM, err := os.ReadFile(filepath.Join(directory, "local-mailbox.pem"))
	mailBlock, _ := pem.Decode(mailCertificatePEM)
	if err != nil || mailBlock == nil {
		t.Fatalf("mailbox certificate: %v", err)
	}
	mailCertificate, err := x509.ParseCertificate(mailBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if verifyErr := mailCertificate.VerifyHostname("local-mailbox"); verifyErr != nil {
		t.Fatalf("mailbox certificate host binding: %v", verifyErr)
	}
	encoded, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	var generated manifest
	if err != nil || json.Unmarshal(encoded, &generated) != nil || generated.Kind != "lites-local-compose-artifacts" || len(generated.Files) < 40 {
		t.Fatalf("manifest=%#v err=%v", generated, err)
	}
	if err = generate(directory, now); err == nil {
		t.Fatal("non-empty artifact directory was overwritten")
	}
}

func readTrimmed(t *testing.T, directory, name string) string {
	t.Helper()
	encoded, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(encoded))
}

func decodeJSONFile(t *testing.T, path string, target any) {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(encoded, target) != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}
