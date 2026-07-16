package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langshift/lites/internal/releaseassets"
)

func TestRunBuildsBundleFromSourceDateEpoch(t *testing.T) {
	configuration := filepath.Join(t.TempDir(), "definitions.json")
	fixture, err := os.ReadFile(filepath.Join("..", "..", "internal", "releaseassets", "testdata", "definitions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(configuration, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "bundle")
	var stdout, stderr bytes.Buffer
	err = run([]string{"--config", configuration, "--output", output, "--source-commit", strings.Repeat("a", 40), "--source-date-epoch", "1784185811"}, &stdout, &stderr)
	if err != nil || stderr.Len() != 0 || !strings.Contains(stdout.String(), "bundle_sha256=") {
		t.Fatalf("stdout=%q stderr=%q error=%v", stdout.String(), stderr.String(), err)
	}
	if _, err = os.Stat(filepath.Join(output, "manifest.json")); err != nil {
		t.Fatal(err)
	}
}

func TestRunFailsClosedOnArguments(t *testing.T) {
	for index, arguments := range [][]string{
		nil,
		{"--config", "relative", "--output", "/tmp/output", "--source-commit", strings.Repeat("a", 40), "--source-date-epoch", "1"},
		{"--config", "/tmp/missing", "--output", "/tmp/output", "--source-commit", strings.Repeat("a", 40), "--source-date-epoch", "0"},
		{"--unknown"},
	} {
		if err := run(arguments, &bytes.Buffer{}, &bytes.Buffer{}); !errors.Is(err, releaseassets.ErrInvalid) {
			t.Fatalf("case %d error=%v", index, err)
		}
	}
}
