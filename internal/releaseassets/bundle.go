// Package releaseassets creates the immutable AgentWorker release bundle.
package releaseassets

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/langshift/lites/internal/agentworker"
	"github.com/langshift/lites/internal/llmgateway/provider"
	"github.com/langshift/lites/internal/toolregistry"
)

var (
	ErrInvalid = errors.New("Agent release asset definition is invalid")
	commitRE   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestRE   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

const DefinitionVersion = "1.0.0"

type Definitions struct {
	DefinitionVersion string                         `json:"definitionVersion"`
	Prompts           []agentworker.PromptDefinition `json:"prompts"`
	Routes            []agentworker.RouteDefinition  `json:"routes"`
	Tools             []toolregistry.Descriptor      `json:"tools"`
	Providers         provider.RegistryDocument      `json:"providers"`
}

type Artifact struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	ManifestVersion  string              `json:"manifestVersion"`
	SourceCommit     string              `json:"sourceCommit"`
	GeneratedAt      time.Time           `json:"generatedAt"`
	DefinitionSHA256 string              `json:"definitionSha256"`
	Artifacts        map[string]Artifact `json:"artifacts"`
	BundleSHA256     string              `json:"bundleSha256"`
}

type Bundle struct {
	Manifest Manifest
	Files    map[string][]byte
}

func LoadDefinitions(path string) (Definitions, error) {
	if !filepath.IsAbs(path) {
		return Definitions{}, ErrInvalid
	}
	encoded, err := os.ReadFile(path)
	if err != nil || len(encoded) == 0 || len(encoded) > 64<<20 {
		return Definitions{}, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var definitions Definitions
	if decoder.Decode(&definitions) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return Definitions{}, ErrInvalid
	}
	return definitions, nil
}

func Build(definitions Definitions, sourceCommit string, generatedAt time.Time) (Bundle, error) {
	_, offset := generatedAt.Zone()
	if definitions.DefinitionVersion != DefinitionVersion || !commitRE.MatchString(sourceCommit) || generatedAt.IsZero() || offset != 0 {
		return Bundle{}, ErrInvalid
	}
	generatedAt = generatedAt.UTC().Truncate(time.Microsecond)
	prompts, promptHash, err := agentworker.EncodePromptArtifact(definitions.Prompts, sourceCommit, generatedAt)
	if err != nil {
		return Bundle{}, errors.Join(ErrInvalid, err)
	}
	routes, routeHash, err := agentworker.EncodeRouteArtifact(definitions.Routes, sourceCommit, generatedAt)
	if err != nil {
		return Bundle{}, errors.Join(ErrInvalid, err)
	}
	tools, toolHash, err := toolregistry.EncodeArtifact(definitions.Tools, sourceCommit, generatedAt)
	if err != nil {
		return Bundle{}, errors.Join(ErrInvalid, err)
	}
	providers, providerHash, err := provider.EncodeRegistry(definitions.Providers)
	if err != nil {
		return Bundle{}, errors.Join(ErrInvalid, err)
	}
	files := map[string][]byte{
		"prompts.json": prompts, "routes.json": routes,
		"tools.json": tools, "providers.json": providers,
	}
	manifest := Manifest{
		ManifestVersion: "1.0.0", SourceCommit: sourceCommit, GeneratedAt: generatedAt,
		Artifacts: map[string]Artifact{
			"prompts":   {File: "prompts.json", SHA256: promptHash},
			"routes":    {File: "routes.json", SHA256: routeHash},
			"tools":     {File: "tools.json", SHA256: toolHash},
			"providers": {File: "providers.json", SHA256: providerHash},
		},
	}
	manifest.DefinitionSHA256, err = definitionHash(definitions)
	if err != nil {
		return Bundle{}, ErrInvalid
	}
	manifest.BundleSHA256, err = manifestHash(manifest)
	if err != nil {
		return Bundle{}, err
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return Bundle{}, ErrInvalid
	}
	files["manifest.json"] = manifestBytes
	return Bundle{Manifest: manifest, Files: files}, nil
}

// definitionHash is release-metadata independent. Approval systems can bind
// eval evidence to this digest before the reviewed definition is committed;
// source commit and timestamp are bound separately by the bundle manifest.
func definitionHash(definitions Definitions) (string, error) {
	canonicalCommit := strings.Repeat("0", 40)
	canonicalTime := time.Unix(1, 0).UTC()
	_, promptHash, err := agentworker.EncodePromptArtifact(definitions.Prompts, canonicalCommit, canonicalTime)
	if err != nil {
		return "", err
	}
	_, routeHash, err := agentworker.EncodeRouteArtifact(definitions.Routes, canonicalCommit, canonicalTime)
	if err != nil {
		return "", err
	}
	_, toolHash, err := toolregistry.EncodeArtifact(definitions.Tools, canonicalCommit, canonicalTime)
	if err != nil {
		return "", err
	}
	_, providerHash, err := provider.EncodeRegistry(definitions.Providers)
	if err != nil {
		return "", err
	}
	identity := struct {
		DefinitionVersion string            `json:"definitionVersion"`
		Artifacts         map[string]string `json:"artifacts"`
	}{definitions.DefinitionVersion, map[string]string{
		"prompts": promptHash, "routes": routeHash,
		"tools": toolHash, "providers": providerHash,
	}}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", ErrInvalid
	}
	return hash(encoded), nil
}

func Write(output string, bundle Bundle) error {
	if !filepath.IsAbs(output) || output == string(filepath.Separator) || !validBundle(bundle) {
		return ErrInvalid
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		return ErrInvalid
	}
	parent := filepath.Dir(output)
	if info, err := os.Stat(parent); err != nil || !info.IsDir() {
		return ErrInvalid
	}
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	temporary := filepath.Join(parent, "."+filepath.Base(output)+".tmp-"+hex.EncodeToString(random))
	if err := os.Mkdir(temporary, 0o750); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(temporary)
		}
	}()
	names := make([]string, 0, len(bundle.Files))
	for name := range bundle.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if filepath.Base(name) != name || name == "." || name == ".." {
			return ErrInvalid
		}
		file, err := os.OpenFile(filepath.Join(temporary, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
		if err != nil {
			return err
		}
		if _, err = file.Write(bundle.Files[name]); err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	directory, err := os.Open(temporary)
	if err != nil {
		return err
	}
	err = directory.Sync()
	closeErr := directory.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(temporary, output); err != nil {
		return err
	}
	committed = true
	return nil
}

func manifestHash(manifest Manifest) (string, error) {
	manifest.BundleSHA256 = ""
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", ErrInvalid
	}
	return hash(encoded), nil
}

func validBundle(bundle Bundle) bool {
	if bundle.Manifest.ManifestVersion != "1.0.0" || !commitRE.MatchString(bundle.Manifest.SourceCommit) || bundle.Manifest.GeneratedAt.IsZero() || !digestRE.MatchString(bundle.Manifest.DefinitionSHA256) || !digestRE.MatchString(bundle.Manifest.BundleSHA256) || len(bundle.Manifest.Artifacts) != 4 || len(bundle.Files) != 5 {
		return false
	}
	expected, err := manifestHash(bundle.Manifest)
	if err != nil || expected != bundle.Manifest.BundleSHA256 {
		return false
	}
	expectedArtifacts := map[string]string{
		"prompts": "prompts.json", "routes": "routes.json",
		"tools": "tools.json", "providers": "providers.json",
	}
	for name, expectedFile := range expectedArtifacts {
		artifact, declared := bundle.Manifest.Artifacts[name]
		encoded, exists := bundle.Files[artifact.File]
		if !declared || !exists || artifact.File != expectedFile || !digestRE.MatchString(artifact.SHA256) || hash(encoded) != artifact.SHA256 {
			return false
		}
	}
	manifestBytes, exists := bundle.Files["manifest.json"]
	if !exists {
		return false
	}
	canonicalManifest, err := json.Marshal(bundle.Manifest)
	if err != nil || !bytes.Equal(manifestBytes, canonicalManifest) {
		return false
	}
	promptArtifact, err := agentworker.DecodePromptArtifact(bundle.Files["prompts.json"], bundle.Manifest.Artifacts["prompts"].SHA256)
	if err != nil {
		return false
	}
	routeArtifact, err := agentworker.DecodeRouteArtifact(bundle.Files["routes.json"], bundle.Manifest.Artifacts["routes"].SHA256)
	if err != nil {
		return false
	}
	toolArtifact, err := toolregistry.DecodeArtifact(bundle.Files["tools.json"], bundle.Manifest.Artifacts["tools"].SHA256)
	if err != nil {
		return false
	}
	if _, err = provider.LoadRegistry(bytes.NewReader(bundle.Files["providers.json"])); err != nil {
		return false
	}
	return promptArtifact.SourceCommit == bundle.Manifest.SourceCommit && routeArtifact.SourceCommit == bundle.Manifest.SourceCommit && toolArtifact.SourceCommit == bundle.Manifest.SourceCommit &&
		promptArtifact.GeneratedAt.Equal(bundle.Manifest.GeneratedAt) && routeArtifact.GeneratedAt.Equal(bundle.Manifest.GeneratedAt) && toolArtifact.GeneratedAt.Equal(bundle.Manifest.GeneratedAt)
}

func hash(encoded []byte) string {
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func (manifest Manifest) String() string {
	return fmt.Sprintf("source_commit=%s bundle_sha256=%s", manifest.SourceCommit, manifest.BundleSHA256)
}
