// Package statuspage verifies short-lived, purpose-signed public status snapshots.
package statuspage

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/langshift/lites/internal/security/trustedcontext"
)

var ErrInvalid = errors.New("public status snapshot is invalid")

type ComponentState string

const (
	Operational ComponentState = "operational"
	Degraded    ComponentState = "degraded"
	MajorOutage ComponentState = "major_outage"
	Maintenance ComponentState = "maintenance"
	Unknown     ComponentState = "unknown"
)

type Document struct {
	SchemaVersion int            `json:"schema_version"`
	Overall       ComponentState `json:"overall"`
	GeneratedAt   time.Time      `json:"generated_at"`
	ValidUntil    time.Time      `json:"valid_until"`
	Components    []Component    `json:"components"`
	Incidents     []Incident     `json:"incidents"`
}

type Component struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	State   ComponentState `json:"state"`
	Message string         `json:"message,omitempty"`
}

type Incident struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Message   string    `json:"message"`
}

type signedDocument struct {
	Payload   Document `json:"payload"`
	KeyID     string   `json:"key_id"`
	Signature string   `json:"signature"`
}

type Reader interface {
	Read(context.Context) (Document, string, error)
}

type FileReader struct {
	DocumentFile, KeyringFile string
	Now                       func() time.Time
}

// Sign validates and signs one short-lived public status snapshot. The private
// key is deliberately accepted only by the publisher path; readers load a
// purpose-specific public keyring.
func Sign(document Document, keyID string, privateKey ed25519.PrivateKey, now time.Time) ([]byte, error) {
	if keyID == "" || len(privateKey) != ed25519.PrivateKeySize || !validDocument(document, now.UTC()) {
		return nil, ErrInvalid
	}
	canonical, err := json.Marshal(document)
	if err != nil {
		return nil, ErrInvalid
	}
	envelope := signedDocument{
		Payload:   document,
		KeyID:     keyID,
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, canonical)),
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, ErrInvalid
	}
	return append(encoded, '\n'), nil
}

func (reader FileReader) Read(_ context.Context) (Document, string, error) {
	now := time.Now().UTC()
	if reader.Now != nil {
		now = reader.Now().UTC()
	}
	encoded, err := os.ReadFile(reader.DocumentFile)
	if err != nil || len(encoded) == 0 || len(encoded) > 256<<10 {
		return Document{}, "", ErrInvalid
	}
	var envelope signedDocument
	if strictDecode(encoded, &envelope) != nil || envelope.KeyID == "" || envelope.Signature == "" || !validDocument(envelope.Payload, now) {
		return Document{}, "", ErrInvalid
	}
	keys, windows, err := trustedcontext.LoadPublicKeyring(reader.KeyringFile)
	key, keyFound := keys[envelope.KeyID]
	window, windowFound := windows[envelope.KeyID]
	if err != nil || !keyFound || !windowFound || len(key) != ed25519.PublicKeySize || envelope.Payload.GeneratedAt.Before(window.NotBefore) || !envelope.Payload.GeneratedAt.Before(window.NotAfter) || envelope.Payload.ValidUntil.After(window.NotAfter) {
		return Document{}, "", ErrInvalid
	}
	signature, err := base64.RawURLEncoding.DecodeString(envelope.Signature)
	canonical, marshalErr := json.Marshal(envelope.Payload)
	if err != nil || marshalErr != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(key, canonical, signature) {
		return Document{}, "", ErrInvalid
	}
	digest := sha256.Sum256(canonical)
	return envelope.Payload, `"` + hex.EncodeToString(digest[:]) + `"`, nil
}

func validDocument(document Document, now time.Time) bool {
	if document.SchemaVersion != 1 || !validState(document.Overall) || document.GeneratedAt.IsZero() || document.ValidUntil.IsZero() || document.GeneratedAt.After(now.Add(30*time.Second)) || !document.ValidUntil.After(now) || document.ValidUntil.Sub(document.GeneratedAt) > 5*time.Minute || document.ValidUntil.Sub(document.GeneratedAt) < 30*time.Second || len(document.Components) < 1 || len(document.Components) > 50 || len(document.Incidents) > 100 {
		return false
	}
	seenComponents := map[string]bool{}
	worst := Operational
	for _, component := range document.Components {
		if !validIdentifier(component.ID, 64) || !validText(component.Name, 1, 100) || !validState(component.State) || len(component.Message) > 500 || seenComponents[component.ID] {
			return false
		}
		seenComponents[component.ID] = true
		if stateRank(component.State) > stateRank(worst) {
			worst = component.State
		}
	}
	if worst != document.Overall {
		return false
	}
	seenIncidents := map[string]bool{}
	for _, incident := range document.Incidents {
		if !validIdentifier(incident.ID, 100) || !validText(incident.Title, 1, 200) || !validText(incident.Message, 1, 2000) || incident.State != "investigating" && incident.State != "identified" && incident.State != "monitoring" && incident.State != "resolved" || incident.StartedAt.IsZero() || incident.UpdatedAt.Before(incident.StartedAt) || incident.UpdatedAt.After(document.GeneratedAt.Add(30*time.Second)) || seenIncidents[incident.ID] {
			return false
		}
		if incident.State != "resolved" && document.Overall == Operational {
			return false
		}
		seenIncidents[incident.ID] = true
	}
	return true
}

func validState(state ComponentState) bool {
	return state == Operational || state == Degraded || state == MajorOutage || state == Maintenance || state == Unknown
}

func stateRank(state ComponentState) int {
	switch state {
	case Operational:
		return 0
	case Maintenance:
		return 1
	case Degraded:
		return 2
	case MajorOutage:
		return 3
	default:
		return 4
	}
}

func validIdentifier(value string, maximum int) bool {
	if value == "" || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character != '-' && character != '_' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func validText(value string, minimum, maximum int) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed == value && len(value) >= minimum && len(value) <= maximum
}

func strictDecode(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	return nil
}
