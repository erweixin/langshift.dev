package s3store

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

type memoryS3 struct {
	objects map[string][]byte
	meta    map[string]map[string]string
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
	return &s3.PutObjectOutput{}, nil
}

func (client *memoryS3) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	key := aws.ToString(input.Key)
	body, found := client.objects[key]
	if !found {
		return nil, errors.New("not found")
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(body)), ContentLength: aws.Int64(int64(len(body))), Metadata: client.meta[key]}, nil
}

func testStore(client *memoryS3) Store {
	return Store{Client: client, Bucket: "lites-payloads", Prefix: "restricted", MaxBytes: 1024, ServerSideEncryption: types.ServerSideEncryptionAes256, RequireDigestMetadata: true}
}

func TestPutIsImmutableAndIdempotentForIdenticalContent(t *testing.T) {
	client := &memoryS3{objects: map[string][]byte{}, meta: map[string]map[string]string{}}
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

func TestGetRejectsReferenceEscapeOversizeAndIntegrityMismatch(t *testing.T) {
	client := &memoryS3{objects: map[string][]byte{}, meta: map[string]map[string]string{}}
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

func TestStoreRequiresServerSideEncryption(t *testing.T) {
	client := &memoryS3{objects: map[string][]byte{}, meta: map[string]map[string]string{}}
	store := testStore(client)
	store.ServerSideEncryption = ""
	if _, err := store.Put(context.Background(), "safe/key", []byte("x")); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("expected encryption configuration rejection, got %v", err)
	}
}
