package referenceassets

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langshift/lites/internal/releaseassets"
)

func TestBuildProducesAllFiveRuntimeLoadableProfiles(t *testing.T) {
	definitions, err := Build(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions.Prompts) != 5 || len(definitions.Routes) != 5 || len(definitions.Tools) != 1 || len(definitions.Providers.Providers) != 1 {
		t.Fatalf("definitions=%#v", definitions)
	}
	path := filepath.Join(t.TempDir(), "definitions.json")
	if err = Write(path, definitions); err != nil {
		t.Fatal(err)
	}
	loaded, err := releaseassets.LoadDefinitions(path)
	if err != nil || len(loaded.Routes) != 5 {
		t.Fatalf("loaded=%#v error=%v", loaded, err)
	}
	if err = Write(path, definitions); err == nil {
		t.Fatal("existing definition replaced")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm()&0o137 != 0 {
		t.Fatalf("mode=%v error=%v", info, err)
	}
}

func TestBuildRejectsPrivateAliasAndMutableImage(t *testing.T) {
	for _, mutation := range []func(*Config){
		func(config *Config) { config.ProviderHost = "provider.internal" },
		func(config *Config) { config.ProviderHost = "127.0.0.1" },
		func(config *Config) { config.RuntimeImage = "ghcr.io/langshift/reference:latest" },
	} {
		configuration := testConfig()
		mutation(&configuration)
		if _, err := Build(configuration); !errors.Is(err, ErrInvalid) {
			t.Fatalf("configuration accepted: %#v error=%v", configuration, err)
		}
	}
}

func testConfig() Config {
	return Config{ProviderHost: "reference-provider.example.com", CredentialSecretRef: "lites/providers/reference", CredentialSecretVersion: "1", RuntimeImage: "ghcr.io/langshift/reference-tool-runtime@sha256:" + strings.Repeat("a", 64)}
}
