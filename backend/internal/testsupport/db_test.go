package testsupport

import (
	"strings"
	"testing"
)

func TestSearchPathURLKeepsExistingQueryParams(t *testing.T) {
	got, err := SearchPathURL("postgres://user:pass@example.test/db?sslmode=disable", "schema_a")
	if err != nil {
		t.Fatalf("search path url: %v", err)
	}
	if !strings.Contains(got, "sslmode=disable") || !strings.Contains(got, "search_path=schema_a") {
		t.Fatalf("search_path url = %s", got)
	}
}
