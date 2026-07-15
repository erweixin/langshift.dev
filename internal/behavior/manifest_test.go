package behavior

import (
	"strings"
	"testing"
	"time"
)

func TestManifestCanonicalHashBindsAllBehaviorComponents(t *testing.T) {
	manifest := validManifest()
	manifest.Tools = []Binding{binding("workspace.write", "v2", '7'), binding("memory.read", "v3", '6')}
	hash, err := manifest.Hash()
	if err != nil || len(hash) != 64 {
		t.Fatalf("Hash()=%q err=%v", hash, err)
	}
	id, err := manifest.SnapshotID()
	if err != nil || id != "behavior-"+hash {
		t.Fatalf("SnapshotID()=%q err=%v", id, err)
	}
	reordered := manifest
	reordered.Tools[0], reordered.Tools[1] = reordered.Tools[1], reordered.Tools[0]
	reorderedHash, err := reordered.Hash()
	if err != nil || reorderedHash != hash {
		t.Fatalf("tool ordering changed canonical hash: %q != %q (%v)", reorderedHash, hash, err)
	}
	changed := manifest
	changed.GuardrailPolicy = binding("guardrails", "v8", '8')
	changedHash, err := changed.Hash()
	if err != nil || changedHash == hash {
		t.Fatal("guardrail change did not create a new immutable snapshot")
	}
}

func TestManifestRejectsDuplicateToolsAndUnknownProfiles(t *testing.T) {
	manifest := validManifest()
	manifest.Tools = []Binding{binding("memory.read", "v1", '6'), binding("memory.read", "v2", '7')}
	if _, err := manifest.Hash(); err == nil {
		t.Fatal("duplicate tool identity accepted")
	}
	manifest = validManifest()
	manifest.Profile = "generic_agent"
	if _, err := manifest.Hash(); err == nil {
		t.Fatal("unregistered profile accepted")
	}
}

func validManifest() Manifest {
	return Manifest{
		SchemaVersion: 1, Profile: RoutePlanner,
		Model: binding("openai:gpt-production", "2026-07-15", '1'), Prompt: binding("route-planner", "v12", '2'),
		ProfileDefinition: binding("route_planner", "v4", '3'), GuardrailPolicy: binding("guardrails", "v7", '4'), RouterPolicy: binding("router", "v6", '5'),
		SourceCommit: "0123456789abcdef0123456789abcdef01234567", CreatedAt: time.Date(2026, 7, 15, 20, 0, 0, 0, time.UTC),
	}
}

func binding(id, version string, fill byte) Binding {
	return Binding{ID: id, Version: version, Hash: strings.Repeat(string(fill), 64)}
}
