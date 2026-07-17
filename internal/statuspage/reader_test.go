package statuspage

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileReaderAcceptsPurposeSignedFreshSnapshot(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	document, documentFile, keyringFile, _, privateKey := signedFixture(t, now)
	got, etag, err := (FileReader{DocumentFile: documentFile, KeyringFile: keyringFile, Now: func() time.Time { return now }}).Read(context.Background())
	if err != nil || got.Overall != document.Overall || got.Components[0].ID != "api" || len(etag) != 66 || etag[0] != '"' || etag[len(etag)-1] != '"' {
		t.Fatalf("document=%#v etag=%q error=%v", got, etag, err)
	}
	if _, err = Sign(document, "wrong", privateKey[:32], now); err == nil {
		t.Fatal("short private key accepted")
	}
}

func TestFileReaderFailsClosedForTamperingExpiryAndInconsistentOverall(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	document, documentFile, keyringFile, _, privateKey := signedFixture(t, now)

	encoded, err := os.ReadFile(documentFile)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err = json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatal(err)
	}
	payload := envelope["payload"].(map[string]any)
	payload["overall"] = "major_outage"
	tampered, _ := json.Marshal(envelope)
	if err = os.WriteFile(documentFile, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = (FileReader{DocumentFile: documentFile, KeyringFile: keyringFile, Now: func() time.Time { return now }}).Read(context.Background()); err == nil {
		t.Fatal("tampered snapshot accepted")
	}

	expired := document
	expired.GeneratedAt = now.Add(-2 * time.Minute)
	expired.ValidUntil = now.Add(-time.Minute)
	canonical, _ := json.Marshal(expired)
	envelope = map[string]any{"payload": expired, "key_id": "status-1", "signature": base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, canonical))}
	encoded, _ = json.Marshal(envelope)
	if err = os.WriteFile(documentFile, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = (FileReader{DocumentFile: documentFile, KeyringFile: keyringFile, Now: func() time.Time { return now }}).Read(context.Background()); err == nil {
		t.Fatal("expired snapshot accepted")
	}

	inconsistent := document
	inconsistent.Overall = MajorOutage
	if _, err = Sign(inconsistent, "status-1", privateKey, now); err == nil {
		t.Fatal("inconsistent overall state accepted by publisher")
	}
	falseGreen := document
	falseGreen.Incidents = []Incident{{ID: "api-outage", Title: "API outage", State: "investigating", StartedAt: now.Add(-time.Minute), UpdatedAt: now, Message: "Investigating API errors."}}
	if _, err = Sign(falseGreen, "status-1", privateKey, now); err == nil {
		t.Fatal("active incident with operational overall accepted by publisher")
	}
}

func signedFixture(t *testing.T, now time.Time) (Document, string, string, ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	document := Document{SchemaVersion: 1, Overall: Operational, GeneratedAt: now, ValidUntil: now.Add(2 * time.Minute), Components: []Component{{ID: "api", Name: "API", State: Operational}}, Incidents: []Incident{}}
	encoded, err := Sign(document, "status-1", privateKey, now)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	documentFile := filepath.Join(directory, "status.json")
	keyringFile := filepath.Join(directory, "keyring.json")
	if err = os.WriteFile(documentFile, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	keyring := fmt.Sprintf(`{"version":"1.0.0","keys":[{"id":"status-1","public_key":"%s","not_before":"%s","not_after":"%s"}]}`, base64.RawURLEncoding.EncodeToString(publicKey), now.Add(-time.Hour).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339))
	if err = os.WriteFile(keyringFile, []byte(keyring), 0o600); err != nil {
		t.Fatal(err)
	}
	return document, documentFile, keyringFile, publicKey, privateKey
}
