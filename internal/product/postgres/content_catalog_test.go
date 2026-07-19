package postgres

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/langshift/lites/internal/product/contentcatalog"
)

func TestContentCatalogRoleIDsAreTenantAndReleaseBound(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	release, err := contentcatalog.Load(filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "product-content", "releases", "1.0.0")))
	if err != nil {
		t.Fatal(err)
	}
	catalog := ContentCatalog{Release: release, IDKey: []byte("0123456789abcdef0123456789abcdef")}
	first, err := catalog.roles("00000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	second, err := catalog.roles("00000000-0000-4000-8000-000000000002")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 || len(first) != len(second) || first[0].ID == second[0].ID || first[0].Slug != second[0].Slug {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	again, err := catalog.roles("00000000-0000-4000-8000-000000000001")
	if err != nil || again[0].ID != first[0].ID {
		t.Fatalf("deterministic role id changed: %v %#v %#v", err, first[0], again[0])
	}
	otherService, err := (ContentCatalog{Release: release, IDKey: []byte("fedcba9876543210fedcba9876543210")}).roles("00000000-0000-4000-8000-000000000001")
	if err != nil || otherService[0].ID != first[0].ID {
		t.Fatalf("content projection unexpectedly depends on a service authority key: %v %#v %#v", err, first[0], otherService[0])
	}
	rubrics, err := catalog.rubrics("00000000-0000-4000-8000-000000000001")
	if err != nil || len(rubrics) != 3 || rubrics[0].ID == "" || rubrics[0].PracticeKind == "" {
		t.Fatalf("rubrics=%#v error=%v", rubrics, err)
	}
	capabilities, err := catalog.capabilities("00000000-0000-4000-8000-000000000001")
	if err != nil || len(capabilities) != len(release.Ontology.Capabilities) || len(capabilities) == 0 || capabilities[0].ID == "" || len(capabilities[0].Spec) == 0 || len(capabilities[0].EvidenceGuidance) == 0 {
		t.Fatalf("capabilities=%#v error=%v", capabilities, err)
	}
	otherCapabilities, err := catalog.capabilities("00000000-0000-4000-8000-000000000002")
	if err != nil || otherCapabilities[0].ID == capabilities[0].ID || otherCapabilities[0].Slug != capabilities[0].Slug {
		t.Fatalf("tenant-bound capabilities changed: %v %#v %#v", err, capabilities[0], otherCapabilities[0])
	}
	requirements, err := catalog.requirements("00000000-0000-4000-8000-000000000001", first, capabilities)
	if err != nil || len(requirements) == 0 || requirements[0].ID == "" || requirements[0].RoleProfileID == "" || requirements[0].CapabilityID == "" || requirements[0].RequirementLevel == "" || requirements[0].Rationale == "" {
		t.Fatalf("requirements=%#v error=%v", requirements, err)
	}
	otherRequirements, err := catalog.requirements("00000000-0000-4000-8000-000000000002", second, otherCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	matching := projectedRequirement{}
	for _, candidate := range otherRequirements {
		if candidate.Rationale == requirements[0].Rationale {
			matching = candidate
			break
		}
	}
	if matching.ID == "" || matching.ID == requirements[0].ID || matching.RequirementLevel != requirements[0].RequirementLevel {
		t.Fatalf("tenant-bound requirements changed: %#v %#v", requirements[0], matching)
	}
}
