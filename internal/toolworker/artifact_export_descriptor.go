package toolworker

import (
	"encoding/json"

	"github.com/langshift/lites/internal/toolregistry"
)

// ArtifactExportMaximumBytes keeps the base64 payload plus the closed JSON
// envelope below the registry's 16 MiB normalized-input ceiling. This is a
// reviewed product limit, not a best-effort transport default.
const ArtifactExportMaximumBytes int64 = 11 << 20

// ArtifactExportDescriptor returns the exact trusted-worker authority that
// must be included in every release serving ArtifactBuilder Runs. The image is
// the signed ToolWorker image, even though trusted handlers execute in-process;
// pinning it prevents a registry from authorizing code other than the reviewed
// deployment artifact.
func ArtifactExportDescriptor(toolWorkerImage string) toolregistry.Descriptor {
	return toolregistry.Descriptor{
		SchemaVersion: 1,
		Name:          "artifact_export",
		Version:       "1.0.0",
		DisplayName:   "Export portfolio Artifact",
		Description:   "Scan and immutably store one completed portfolio export using the exact Project revision manifest bound to this Run.",
		Category:      "artifact",
		InputSchema: json.RawMessage(`{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object",
  "properties":{
    "schema_version":{"const":1},
    "target_kind":{"const":"portfolio_export"},
    "target_id":{"type":"string","pattern":"^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"},
    "media_type":{"enum":["text/html","application/pdf","application/zip"]},
    "content_base64":{"type":"string","minLength":4,"maxLength":15379116,"contentEncoding":"base64"},
    "content_sha256":{"type":"string","pattern":"^[0-9a-f]{64}$"}
  },
  "required":["schema_version","target_kind","target_id","media_type","content_base64","content_sha256"],
  "additionalProperties":false
}`),
		OutputSchema: json.RawMessage(`{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object",
  "properties":{
    "schema_version":{"const":1},
    "target_kind":{"const":"portfolio_export"},
    "target_id":{"type":"string","pattern":"^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"},
    "object_ref":{"type":"string","pattern":"^s3://[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]/[A-Za-z0-9][A-Za-z0-9._/-]*$","maxLength":2048},
    "object_version":{"type":"string","minLength":1,"maxLength":1024},
    "content_hash":{"type":"string","pattern":"^[0-9a-f]{64}$"},
    "media_type":{"enum":["text/html","application/pdf","application/zip"]},
    "byte_size":{"type":"integer","minimum":1,"maximum":11534336},
    "scan_result_hash":{"type":"string","pattern":"^[0-9a-f]{64}$"}
  },
  "required":["schema_version","target_kind","target_id","object_ref","object_version","content_hash","media_type","byte_size","scan_result_hash"],
  "additionalProperties":false
}`),
		EffectClass:       "reconcilable_write",
		RequiresEffectKey: true,
		SupportsReconcile: true,
		MaxAttempts:       5,
		ReconcileAfter:    "1m",
		RequiredPermissions: []string{
			"private_work.read",
			"private_work.update",
		},
		ApprovalMode:        toolregistry.ApprovalNone,
		ExecutionKind:       toolregistry.ExecutionWorker,
		Handler:             "artifact_export",
		TrustTier:           "trusted",
		RuntimeImage:        toolWorkerImage,
		Resources:           toolregistry.ResourceLimits{CPUMillis: 1000, MemoryBytes: 256 << 20, DiskBytes: 0, Timeout: "5m", MaximumInput: 16 << 20, MaximumOutput: 16 << 10, MaximumLogBytes: 1 << 20},
		Scheduling:          toolregistry.Scheduling{QueueClass: "background", ResourceClass: "artifact-export", Priority: 60, CostUnits: 4},
		NetworkEgressPolicy: "deny_all",
		Source:              "platform",
		Changelog:           "Initial production Artifact export with ClamAV scanning, immutable versioned object writes, and automatic effect reconciliation.",
	}
}
