package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/langshift/lites/internal/statuspage"
)

func TestPublishProducesReaderVerifiableFreshSnapshot(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	input := filepath.Join(directory, "input.json")
	keyFile := filepath.Join(directory, "private-key.json")
	keyringFile := filepath.Join(directory, "public-keyring.json")
	output := filepath.Join(directory, "status.json")
	if err = os.WriteFile(input, []byte(`{"schema_version":1,"overall":"operational","generated_at":"0001-01-01T00:00:00Z","valid_until":"0001-01-01T00:00:00Z","components":[{"id":"api","name":"API","state":"operational"}],"incidents":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyFile, []byte(fmt.Sprintf(`{"version":"1.0.0","key_id":"status-1","private_key":"%s"}`, base64.RawURLEncoding.EncodeToString(privateKey))), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyringFile, []byte(fmt.Sprintf(`{"version":"1.0.0","keys":[{"id":"status-1","public_key":"%s","not_before":"%s","not_after":"%s"}]}`, base64.RawURLEncoding.EncodeToString(publicKey), now.Add(-time.Hour).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339))), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = publish(input, keyFile, output, 2*time.Minute, now); err != nil {
		t.Fatal(err)
	}
	document, _, err := (statuspage.FileReader{DocumentFile: output, KeyringFile: keyringFile, Now: func() time.Time { return now }}).Read(context.Background())
	if err != nil || !document.GeneratedAt.Equal(now) || !document.ValidUntil.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("document=%#v error=%v", document, err)
	}
}

func TestPublishRejectsUnsafeValidityAndUnknownFields(t *testing.T) {
	if err := publish("missing", "missing", "missing", 6*time.Minute, time.Now()); err == nil {
		t.Fatal("unsafe validity accepted")
	}
}
