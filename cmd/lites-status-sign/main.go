// Command lites-status-sign publishes a short-lived, purpose-signed status snapshot.
package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/langshift/lites/internal/statuspage"
)

type privateKeyBundle struct {
	Version    string `json:"version"`
	KeyID      string `json:"key_id"`
	PrivateKey string `json:"private_key"`
}

func main() {
	input := flag.String("input", "", "unsigned status document JSON")
	keyFile := flag.String("key-file", "", "purpose-specific Ed25519 private key bundle")
	output := flag.String("output", "", "signed snapshot output path")
	validFor := flag.Duration("valid-for", 2*time.Minute, "snapshot validity between 30s and 5m")
	flag.Parse()
	if err := publish(*input, *keyFile, *output, *validFor, time.Now().UTC()); err != nil {
		fmt.Fprintln(os.Stderr, "status snapshot rejected:", err)
		os.Exit(1)
	}
}

func publish(input, keyFile, output string, validFor time.Duration, now time.Time) error {
	if input == "" || keyFile == "" || output == "" || validFor < 30*time.Second || validFor > 5*time.Minute {
		return errors.New("input, key-file, output, and a 30s..5m validity are required")
	}
	var document statuspage.Document
	if err := readStrictFile(input, 256<<10, &document); err != nil {
		return errors.New("unsigned document is invalid")
	}
	var bundle privateKeyBundle
	if err := readStrictFile(keyFile, 16<<10, &bundle); err != nil || bundle.Version != "1.0.0" || bundle.KeyID == "" {
		return errors.New("private key bundle is invalid")
	}
	privateKey, err := base64.RawURLEncoding.DecodeString(bundle.PrivateKey)
	if err != nil || len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("private key material is invalid")
	}
	document.GeneratedAt = now.UTC()
	document.ValidUntil = document.GeneratedAt.Add(validFor)
	encoded, err := statuspage.Sign(document, bundle.KeyID, ed25519.PrivateKey(privateKey), document.GeneratedAt)
	if err != nil {
		return errors.New("status document violates the public contract")
	}
	return atomicWrite(output, encoded)
}

func readStrictFile(path string, limit int64, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(encoded)) > limit {
		return errors.New("file exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return err
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

func atomicWrite(path string, contents []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".public-status-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(contents)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
