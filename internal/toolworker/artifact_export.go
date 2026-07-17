package toolworker

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
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/malware/clamav"
	"github.com/langshift/lites/internal/objectstore/s3store"
)

var (
	ErrArtifactExportConfiguration = errors.New("artifact export handler is not configured")
	ErrArtifactExportAuthorization = errors.New("artifact export is not authorized")
	artifactExportUUIDPattern      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	artifactExportHashPattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type ArtifactScanner interface {
	Scan(context.Context, []byte) (clamav.Receipt, error)
}

type VersionedArtifactStore interface {
	PutScannedVersioned(context.Context, string, string, []byte, string) (s3store.VersionedObject, error)
}

type ArtifactExportAuthorization struct {
	ObjectKeyPrefix string
}

type ArtifactExportAuthorizer interface {
	Authorize(context.Context, string, string, string, string, string, string) (ArtifactExportAuthorization, error)
}

type ArtifactExportHandler struct {
	Authorizer ArtifactExportAuthorizer
	Scanner    ArtifactScanner
	Objects    VersionedArtifactStore
	MaxBytes   int64
}

type artifactExportInput struct {
	SchemaVersion int    `json:"schema_version"`
	TargetKind    string `json:"target_kind"`
	TargetID      string `json:"target_id"`
	MediaType     string `json:"media_type"`
	ContentBase64 string `json:"content_base64"`
	ContentHash   string `json:"content_sha256"`
}

type artifactExportOutput struct {
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

func (handler ArtifactExportHandler) Invoke(ctx context.Context, invocation Invocation) (HandlerResult, error) {
	if handler.Authorizer == nil || handler.Scanner == nil || handler.Objects == nil || handler.MaxBytes < 1 || handler.MaxBytes > 1<<30 {
		return HandlerResult{}, ErrArtifactExportConfiguration
	}
	input, contents, err := decodeArtifactExportInput(invocation.Input, handler.MaxBytes)
	if err != nil || !artifactExportUUIDPattern.MatchString(invocation.ProviderRequestID) {
		return HandlerResult{Status: ResultFailed, Failure: Failure{Code: "invalid_artifact_export_input", SafeMessage: "The Artifact export request is invalid."}}, nil
	}
	authorization, err := handler.Authorizer.Authorize(ctx, invocation.TenantID, invocation.UserID, invocation.RunID, input.TargetKind, input.TargetID, input.MediaType)
	if err != nil || authorization.ObjectKeyPrefix == "" {
		if errors.Is(err, ErrArtifactExportAuthorization) {
			return HandlerResult{Status: ResultFailed, Failure: Failure{Code: "artifact_export_not_authorized", SafeMessage: "The Run is not authorized for this Artifact export."}}, nil
		}
		return HandlerResult{}, BeforeEffect(err)
	}
	receipt, err := handler.Scanner.Scan(ctx, contents)
	if err != nil {
		return HandlerResult{}, BeforeEffect(err)
	}
	if receipt.ContentHash != input.ContentHash || receipt.ByteSize != int64(len(contents)) {
		return HandlerResult{}, BeforeEffect(clamav.ErrProtocol)
	}
	if receipt.Status == "infected" {
		return HandlerResult{Status: ResultFailed, Failure: Failure{Code: "unsafe_artifact", SafeMessage: "The Artifact was rejected by the content safety scanner."}}, nil
	}
	if receipt.Status != "clean" {
		return HandlerResult{}, BeforeEffect(clamav.ErrProtocol)
	}
	scanHash, err := receipt.Hash()
	if err != nil {
		return HandlerResult{}, BeforeEffect(err)
	}
	extension := map[string]string{"text/html": ".html", "application/pdf": ".pdf", "application/zip": ".zip"}[input.MediaType]
	object, err := handler.Objects.PutScannedVersioned(ctx, strings.TrimSuffix(authorization.ObjectKeyPrefix, "/")+"/"+invocation.ProviderRequestID+extension, input.MediaType, contents, scanHash)
	if err != nil {
		// PutVersioned may have crossed the S3 effect boundary. Returning the raw
		// error deliberately drives reconcilable_write to outcome_unknown.
		return HandlerResult{}, err
	}
	if object.ContentHash != input.ContentHash || object.MediaType != input.MediaType || object.ByteSize != int64(len(contents)) || object.ScanResultHash != scanHash || object.Reference == "" || object.VersionID == "" {
		return HandlerResult{}, errors.New("artifact object receipt mismatch")
	}
	encoded, err := json.Marshal(artifactExportOutput{SchemaVersion: 1, TargetKind: input.TargetKind, TargetID: input.TargetID, ObjectRef: object.Reference, ObjectVersion: object.VersionID, ContentHash: object.ContentHash, MediaType: object.MediaType, ByteSize: object.ByteSize, ScanResultHash: scanHash})
	if err != nil {
		return HandlerResult{}, err
	}
	return HandlerResult{Status: ResultSucceeded, Output: encoded, ExternalResourceRef: object.Reference}, nil
}

func decodeArtifactExportInput(encoded json.RawMessage, maximum int64) (artifactExportInput, []byte, error) {
	var input artifactExportInput
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || input.SchemaVersion != 1 || input.TargetKind != "portfolio_export" || !artifactExportUUIDPattern.MatchString(input.TargetID) || !artifactExportHashPattern.MatchString(input.ContentHash) || input.MediaType != "text/html" && input.MediaType != "application/pdf" && input.MediaType != "application/zip" || input.ContentBase64 == "" || int64(len(input.ContentBase64)) > int64(base64.StdEncoding.EncodedLen(int(maximum))+4) {
		return artifactExportInput{}, nil, ErrHandlerResult
	}
	contents, err := base64.StdEncoding.Strict().DecodeString(input.ContentBase64)
	if err != nil || len(contents) == 0 || int64(len(contents)) > maximum {
		return artifactExportInput{}, nil, ErrHandlerResult
	}
	digest := sha256.Sum256(contents)
	if hex.EncodeToString(digest[:]) != input.ContentHash {
		return artifactExportInput{}, nil, ErrHandlerResult
	}
	return input, contents, nil
}

type PostgresArtifactExportAuthorizer struct{ Pool *pgxpool.Pool }

func (authorizer PostgresArtifactExportAuthorizer) Authorize(ctx context.Context, tenantID, userID, runID, targetKind, targetID, mediaType string) (ArtifactExportAuthorization, error) {
	if authorizer.Pool == nil || targetKind != "portfolio_export" {
		return ArtifactExportAuthorization{}, ErrArtifactExportConfiguration
	}
	tx, err := authorizer.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return ArtifactExportAuthorization{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return ArtifactExportAuthorization{}, err
	}
	var admissible bool
	err = tx.QueryRow(ctx, `SELECT agent.lock_authorized_artifact_export($1,$2,$3,$4,$5,$6)`, tenantID, userID, runID, targetKind, targetID, mediaType).Scan(&admissible)
	if err != nil {
		return ArtifactExportAuthorization{}, err
	}
	if !admissible {
		return ArtifactExportAuthorization{}, ErrArtifactExportAuthorization
	}
	if err = tx.Commit(ctx); err != nil {
		return ArtifactExportAuthorization{}, err
	}
	return ArtifactExportAuthorization{ObjectKeyPrefix: tenantID + "/portfolio-exports/" + targetID}, nil
}
