package s3store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

type memoryS3 struct {
	objects map[string][]byte
	meta    map[string]map[string]string
	media   map[string]string
	version map[string]string
}

func (client *memoryS3) HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	return &s3.HeadBucketOutput{}, nil
}

func (client *memoryS3) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	key := aws.ToString(input.Key)
	if _, found := client.objects[key]; found {
		return nil, &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "exists"}
	}
	body, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	client.objects[key] = body
	client.meta[key] = input.Metadata
	client.media[key] = aws.ToString(input.ContentType)
	client.version[key] = "version-1"
	return &s3.PutObjectOutput{VersionId: aws.String(client.version[key])}, nil
}

func (client *memoryS3) HeadObject(_ context.Context, input *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	key := aws.ToString(input.Key)
	body, found := client.objects[key]
	if !found {
		return nil, &smithy.GenericAPIError{Code: "NotFound", Message: "not found"}
	}
	return &s3.HeadObjectOutput{ContentLength: aws.Int64(int64(len(body))), ContentType: aws.String(client.media[key]), Metadata: client.meta[key], VersionId: aws.String(client.version[key])}, nil
}

func (client *memoryS3) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	key := aws.ToString(input.Key)
	body, found := client.objects[key]
	if !found {
		return nil, errors.New("not found")
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(body)), ContentLength: aws.Int64(int64(len(body))), Metadata: client.meta[key]}, nil
}

func (client *memoryS3) DeleteObject(_ context.Context, input *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	key := aws.ToString(input.Key)
	delete(client.objects, key)
	delete(client.meta, key)
	delete(client.media, key)
	delete(client.version, key)
	return &s3.DeleteObjectOutput{}, nil
}

func (client *memoryS3) ListObjectVersions(_ context.Context, input *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
	key := aws.ToString(input.Prefix)
	version, found := client.version[key]
	if !found {
		return &s3.ListObjectVersionsOutput{}, nil
	}
	return &s3.ListObjectVersionsOutput{Versions: []types.ObjectVersion{{Key: aws.String(key), VersionId: aws.String(version)}}}, nil
}

func testStore(client *memoryS3) Store {
	return Store{Client: client, Bucket: "lites-payloads", Prefix: "restricted", MaxBytes: 1024, ServerSideEncryption: types.ServerSideEncryptionAes256, RequireDigestMetadata: true}
}

func memoryClient() *memoryS3 {
	return &memoryS3{objects: map[string][]byte{}, meta: map[string]map[string]string{}, media: map[string]string{}, version: map[string]string{}}
}

func TestPutIsImmutableAndIdempotentForIdenticalContent(t *testing.T) {
	client := memoryClient()
	store := testStore(client)
	key := "tenant/event/id/hash"
	ref, err := store.Put(context.Background(), key, []byte("ciphertext"))
	if err != nil || ref != "s3://lites-payloads/restricted/"+key {
		t.Fatalf("ref=%q err=%v", ref, err)
	}
	if _, err = store.Put(context.Background(), key, []byte("ciphertext")); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Put(context.Background(), key, []byte("different")); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected immutable conflict, got %v", err)
	}
}

func TestPutAcceptsDurableEventStageObjectID(t *testing.T) {
	client := memoryClient()
	store := testStore(client)
	key := "tenant/event/attempt-1:provider-dispatch/hash"
	ref, err := store.Put(context.Background(), key, []byte("ciphertext"))
	if err != nil || ref != "s3://lites-payloads/restricted/"+key {
		t.Fatalf("ref=%q err=%v", ref, err)
	}
	contents, err := store.Get(context.Background(), ref)
	if err != nil || string(contents) != "ciphertext" {
		t.Fatalf("contents=%q err=%v", contents, err)
	}
}

func TestObjectKeyStillRejectsTraversalAndURLSyntax(t *testing.T) {
	client := memoryClient()
	store := testStore(client)
	for _, key := range []string{
		"tenant/event/../secret",
		"tenant/event/id?query",
		"tenant/event/id#fragment",
		"tenant/event/id%2Fescape",
		"tenant/event/id\\escape",
		"tenant/event/id::stage with-space",
	} {
		if _, err := store.Put(context.Background(), key, []byte("ciphertext")); !errors.Is(err, ErrConfiguration) {
			t.Fatalf("key=%q err=%v", key, err)
		}
	}
}

func TestPutVersionedRequiresAndReplaysProviderVersion(t *testing.T) {
	client := memoryClient()
	store := testStore(client)
	object, err := store.PutVersioned(context.Background(), "tenant/artifact/id/request.pdf", "application/pdf", []byte("safe-pdf"))
	if err != nil || object.Reference != "s3://lites-payloads/restricted/tenant/artifact/id/request.pdf" || object.VersionID != "version-1" || object.MediaType != "application/pdf" || object.ByteSize != 8 || len(object.ContentHash) != 64 {
		t.Fatalf("object=%#v err=%v", object, err)
	}
	replayed, err := store.PutVersioned(context.Background(), "tenant/artifact/id/request.pdf", "application/pdf", []byte("safe-pdf"))
	if err != nil || replayed != object {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	if _, err = store.PutVersioned(context.Background(), "tenant/artifact/id/request.pdf", "application/pdf", []byte("different")); !errors.Is(err, ErrConflict) {
		t.Fatalf("immutable conflict=%v", err)
	}
	client.version["restricted/tenant/artifact/id/request.pdf"] = ""
	if _, err = store.PutVersioned(context.Background(), "tenant/artifact/id/request.pdf", "application/pdf", []byte("safe-pdf")); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("unversioned replay=%v", err)
	}
}

func TestScannedVersionedReceiptIsRecoverableByExactMetadata(t *testing.T) {
	client := memoryClient()
	store := testStore(client)
	contentHash := ""
	contents := []byte("safe-pdf")
	digest := sha256.Sum256(contents)
	contentHash = hex.EncodeToString(digest[:])
	scanHash := strings.Repeat("a", 64)
	object, err := store.PutScannedVersioned(context.Background(), "tenant/artifact/id/request.pdf", "application/pdf", contents, scanHash)
	if err != nil || object.ScanResultHash != scanHash {
		t.Fatalf("object=%#v err=%v", object, err)
	}
	inspected, err := store.InspectScannedVersioned(context.Background(), "tenant/artifact/id/request.pdf", "application/pdf", contentHash, int64(len(contents)))
	if err != nil || inspected != object {
		t.Fatalf("inspected=%#v err=%v", inspected, err)
	}
	client.meta["restricted/tenant/artifact/id/request.pdf"]["lites-scan-result-sha256"] = "bad"
	if _, err = store.InspectScannedVersioned(context.Background(), "tenant/artifact/id/request.pdf", "application/pdf", contentHash, int64(len(contents))); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("tampered scan receipt err=%v", err)
	}
	if _, err = store.InspectScannedVersioned(context.Background(), "tenant/artifact/id/missing.pdf", "application/pdf", contentHash, int64(len(contents))); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing object err=%v", err)
	}
}

func TestGetRejectsReferenceEscapeOversizeAndIntegrityMismatch(t *testing.T) {
	client := memoryClient()
	store := testStore(client)
	ref, err := store.Put(context.Background(), "tenant/event/id/hash", []byte("ciphertext"))
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"file:///tmp/secret", "s3://other/restricted/tenant/event/id/hash", "s3://lites-payloads/other/object", "s3://lites-payloads/restricted/../secret"} {
		if _, err = store.Get(context.Background(), invalid); !errors.Is(err, ErrReference) {
			t.Fatalf("ref=%q err=%v", invalid, err)
		}
	}
	client.meta["restricted/tenant/event/id/hash"]["lites-sha256"] = "bad"
	if _, err = store.Get(context.Background(), ref); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("expected integrity rejection, got %v", err)
	}
	store.MaxBytes = 4
	if _, err = store.Get(context.Background(), ref); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected size rejection, got %v", err)
	}
}

func TestDeleteIsIdempotentAndConfinedToStorePrefix(t *testing.T) {
	client := memoryClient()
	store := testStore(client)
	ref, err := store.Put(context.Background(), "tenant/onboarding/id/hash", []byte("ciphertext"))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Delete(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if err = store.Delete(context.Background(), ref); err != nil {
		t.Fatalf("idempotent delete: %v", err)
	}
	if _, found := client.objects["restricted/tenant/onboarding/id/hash"]; found {
		t.Fatal("object survived deletion")
	}
	for _, invalid := range []string{"s3://other/restricted/tenant/object", "s3://lites-payloads/other/object", "file:///tmp/secret"} {
		if err = store.Delete(context.Background(), invalid); !errors.Is(err, ErrReference) {
			t.Fatalf("ref=%q err=%v", invalid, err)
		}
	}
}

func TestPurgeDeletesAllVersionsAndReturnsVerifiedReceipt(t *testing.T) {
	client := memoryClient()
	store := testStore(client)
	ref, err := store.PutVersioned(context.Background(), "tenant/artifact/id/file.pdf", "application/pdf", []byte("content"))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := store.Purge(context.Background(), ref.Reference)
	if err != nil || receipt.VersionsDeleted != 1 || len(receipt.Checksum) != 64 {
		t.Fatalf("receipt=%#v err=%v", receipt, err)
	}
	if _, found := client.objects["restricted/tenant/artifact/id/file.pdf"]; found {
		t.Fatal("version survived verified purge")
	}
	replayed, err := store.Purge(context.Background(), ref.Reference)
	if err != nil || replayed.VersionsDeleted != 0 || len(replayed.Checksum) != 64 {
		t.Fatalf("replay=%#v err=%v", replayed, err)
	}
}

func TestPurgeRouterRejectsUnknownBucketAndRoutesExactReference(t *testing.T) {
	client := memoryClient()
	store := testStore(client)
	ref, err := store.Put(context.Background(), "tenant/payload/id", []byte("ciphertext"))
	if err != nil {
		t.Fatal(err)
	}
	router := PurgeRouter{Stores: []Store{store}}
	if _, err = router.Purge(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if _, err = router.Purge(context.Background(), "s3://other/restricted/tenant/payload/id"); !errors.Is(err, ErrReference) {
		t.Fatalf("unknown bucket err=%v", err)
	}
}

func TestStoreRequiresServerSideEncryption(t *testing.T) {
	client := memoryClient()
	store := testStore(client)
	store.ServerSideEncryption = ""
	if _, err := store.Put(context.Background(), "safe/key", []byte("x")); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("expected encryption configuration rejection, got %v", err)
	}
}
