package toolreconciler

import (
	"context"
	"errors"
	"reflect"

	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/toolregistry"
)

var (
	ErrLookupConfiguration = errors.New("tool reconciliation lookup registry is invalid")
	ErrLookupBinding       = errors.New("tool reconciliation lookup binding is invalid")
)

type LookupDisposition string

const (
	LookupConfirmed    LookupDisposition = "confirmed"
	LookupNotApplied   LookupDisposition = "not_applied"
	LookupInconclusive LookupDisposition = "inconclusive"
)

type LookupRequest struct {
	Command  CommandPayload
	Claim    executionpostgres.ReconciliationClaim
	Snapshot toolregistry.Snapshot
}

type LookupResult struct {
	Disposition         LookupDisposition `json:"disposition"`
	Evidence            []byte            `json:"evidence"`
	ExternalResourceRef string            `json:"external_resource_ref,omitempty"`
}

type EffectLookup interface {
	Lookup(context.Context, LookupRequest) (LookupResult, error)
}

type LookupRegistration struct {
	Name   string
	Lookup EffectLookup
}

type LookupExecutor interface {
	Execute(context.Context, LookupRequest) (LookupResult, error)
}

type RegistryLookup struct {
	registry *toolregistry.Registry
	lookups  map[string]EffectLookup
}

// NewRegistryLookup requires exact adapter coverage for every automatically
// reconcilable descriptor in the release-pinned registry. Catalog/image drift
// therefore fails at startup instead of stranding outcome-unknown effects.
func NewRegistryLookup(registry *toolregistry.Registry, registrations []LookupRegistration) (*RegistryLookup, error) {
	if registry == nil || registry.Hash() == "" {
		return nil, ErrLookupConfiguration
	}
	required := map[string]struct{}{}
	for _, snapshot := range registry.Snapshots() {
		if snapshot.Descriptor.EffectClass == automaticallyReconcilableEffectClass {
			if !snapshot.Descriptor.SupportsReconcile || snapshot.Descriptor.Handler == "" {
				return nil, ErrLookupConfiguration
			}
			required[snapshot.Descriptor.Handler] = struct{}{}
		}
	}
	lookups := make(map[string]EffectLookup, len(registrations))
	for _, registration := range registrations {
		if _, needed := required[registration.Name]; !needed || registration.Name == "" || nilLookup(registration.Lookup) {
			return nil, ErrLookupConfiguration
		}
		if _, duplicate := lookups[registration.Name]; duplicate {
			return nil, ErrLookupConfiguration
		}
		lookups[registration.Name] = registration.Lookup
	}
	if len(lookups) != len(required) {
		return nil, ErrLookupConfiguration
	}
	return &RegistryLookup{registry: registry, lookups: lookups}, nil
}

func (executor *RegistryLookup) Execute(ctx context.Context, request LookupRequest) (LookupResult, error) {
	if executor == nil || executor.registry == nil {
		return LookupResult{}, ErrLookupConfiguration
	}
	snapshot, err := executor.registry.Resolve(request.Command.DescriptorSnapshotID, request.Command.DescriptorHash)
	if err != nil || !matchesLookup(snapshot, request) {
		return LookupResult{}, errors.Join(ErrLookupBinding, err)
	}
	lookup, ok := executor.lookups[snapshot.Descriptor.Handler]
	if !ok || nilLookup(lookup) {
		return LookupResult{}, ErrLookupConfiguration
	}
	request.Snapshot = snapshot
	return invokeLookup(ctx, lookup, request)
}

func matchesLookup(snapshot toolregistry.Snapshot, request LookupRequest) bool {
	command, claim, descriptor := request.Command, request.Claim, snapshot.Descriptor
	return descriptor.Name == command.ToolName && descriptor.EffectClass == automaticallyReconcilableEffectClass && descriptor.SupportsReconcile &&
		claim.ToolCallID == command.ToolCallID && claim.RunID == command.RunID && claim.EffectID == command.EffectID && claim.ToolCallVersion == command.TerminalToolVersion &&
		claim.EffectClass == command.EffectClass && claim.EffectKey == command.EffectKey && claim.EffectScope == command.EffectScope && claim.ProviderID == command.ProviderID && claim.ProviderRequestID == command.ProviderRequestID
}

func invokeLookup(ctx context.Context, lookup EffectLookup, request LookupRequest) (result LookupResult, err error) {
	defer func() {
		if recover() != nil {
			result = LookupResult{}
			err = ErrLookupConfiguration
		}
	}()
	return lookup.Lookup(ctx, request)
}

func nilLookup(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
