package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"time"

	behaviorpostgres "github.com/langshift/lites/internal/behavior/postgres"
)

var behaviorKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type behaviorKeyringDocument struct {
	Version string              `json:"version"`
	Keys    []behaviorKeyRecord `json:"keys"`
}

type behaviorKeyRecord struct {
	ID        string    `json:"id"`
	Purpose   string    `json:"purpose"`
	PublicKey string    `json:"public_key"`
	NotBefore time.Time `json:"not_before"`
	NotAfter  time.Time `json:"not_after"`
}

func loadBehaviorKeyring(path string, now time.Time) (map[string]ed25519.PublicKey, map[string]behaviorpostgres.KeyWindow, map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, errors.New("behavior signing keyring is unavailable")
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil || len(encoded) > 64*1024 {
		return nil, nil, nil, errors.New("behavior signing keyring is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var document behaviorKeyringDocument
	if err = decoder.Decode(&document); err != nil {
		return nil, nil, nil, errors.New("behavior signing keyring is invalid")
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || document.Version != "1.0.0" || len(document.Keys) < 3 || len(document.Keys) > 32 {
		return nil, nil, nil, errors.New("behavior signing keyring is invalid")
	}
	keys := make(map[string]ed25519.PublicKey, len(document.Keys))
	windows := make(map[string]behaviorpostgres.KeyWindow, len(document.Keys))
	purposes := make(map[string]string, len(document.Keys))
	activePurpose := map[string]int{}
	for _, record := range document.Keys {
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(record.PublicKey)
		if decodeErr != nil || len(decoded) != ed25519.PublicKeySize || !behaviorKeyIDPattern.MatchString(record.ID) || !validBehaviorKeyPurpose(record.Purpose) || record.NotBefore.IsZero() || !record.NotAfter.After(record.NotBefore) {
			return nil, nil, nil, errors.New("behavior signing keyring is invalid")
		}
		if _, duplicate := keys[record.ID]; duplicate {
			return nil, nil, nil, errors.New("behavior signing keyring contains duplicate key")
		}
		keys[record.ID] = ed25519.PublicKey(append([]byte(nil), decoded...))
		windows[record.ID] = behaviorpostgres.KeyWindow{NotBefore: record.NotBefore.UTC(), NotAfter: record.NotAfter.UTC()}
		purposes[record.ID] = record.Purpose
		if !now.Before(record.NotBefore) && now.Before(record.NotAfter) {
			activePurpose[record.Purpose]++
		}
	}
	for _, purpose := range []string{"risk_owner", "release_owner", "rollback_automation"} {
		if activePurpose[purpose] < 1 {
			return nil, nil, nil, errors.New("behavior signing keyring lacks an active purpose-separated key")
		}
	}
	return keys, windows, purposes, nil
}

func validBehaviorKeyPurpose(value string) bool {
	return value == "risk_owner" || value == "release_owner" || value == "rollback_automation"
}
