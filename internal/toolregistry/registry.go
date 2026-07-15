// Package toolregistry provides the immutable production registry shared by
// AgentWorker and ToolWorker. A descriptor hash covers the LLM schema and all
// execution authority: effects, permissions, secrets, runtime and egress.
package toolregistry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/langshift/lites/internal/llmgateway/provider"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

var (
	ErrRegistryInvalid   = errors.New("tool registry is invalid")
	ErrDescriptorInvalid = errors.New("tool descriptor is invalid")
	ErrToolNotFound      = errors.New("tool descriptor snapshot is not available")
	ErrSchemaInvalid     = errors.New("tool JSON schema is invalid")
	ErrInputInvalid      = errors.New("tool input does not match its descriptor")
	ErrOutputInvalid     = errors.New("tool output does not match its descriptor")
)

const (
	ExecutionWorker = "worker"
	ExecutionChild  = "child_run"

	ApprovalNone    = "none"
	ApprovalDirect  = "direct"
	ApprovalPreview = "preview"
)

type ResourceLimits struct {
	CPUMillis       int64  `json:"cpu_millis"`
	MemoryBytes     int64  `json:"memory_bytes"`
	DiskBytes       int64  `json:"disk_bytes"`
	Timeout         string `json:"timeout"`
	MaximumInput    int    `json:"maximum_input_bytes"`
	MaximumOutput   int    `json:"maximum_output_bytes"`
	MaximumLogBytes int64  `json:"maximum_log_bytes"`
}

type Scheduling struct {
	QueueClass    string `json:"queue_class"`
	ResourceClass string `json:"resource_class"`
	Priority      int    `json:"priority"`
	CostUnits     int64  `json:"cost_units"`
}

type Descriptor struct {
	SchemaVersion int    `json:"schema_version"`
	Name          string `json:"tool_name"`
	Version       string `json:"tool_version"`
	DisplayName   string `json:"display_name"`
	Description   string `json:"description"`
	Category      string `json:"category"`

	InputSchema   json.RawMessage   `json:"input_schema"`
	OutputSchema  json.RawMessage   `json:"output_schema"`
	InputExamples []json.RawMessage `json:"input_examples,omitempty"`

	EffectClass          string `json:"effect_class"`
	RequiresEffectKey    bool   `json:"requires_effect_key"`
	SupportsReconcile    bool   `json:"supports_reconcile"`
	SupportsCompensation bool   `json:"supports_compensation"`
	MaxAttempts          int    `json:"max_attempts"`
	ReconcileAfter       string `json:"reconcile_after,omitempty"`

	RequiredPermissions []string `json:"required_permissions,omitempty"`
	ApprovalMode        string   `json:"approval_mode"`
	ApprovalHint        string   `json:"approval_hint,omitempty"`
	SecretScopes        []string `json:"secret_scopes,omitempty"`

	ExecutionKind       string         `json:"execution_kind"`
	Handler             string         `json:"handler,omitempty"`
	TrustTier           string         `json:"trust_tier"`
	RuntimeImage        string         `json:"runtime_image,omitempty"`
	Resources           ResourceLimits `json:"resource_limits"`
	Scheduling          Scheduling     `json:"scheduling"`
	NetworkEgressPolicy string         `json:"network_egress_policy"`
	EgressAllowlist     []string       `json:"egress_allowlist,omitempty"`

	Source        string `json:"source"`
	Deprecated    bool   `json:"deprecated"`
	SuccessorTool string `json:"successor_tool,omitempty"`
	Changelog     string `json:"changelog"`
}

type Snapshot struct {
	Descriptor     Descriptor
	SnapshotID     string
	Hash           string
	InputHash      string
	OutputHash     string
	InputSchema    *jsonschema.Schema
	OutputSchema   *jsonschema.Schema
	ReconcileAfter time.Duration
}

type Registry struct {
	bySnapshot map[string]Snapshot
	byName     map[string][]Snapshot
	hash       string
}

var (
	toolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	versionPattern  = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
	idPattern       = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,127}$`)
	digestPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	imagePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*(?::[a-zA-Z0-9._-]+)?@sha256:[0-9a-f]{64}$`)
)

func New(descriptors []Descriptor) (Registry, error) {
	if len(descriptors) == 0 || len(descriptors) > 4096 {
		return Registry{}, ErrRegistryInvalid
	}
	registry := Registry{bySnapshot: make(map[string]Snapshot, len(descriptors)), byName: map[string][]Snapshot{}}
	hashes := make([]string, 0, len(descriptors))
	for _, declared := range descriptors {
		snapshot, err := compileDescriptor(declared)
		if err != nil {
			return Registry{}, err
		}
		if _, exists := registry.bySnapshot[snapshot.SnapshotID]; exists {
			return Registry{}, ErrRegistryInvalid
		}
		registry.bySnapshot[snapshot.SnapshotID] = snapshot
		registry.byName[snapshot.Descriptor.Name] = append(registry.byName[snapshot.Descriptor.Name], snapshot)
		hashes = append(hashes, snapshot.SnapshotID+"\x00"+snapshot.Hash)
	}
	for name := range registry.byName {
		sort.Slice(registry.byName[name], func(left, right int) bool {
			return registry.byName[name][left].Descriptor.Version < registry.byName[name][right].Descriptor.Version
		})
	}
	sort.Strings(hashes)
	digest := sha256.Sum256([]byte(strings.Join(hashes, "\n")))
	registry.hash = hex.EncodeToString(digest[:])
	return registry, nil
}

func (registry Registry) Hash() string { return registry.hash }

// Snapshots returns the code-declared immutable catalog in deterministic
// order. Runtime authorization must still call Resolve with an expected hash.
func (registry Registry) Snapshots() []Snapshot {
	result := make([]Snapshot, 0, len(registry.bySnapshot))
	for _, snapshot := range registry.bySnapshot {
		result = append(result, cloneSnapshot(snapshot))
	}
	sort.Slice(result, func(left, right int) bool { return result[left].SnapshotID < result[right].SnapshotID })
	return result
}

func (registry Registry) Resolve(snapshotID, expectedHash string) (Snapshot, error) {
	snapshot, ok := registry.bySnapshot[snapshotID]
	if !ok || expectedHash == "" || !digestPattern.MatchString(expectedHash) || snapshot.Hash != expectedHash {
		return Snapshot{}, ErrToolNotFound
	}
	return cloneSnapshot(snapshot), nil
}

func (registry Registry) LLMTool(snapshotID, expectedHash string) (provider.Tool, error) {
	snapshot, err := registry.Resolve(snapshotID, expectedHash)
	if err != nil {
		return provider.Tool{}, err
	}
	return provider.Tool{Name: snapshot.Descriptor.Name, Description: snapshot.Descriptor.Description, InputSchema: append(json.RawMessage(nil), snapshot.Descriptor.InputSchema...), Strict: true}, nil
}

// NormalizeInput validates an untrusted model call and returns stable JSON.
// RequestHash binds both the full descriptor hash and normalized input hash.
func (registry Registry) NormalizeInput(snapshotID, expectedHash string, input json.RawMessage) (normalized json.RawMessage, inputHash, requestHash string, err error) {
	snapshot, err := registry.Resolve(snapshotID, expectedHash)
	if err != nil {
		return nil, "", "", err
	}
	normalized, value, err := canonicalInstance(input, snapshot.Descriptor.Resources.MaximumInput)
	if err != nil || snapshot.InputSchema.Validate(value) != nil {
		return nil, "", "", ErrInputInvalid
	}
	inputHash = hashBytes(normalized)
	requestHash = hashBytes([]byte(snapshot.Hash + "\x00" + inputHash))
	return normalized, inputHash, requestHash, nil
}

func (registry Registry) ValidateOutput(snapshotID, expectedHash string, output json.RawMessage) (json.RawMessage, string, error) {
	snapshot, err := registry.Resolve(snapshotID, expectedHash)
	if err != nil {
		return nil, "", err
	}
	normalized, value, err := canonicalInstance(output, snapshot.Descriptor.Resources.MaximumOutput)
	if err != nil || snapshot.OutputSchema.Validate(value) != nil {
		return nil, "", ErrOutputInvalid
	}
	return normalized, hashBytes(normalized), nil
}

func compileDescriptor(declared Descriptor) (Snapshot, error) {
	descriptor, reconcileAfter, err := canonicalDescriptor(declared)
	if err != nil {
		return Snapshot{}, err
	}
	inputSchema, inputHash, err := compileSchema(descriptor.Name+"/"+descriptor.Version+"/input", descriptor.InputSchema)
	if err != nil {
		return Snapshot{}, err
	}
	outputSchema, outputHash, err := compileSchema(descriptor.Name+"/"+descriptor.Version+"/output", descriptor.OutputSchema)
	if err != nil {
		return Snapshot{}, err
	}
	descriptor.InputSchema, descriptor.OutputSchema = canonicalSchemaBytes(descriptor.InputSchema), canonicalSchemaBytes(descriptor.OutputSchema)
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		return Snapshot{}, ErrDescriptorInvalid
	}
	hash := hashBytes(encoded)
	return Snapshot{Descriptor: descriptor, SnapshotID: descriptor.Name + "@" + descriptor.Version, Hash: hash, InputHash: inputHash, OutputHash: outputHash, InputSchema: inputSchema, OutputSchema: outputSchema, ReconcileAfter: reconcileAfter}, nil
}

func canonicalDescriptor(value Descriptor) (Descriptor, time.Duration, error) {
	if value.SchemaVersion != 1 || !toolNamePattern.MatchString(value.Name) || !versionPattern.MatchString(value.Version) || strings.TrimSpace(value.DisplayName) == "" || len(value.DisplayName) > 128 || strings.TrimSpace(value.Description) == "" || len(value.Description) > 4096 || !idPattern.MatchString(value.Category) || len(value.InputExamples) > 32 || !validEffect(value) || !validApproval(value) || !validRuntime(value) || !validSource(value) {
		return Descriptor{}, 0, ErrDescriptorInvalid
	}
	canonical := value
	canonical.DisplayName, canonical.Description = strings.TrimSpace(value.DisplayName), strings.TrimSpace(value.Description)
	canonical.ApprovalHint = strings.TrimSpace(value.ApprovalHint)
	timeout, err := time.ParseDuration(value.Resources.Timeout)
	if err != nil {
		return Descriptor{}, 0, ErrDescriptorInvalid
	}
	canonical.Resources.Timeout = timeout.String()
	canonical.RequiredPermissions, err = canonicalStrings(value.RequiredPermissions, 64)
	if err != nil {
		return Descriptor{}, 0, ErrDescriptorInvalid
	}
	canonical.SecretScopes, err = canonicalStrings(value.SecretScopes, 64)
	if err != nil {
		return Descriptor{}, 0, ErrDescriptorInvalid
	}
	canonical.EgressAllowlist, err = canonicalHosts(value.EgressAllowlist)
	if err != nil {
		return Descriptor{}, 0, ErrDescriptorInvalid
	}
	canonical.InputSchema, err = canonicalJSON(value.InputSchema, 256<<10)
	if err != nil {
		return Descriptor{}, 0, ErrSchemaInvalid
	}
	canonical.OutputSchema, err = canonicalJSON(value.OutputSchema, 256<<10)
	if err != nil {
		return Descriptor{}, 0, ErrSchemaInvalid
	}
	canonical.InputExamples = make([]json.RawMessage, len(value.InputExamples))
	for index, example := range value.InputExamples {
		canonical.InputExamples[index], _, err = canonicalInstance(example, value.Resources.MaximumInput)
		if err != nil {
			return Descriptor{}, 0, ErrDescriptorInvalid
		}
	}
	reconcileAfter := time.Duration(0)
	if value.ReconcileAfter != "" {
		reconcileAfter, err = time.ParseDuration(value.ReconcileAfter)
		if err != nil || reconcileAfter < time.Second || reconcileAfter > 24*time.Hour || !value.SupportsReconcile {
			return Descriptor{}, 0, ErrDescriptorInvalid
		}
	} else if value.SupportsReconcile {
		return Descriptor{}, 0, ErrDescriptorInvalid
	}
	if reconcileAfter > 0 {
		canonical.ReconcileAfter = reconcileAfter.String()
	}
	return canonical, reconcileAfter, nil
}

func validEffect(value Descriptor) bool {
	if value.MaxAttempts < 1 || value.MaxAttempts > 100 {
		return false
	}
	switch value.EffectClass {
	case "read_only":
		return !value.RequiresEffectKey && !value.SupportsReconcile && !value.SupportsCompensation && value.ReconcileAfter == ""
	case "idempotent_write":
		return value.RequiresEffectKey && !value.SupportsCompensation
	case "reconcilable_write":
		return value.RequiresEffectKey && value.SupportsReconcile && !value.SupportsCompensation
	case "compensatable_write":
		return value.RequiresEffectKey && value.SupportsReconcile && value.SupportsCompensation
	case "irreversible_write":
		return value.RequiresEffectKey && !value.SupportsCompensation
	default:
		return false
	}
}

func validApproval(value Descriptor) bool {
	switch value.ApprovalMode {
	case ApprovalNone:
		return value.ApprovalHint == ""
	case ApprovalDirect, ApprovalPreview:
		return strings.TrimSpace(value.ApprovalHint) != "" && len(value.ApprovalHint) <= 2048
	default:
		return false
	}
}

func validRuntime(value Descriptor) bool {
	resources, schedule := value.Resources, value.Scheduling
	if resources.CPUMillis < 10 || resources.CPUMillis > 128_000 || resources.MemoryBytes < 16<<20 || resources.MemoryBytes > 1<<40 || resources.DiskBytes < 0 || resources.DiskBytes > 10<<40 || resources.MaximumInput < 1 || resources.MaximumInput > 16<<20 || resources.MaximumOutput < 1024 || resources.MaximumOutput > 64<<20 || resources.MaximumLogBytes < 1024 || resources.MaximumLogBytes > 1<<30 || schedule.QueueClass != "interactive" && schedule.QueueClass != "background" || schedule.ResourceClass == "" || schedule.Priority < 0 || schedule.Priority > 1000 || schedule.CostUnits < 1 || schedule.CostUnits > 1_000_000_000_000 {
		return false
	}
	timeout, err := time.ParseDuration(resources.Timeout)
	if err != nil || timeout < time.Second || timeout > time.Hour {
		return false
	}
	switch value.TrustTier {
	case "trusted", "semi_trusted", "untrusted", "privileged":
	default:
		return false
	}
	if value.ExecutionKind == ExecutionChild {
		return value.Name == "spawn_agent_run" && value.EffectClass == "read_only" && value.Handler == "spawn_agent_run" && value.RuntimeImage == "" && len(value.SecretScopes) == 0 && value.NetworkEgressPolicy == "deny_all" && len(value.EgressAllowlist) == 0 && value.TrustTier == "trusted"
	}
	if value.ExecutionKind != ExecutionWorker || !idPattern.MatchString(value.Handler) || !imagePattern.MatchString(value.RuntimeImage) {
		return false
	}
	switch value.NetworkEgressPolicy {
	case "deny_all", "tenant_policy":
		return len(value.EgressAllowlist) == 0
	case "allowlist":
		return len(value.EgressAllowlist) > 0
	default:
		return false
	}
}

func validSource(value Descriptor) bool {
	if value.SuccessorTool != "" && (!value.Deprecated || !toolNamePattern.MatchString(value.SuccessorTool)) || len(value.Changelog) > 8192 {
		return false
	}
	switch value.Source {
	case "platform", "tenant_custom", "marketplace":
		return true
	default:
		return false
	}
}

func canonicalStrings(values []string, maximum int) ([]string, error) {
	result := append([]string(nil), values...)
	if len(result) > maximum {
		return nil, ErrDescriptorInvalid
	}
	for _, value := range result {
		if !idPattern.MatchString(value) {
			return nil, ErrDescriptorInvalid
		}
	}
	sort.Strings(result)
	for index := 1; index < len(result); index++ {
		if result[index-1] == result[index] {
			return nil, ErrDescriptorInvalid
		}
	}
	return result, nil
}

func canonicalHosts(values []string) ([]string, error) {
	if len(values) > 128 {
		return nil, ErrDescriptorInvalid
	}
	result := make([]string, len(values))
	for index, value := range values {
		host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
		if host == "" || len(host) > 253 || net.ParseIP(host) != nil || strings.ContainsAny(host, "_/:*\\\x00\r\n") || !validDNSName(host) {
			return nil, ErrDescriptorInvalid
		}
		result[index] = host
	}
	sort.Strings(result)
	for index := 1; index < len(result); index++ {
		if result[index-1] == result[index] {
			return nil, ErrDescriptorInvalid
		}
	}
	return result, nil
}

func validDNSName(host string) bool {
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
				return false
			}
		}
	}
	return true
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	clone := snapshot
	clone.Descriptor.InputSchema = append(json.RawMessage(nil), snapshot.Descriptor.InputSchema...)
	clone.Descriptor.OutputSchema = append(json.RawMessage(nil), snapshot.Descriptor.OutputSchema...)
	clone.Descriptor.RequiredPermissions = append([]string(nil), snapshot.Descriptor.RequiredPermissions...)
	clone.Descriptor.SecretScopes = append([]string(nil), snapshot.Descriptor.SecretScopes...)
	clone.Descriptor.EgressAllowlist = append([]string(nil), snapshot.Descriptor.EgressAllowlist...)
	clone.Descriptor.InputExamples = make([]json.RawMessage, len(snapshot.Descriptor.InputExamples))
	for index := range snapshot.Descriptor.InputExamples {
		clone.Descriptor.InputExamples[index] = append(json.RawMessage(nil), snapshot.Descriptor.InputExamples[index]...)
	}
	return clone
}

func hashBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func (snapshot Snapshot) String() string {
	return fmt.Sprintf("%s#sha256:%s", snapshot.SnapshotID, snapshot.Hash)
}
