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
	rubrics, err := catalog.rubrics("00000000-0000-4000-8000-000000000001")
	if err != nil || len(rubrics) != 3 || rubrics[0].ID == "" || rubrics[0].PracticeKind == "" {
		t.Fatalf("rubrics=%#v error=%v", rubrics, err)
	}
}
