package toolworker

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/langshift/lites/internal/malware/clamav"
	"github.com/langshift/lites/internal/objectstore/s3store"
)

type artifactAuthorizerStub struct{ err error }

func (stub artifactAuthorizerStub) Authorize(context.Context, string, string, string, string, string, string) (ArtifactExportAuthorization, error) {
	return ArtifactExportAuthorization{ObjectKeyPrefix: "tenant/portfolio-exports/export"}, stub.err
}

type artifactScannerStub struct {
	receipt clamav.Receipt
	err     error
}

func (stub artifactScannerStub) Scan(context.Context, []byte) (clamav.Receipt, error) {
	return stub.receipt, stub.err
}

type versionedStoreStub struct {
	object s3store.VersionedObject
	calls  int
}

func (stub *versionedStoreStub) PutScannedVersioned(_ context.Context, _ string, _ string, _ []byte, scanHash string) (s3store.VersionedObject, error) {
	stub.calls++
	stub.object.ScanResultHash = scanHash
	return stub.object, nil
}

func TestArtifactExportHandlerScansBeforeVersionedWrite(t *testing.T) {
	contents := []byte("trusted portfolio")
	digest := sha256.Sum256(contents)
	hash := hex.EncodeToString(digest[:])
	input, _ := json.Marshal(artifactExportInput{SchemaVersion: 1, TargetKind: "portfolio_export", TargetID: "a7000000-0000-4000-8000-000000000001", MediaType: "application/pdf", ContentBase64: base64.StdEncoding.EncodeToString(contents), ContentHash: hash})
	receipt := clamav.Receipt{SchemaVersion: 1, Engine: "clamav-instream", Status: "clean", ContentHash: hash, ByteSize: int64(len(contents))}
	objects := &versionedStoreStub{object: s3store.VersionedObject{Reference: "s3://artifacts/object", VersionID: "version-1", ContentHash: hash, MediaType: "application/pdf", ByteSize: int64(len(contents))}}
	handler := ArtifactExportHandler{Authorizer: artifactAuthorizerStub{}, Scanner: artifactScannerStub{receipt: receipt}, Objects: objects, MaxBytes: 1024}
	result, err := handler.Invoke(context.Background(), Invocation{TenantID: "tenant", UserID: "user", RunID: "run", ProviderRequestID: "a7000000-0000-4000-8000-000000000002", Input: input})
	if err != nil || result.Status != ResultSucceeded || result.ExternalResourceRef != objects.object.Reference || objects.calls != 1 {
		t.Fatalf("result=%#v calls=%d err=%v", result, objects.calls, err)
	}
	var output artifactExportOutput
	if json.Unmarshal(result.Output, &output) != nil || output.ObjectVersion != "version-1" || output.ScanResultHash == "" || output.ContentHash != hash {
		t.Fatalf("output=%s", result.Output)
	}
}

func TestArtifactExportHandlerRejectsInfectedAndUnauthorizedBeforeWrite(t *testing.T) {
	contents := []byte("unsafe")
	digest := sha256.Sum256(contents)
	hash := hex.EncodeToString(digest[:])
	input, _ := json.Marshal(artifactExportInput{SchemaVersion: 1, TargetKind: "portfolio_export", TargetID: "a7000000-0000-4000-8000-000000000003", MediaType: "text/html", ContentBase64: base64.StdEncoding.EncodeToString(contents), ContentHash: hash})
	objects := &versionedStoreStub{}
	infected := clamav.Receipt{SchemaVersion: 1, Engine: "clamav-instream", Status: "infected", Signature: "Eicar-Test-Signature", ContentHash: hash, ByteSize: int64(len(contents))}
	handler := ArtifactExportHandler{Authorizer: artifactAuthorizerStub{}, Scanner: artifactScannerStub{receipt: infected}, Objects: objects, MaxBytes: 1024}
	result, err := handler.Invoke(context.Background(), Invocation{ProviderRequestID: "a7000000-0000-4000-8000-000000000004", Input: input})
	if err != nil || result.Status != ResultFailed || result.Failure.Code != "unsafe_artifact" || objects.calls != 0 {
		t.Fatalf("infected result=%#v calls=%d err=%v", result, objects.calls, err)
	}
	handler.Authorizer = artifactAuthorizerStub{err: ErrArtifactExportAuthorization}
	result, err = handler.Invoke(context.Background(), Invocation{ProviderRequestID: "a7000000-0000-4000-8000-000000000004", Input: input})
	if err != nil || result.Failure.Code != "artifact_export_not_authorized" || objects.calls != 0 {
		t.Fatalf("unauthorized result=%#v calls=%d err=%v", result, objects.calls, err)
	}
	handler.Authorizer = artifactAuthorizerStub{err: errors.New("database unavailable")}
	if _, err = handler.Invoke(context.Background(), Invocation{ProviderRequestID: "a7000000-0000-4000-8000-000000000004", Input: input}); err == nil || objects.calls != 0 {
		t.Fatalf("infrastructure err=%v calls=%d", err, objects.calls)
	}
}
