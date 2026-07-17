package toolreconciler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"

	"github.com/langshift/lites/internal/objectstore/s3store"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/toolregistry"
)

var (
	ErrArtifactExportLookup = errors.New("Artifact export reconciliation lookup failed")
	artifactExportUUIDRE    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	artifactExportDigestRE  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type ScannedVersionedInspector interface {
	InspectScannedVersioned(context.Context, string, string, string, int64) (s3store.VersionedObject, error)
}

// ArtifactExportLookup reconciles only the exact object key and metadata bound
// to the original encrypted normalized input and provider request ID. It never
// replays PutObject and never treats an incomplete receipt as success.
type ArtifactExportLookup struct {
	Payloads payload.Store
	Objects  ScannedVersionedInspector
	MaxBytes int64
}

type artifactExportLookupInput struct {
	SchemaVersion int    `json:"schema_version"`
	TargetKind    string `json:"target_kind"`
	TargetID      string `json:"target_id"`
	MediaType     string `json:"media_type"`
	ContentBase64 string `json:"content_base64"`
	ContentHash   string `json:"content_sha256"`
}

type artifactExportLookupEvidence struct {
	SchemaVersion  int    `json:"schema_version"`
	TargetKind     string `json:"target_kind"`
	TargetID       string `json:"target_id"`
	ObjectRef      string `json:"object_ref"`
	ObjectVersion  string `json:"object_version"`
	ContentHash    string `json:"content_hash"`
	MediaType      string `json:"media_type"`
	ByteSize       int64  `json:"byte_size"`
	ScanResultHash string `json:"scan_result_hash"`
}

func (lookup ArtifactExportLookup) Lookup(ctx context.Context, request LookupRequest) (LookupResult, error) {
	command := request.Command
	if lookup.Payloads == nil || lookup.Objects == nil || lookup.MaxBytes < 1 || lookup.MaxBytes > 1<<30 || request.Snapshot.Descriptor.Handler != "artifact_export" || command.ToolName != "artifact_export" || command.Input.Ref == "" || command.Input.Hash == "" || !artifactExportUUIDRE.MatchString(command.ProviderRequestID) {
		return LookupResult{}, ErrArtifactExportLookup
	}
	encoded, err := lookup.Payloads.Get(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: command.ToolCallID, Class: "tool-normalized-input", ContentType: "application/json"}, command.Input)
	if err != nil || len(encoded) == 0 || len(encoded) > request.Snapshot.Descriptor.Resources.MaximumInput {
		return LookupResult{}, errors.Join(ErrArtifactExportLookup, err)
	}
	normalized, _, requestHash, err := request.SnapshotRegistryNormalize(encoded, command)
	if err != nil || !bytes.Equal(normalized, encoded) || requestHash != command.RequestHash {
		return LookupResult{}, errors.Join(ErrArtifactExportLookup, err)
	}
	input, byteSize, err := decodeArtifactExportLookupInput(encoded, lookup.MaxBytes)
	if err != nil {
		return LookupResult{}, err
	}
	extension := map[string]string{"text/html": ".html", "application/pdf": ".pdf", "application/zip": ".zip"}[input.MediaType]
	key := command.TenantID + "/portfolio-exports/" + input.TargetID + "/" + command.ProviderRequestID + extension
	object, err := lookup.Objects.InspectScannedVersioned(ctx, key, input.MediaType, input.ContentHash, byteSize)
	if errors.Is(err, s3store.ErrNotFound) {
		evidence, marshalErr := json.Marshal(map[string]any{"schema_version": 1, "status": "not_applied", "provider_request_id": command.ProviderRequestID})
		return LookupResult{Disposition: LookupNotApplied, Evidence: evidence}, marshalErr
	}
	if err != nil {
		return LookupResult{}, err
	}
	if object.Reference == "" || object.VersionID == "" || object.ContentHash != input.ContentHash || object.MediaType != input.MediaType || object.ByteSize != byteSize || !artifactExportDigestRE.MatchString(object.ScanResultHash) {
		return LookupResult{}, ErrArtifactExportLookup
	}
	evidence, err := json.Marshal(artifactExportLookupEvidence{SchemaVersion: 1, TargetKind: input.TargetKind, TargetID: input.TargetID, ObjectRef: object.Reference, ObjectVersion: object.VersionID, ContentHash: object.ContentHash, MediaType: object.MediaType, ByteSize: object.ByteSize, ScanResultHash: object.ScanResultHash})
	if err != nil {
		return LookupResult{}, err
	}
	return LookupResult{Disposition: LookupConfirmed, Evidence: evidence, ExternalResourceRef: object.Reference}, nil
}

// SnapshotRegistryNormalize is kept on the request so the lookup cannot use a
// registry other than the one already hash-resolved by RegistryLookup.
func (request LookupRequest) SnapshotRegistryNormalize(input json.RawMessage, command CommandPayload) (json.RawMessage, string, string, error) {
	// Recompile the one exact descriptor to reuse the registry's canonical input
	// validation and request-hash algorithm without widening LookupRequest.
	registry, err := toolregistry.New([]toolregistry.Descriptor{request.Snapshot.Descriptor})
	if err != nil {
		return nil, "", "", err
	}
	return registry.NormalizeInput(command.DescriptorSnapshotID, command.DescriptorHash, input)
}

func decodeArtifactExportLookupInput(encoded json.RawMessage, maximum int64) (artifactExportLookupInput, int64, error) {
	var input artifactExportLookupInput
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || input.SchemaVersion != 1 || input.TargetKind != "portfolio_export" || !artifactExportUUIDRE.MatchString(input.TargetID) || !artifactExportDigestRE.MatchString(input.ContentHash) || input.MediaType != "text/html" && input.MediaType != "application/pdf" && input.MediaType != "application/zip" || input.ContentBase64 == "" || int64(len(input.ContentBase64)) > int64(base64.StdEncoding.EncodedLen(int(maximum))) {
		return artifactExportLookupInput{}, 0, ErrArtifactExportLookup
	}
	contents, err := base64.StdEncoding.Strict().DecodeString(input.ContentBase64)
	if err != nil || len(contents) == 0 || int64(len(contents)) > maximum {
		return artifactExportLookupInput{}, 0, ErrArtifactExportLookup
	}
	digest := sha256.Sum256(contents)
	if hex.EncodeToString(digest[:]) != input.ContentHash {
		return artifactExportLookupInput{}, 0, ErrArtifactExportLookup
	}
	return input, int64(len(contents)), nil
}
