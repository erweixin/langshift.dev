package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/langshift/lites/internal/payload"
)

type routePlannerIntakeStore struct{ value []byte }

func (store routePlannerIntakeStore) Put(context.Context, payload.Descriptor, []byte) (payload.Manifest, error) {
	return payload.Manifest{}, errors.New("not implemented")
}

func (store routePlannerIntakeStore) Get(_ context.Context, descriptor payload.Descriptor, manifest payload.Manifest) ([]byte, error) {
	if descriptor.TenantID != "tenant-1" || descriptor.ObjectID != "session-1" || descriptor.Class != "onboarding-body" || manifest.Ref != "payload-ref" || manifest.Hash != "payload-hash" {
		return nil, errors.New("descriptor mismatch")
	}
	return append([]byte(nil), store.value...), nil
}

func TestRoutePlannerPromptIncludesDurablyReferencedNaturalLanguageIntake(t *testing.T) {
	intake := []byte(`{"current_role":"Frontend developer","target_role":"AI application engineer","experience_summary":"I built incident-aware UI workflows and want to learn durable crash recovery.","weekly_minutes":180}`)
	manifest := routeInputManifest{SchemaVersion: 3, Onboarding: &routeOnboardingSnapshot{SessionID: "session-1", Version: 2, CurrentRoleInput: json.RawMessage(`{"text":"Frontend developer"}`), TargetRoleInput: json.RawMessage(`{"text":"AI application engineer"}`), ExperiencePayload: payload.Manifest{Ref: "payload-ref", Hash: "payload-hash"}}}
	record := plannerRouteRecord{InputManifest: json.RawMessage(`{"schema_version":3,"mission":{"id":"mission-1"}}`)}
	service := RoutePlannerService{Payloads: routePlannerIntakeStore{value: intake}}

	prompt, err := service.routePlannerPromptText(context.Background(), "tenant-1", record, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "INPUT_MANIFEST=") || !strings.Contains(prompt, "ONBOARDING_INTAKE=") || !strings.Contains(prompt, "durable crash recovery") {
		t.Fatalf("prompt did not bind natural-language intake: %s", prompt)
	}
}

func TestRoutePlannerPromptRejectsInvalidReferencedIntake(t *testing.T) {
	manifest := routeInputManifest{SchemaVersion: 3, Onboarding: &routeOnboardingSnapshot{SessionID: "session-1", Version: 1, ExperiencePayload: payload.Manifest{Ref: "payload-ref", Hash: "payload-hash"}}}
	service := RoutePlannerService{Payloads: routePlannerIntakeStore{value: []byte("not-json")}}
	if _, err := service.routePlannerPromptText(context.Background(), "tenant-1", plannerRouteRecord{InputManifest: json.RawMessage(`{}`)}, manifest); !errors.Is(err, payload.ErrIntegrity) {
		t.Fatalf("invalid intake err=%v", err)
	}
}
