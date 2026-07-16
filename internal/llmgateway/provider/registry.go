package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	ErrRegistryInvalid   = errors.New("provider registry is invalid")
	ErrProviderNotFound  = errors.New("provider is not available")
	ErrModelNotFound     = errors.New("provider model snapshot is not available")
	ErrCredentialBinding = errors.New("provider credential binding is invalid")
)

type RegistryDocument struct {
	SchemaVersion int                  `json:"schema_version"`
	SnapshotID    string               `json:"snapshot_id"`
	Version       uint64               `json:"version"`
	Providers     []ProviderDefinition `json:"providers"`
}

type ProviderDefinition struct {
	ID                   string            `json:"id"`
	Type                 string            `json:"type"`
	Status               string            `json:"status"`
	Endpoint             string            `json:"endpoint"`
	BoundHost            string            `json:"bound_host"`
	Region               string            `json:"region"`
	AnthropicVersion     string            `json:"anthropic_version,omitempty"`
	Credential           ManagedCredential `json:"credential"`
	RequestTimeout       time.Duration     `json:"-"`
	RequestTimeoutText   string            `json:"request_timeout"`
	MaximumRequestBytes  int               `json:"maximum_request_bytes"`
	MaximumResponseBytes int64             `json:"maximum_response_bytes"`
	Models               []ModelDefinition `json:"models"`
}

type ManagedCredential struct {
	Mode          string `json:"mode"`
	SecretRef     string `json:"secret_ref"`
	SecretVersion string `json:"secret_version"`
}

type ModelDefinition struct {
	ID                          string   `json:"id"`
	Version                     string   `json:"version"`
	WireModel                   string   `json:"wire_model"`
	PricingVersion              string   `json:"pricing_version"`
	Capabilities                []string `json:"capabilities"`
	MaximumInputTokens          uint64   `json:"maximum_input_tokens"`
	MaximumOutputTokens         uint64   `json:"maximum_output_tokens"`
	InputMicrounitsPerMillion   uint64   `json:"input_microunits_per_million"`
	OutputMicrounitsPerMillion  uint64   `json:"output_microunits_per_million"`
	InputCreditUnitsPerMillion  uint64   `json:"input_credit_units_per_million"`
	OutputCreditUnitsPerMillion uint64   `json:"output_credit_units_per_million"`
}

type Registry struct {
	document  RegistryDocument
	providers map[string]ProviderDefinition
	hash      string
}

type ResolvedProvider struct {
	Provider ProviderDefinition
	Model    ModelDefinition
}

// EncodeRegistry canonicalizes a release registry before it is signed and
// mounted. Runtime loading remains strict and independently recomputes the
// semantic snapshot hash.
func EncodeRegistry(document RegistryDocument) ([]byte, string, error) {
	canonical := document
	canonical.Providers = append([]ProviderDefinition(nil), document.Providers...)
	for index := range canonical.Providers {
		definition := &canonical.Providers[index]
		definition.Models = append([]ModelDefinition(nil), definition.Models...)
		for modelIndex := range definition.Models {
			definition.Models[modelIndex].Capabilities = append([]string(nil), definition.Models[modelIndex].Capabilities...)
			sort.Strings(definition.Models[modelIndex].Capabilities)
		}
		sort.Slice(definition.Models, func(left, right int) bool {
			return definition.Models[left].ID+"\x00"+definition.Models[left].Version < definition.Models[right].ID+"\x00"+definition.Models[right].Version
		})
	}
	sort.Slice(canonical.Providers, func(left, right int) bool { return canonical.Providers[left].ID < canonical.Providers[right].ID })
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return nil, "", ErrRegistryInvalid
	}
	registry, err := LoadRegistry(bytes.NewReader(encoded))
	if err != nil {
		return nil, "", err
	}
	canonicalEncoded, err := json.Marshal(registry.document)
	if err != nil {
		return nil, "", ErrRegistryInvalid
	}
	return canonicalEncoded, hashBytes(canonicalEncoded), nil
}

func LoadRegistry(reader io.Reader) (Registry, error) {
	if reader == nil {
		return Registry{}, ErrRegistryInvalid
	}
	payload, err := io.ReadAll(io.LimitReader(reader, 4<<20+1))
	if err != nil || len(payload) == 0 || len(payload) > 4<<20 {
		return Registry{}, ErrRegistryInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var document RegistryDocument
	if decoder.Decode(&document) != nil || decoder.Decode(&struct{}{}) != io.EOF || document.SchemaVersion != 1 || document.SnapshotID == "" || document.Version == 0 || len(document.Providers) == 0 || len(document.Providers) > 128 {
		return Registry{}, ErrRegistryInvalid
	}
	providers := make(map[string]ProviderDefinition, len(document.Providers))
	for index := range document.Providers {
		definition := document.Providers[index]
		if err = validateProvider(&definition); err != nil {
			return Registry{}, err
		}
		if _, exists := providers[definition.ID]; exists {
			return Registry{}, ErrRegistryInvalid
		}
		document.Providers[index] = definition
		providers[definition.ID] = definition
	}
	canonical, err := json.Marshal(document)
	if err != nil {
		return Registry{}, ErrRegistryInvalid
	}
	digest := sha256.Sum256(canonical)
	return Registry{document: document, providers: providers, hash: hex.EncodeToString(digest[:])}, nil
}

// LoadRegistryFile pins the deployed provider catalog by its release-manifest
// file hash before parsing the canonical registry. This prevents replacing a
// valid registry file with another valid but unauthorized catalog.
func LoadRegistryFile(path, expectedFileHash string) (Registry, error) {
	if path == "" || !registryDigestPattern.MatchString(expectedFileHash) {
		return Registry{}, ErrRegistryInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return Registry{}, ErrRegistryInvalid
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, 4<<20+1))
	if err != nil || len(encoded) == 0 || len(encoded) > 4<<20 || hashBytes(encoded) != expectedFileHash {
		return Registry{}, ErrRegistryInvalid
	}
	return LoadRegistry(bytes.NewReader(encoded))
}

func (registry Registry) Snapshot() (string, uint64, string) {
	return registry.document.SnapshotID, registry.document.Version, registry.hash
}

func (registry Registry) Resolve(providerID, modelID, modelVersion, boundHost, pricingVersion string) (ResolvedProvider, error) {
	definition, exists := registry.providers[providerID]
	if !exists || definition.Status == "disabled" || definition.BoundHost != boundHost {
		return ResolvedProvider{}, ErrProviderNotFound
	}
	for _, model := range definition.Models {
		if model.ID == modelID && model.Version == modelVersion && model.PricingVersion == pricingVersion {
			return ResolvedProvider{Provider: definition, Model: model}, nil
		}
	}
	return ResolvedProvider{}, ErrModelNotFound
}

func (resolved ResolvedProvider) Credential(byokRef, byokVersion string) (ManagedCredential, error) {
	credential := resolved.Provider.Credential
	if byokRef != "" || byokVersion != "" {
		if byokRef == "" || byokVersion == "" {
			return ManagedCredential{}, ErrCredentialBinding
		}
		credential.SecretRef, credential.SecretVersion = byokRef, byokVersion
	}
	if !validCredentialDefinition(resolved.Provider.Type, credential) {
		return ManagedCredential{}, ErrCredentialBinding
	}
	return credential, nil
}

var (
	registryIDPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)
	registryDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func validateProvider(definition *ProviderDefinition) error {
	if definition == nil || !registryIDPattern.MatchString(definition.ID) || !registryIDPattern.MatchString(definition.Region) || len(definition.Models) == 0 || len(definition.Models) > 256 || definition.MaximumRequestBytes < 1024 || definition.MaximumRequestBytes > 64<<20 || definition.MaximumResponseBytes < 1024 || definition.MaximumResponseBytes > 64<<20 {
		return ErrRegistryInvalid
	}
	switch definition.Type {
	case "openai", "anthropic", "openai_compatible":
	default:
		return ErrRegistryInvalid
	}
	if definition.Status != "active" && definition.Status != "degraded" && definition.Status != "disabled" {
		return ErrRegistryInvalid
	}
	endpoint, err := url.Parse(definition.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Hostname() == "" || endpoint.Port() != "" && endpoint.Port() != "443" || !strings.HasSuffix(endpoint.Path, "/") || endpoint.RawPath != "" {
		return ErrRegistryInvalid
	}
	host := strings.ToLower(strings.TrimSuffix(endpoint.Hostname(), "."))
	if host != definition.BoundHost || net.ParseIP(host) != nil || strings.ContainsAny(host, "_\\\x00\r\n") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return ErrRegistryInvalid
	}
	definition.BoundHost = host
	if definition.RequestTimeout, err = time.ParseDuration(definition.RequestTimeoutText); err != nil || definition.RequestTimeout < time.Second || definition.RequestTimeout > 5*time.Minute {
		return ErrRegistryInvalid
	}
	if !validCredentialDefinition(definition.Type, definition.Credential) {
		return ErrRegistryInvalid
	}
	if definition.Type == "anthropic" {
		if len(definition.AnthropicVersion) != 10 || definition.AnthropicVersion[4] != '-' || definition.AnthropicVersion[7] != '-' {
			return ErrRegistryInvalid
		}
	} else if definition.AnthropicVersion != "" {
		return ErrRegistryInvalid
	}
	models := make(map[string]struct{}, len(definition.Models))
	for _, model := range definition.Models {
		key := model.ID + "\x00" + model.Version
		if model.ID == "" || model.Version == "" || model.WireModel == "" || model.PricingVersion == "" || strings.Contains(strings.ToLower(model.WireModel), "latest") || model.MaximumInputTokens == 0 || model.MaximumOutputTokens == 0 || model.MaximumOutputTokens > model.MaximumInputTokens || model.InputMicrounitsPerMillion > 1_000_000_000_000 || model.OutputMicrounitsPerMillion > 1_000_000_000_000 || model.InputCreditUnitsPerMillion == 0 || model.OutputCreditUnitsPerMillion == 0 || len(model.Capabilities) == 0 || len(model.Capabilities) > 32 {
			return ErrRegistryInvalid
		}
		if _, exists := models[key]; exists {
			return ErrRegistryInvalid
		}
		models[key] = struct{}{}
		capabilities := make(map[string]struct{}, len(model.Capabilities))
		for _, capability := range model.Capabilities {
			if !registryIDPattern.MatchString(capability) {
				return ErrRegistryInvalid
			}
			if _, exists := capabilities[capability]; exists {
				return ErrRegistryInvalid
			}
			capabilities[capability] = struct{}{}
		}
	}
	return nil
}

func validCredentialDefinition(providerType string, credential ManagedCredential) bool {
	if credential.SecretRef == "" || credential.SecretVersion == "" || strings.ContainsAny(credential.SecretRef+credential.SecretVersion, "\x00\r\n") {
		return false
	}
	switch credential.Mode {
	case "bearer":
		return providerType == "openai" || providerType == "openai_compatible"
	case "x-api-key":
		return providerType == "anthropic" || providerType == "openai_compatible"
	default:
		return false
	}
}
