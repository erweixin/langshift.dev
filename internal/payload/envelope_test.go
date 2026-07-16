package payload

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

type keyProviderStub struct{ key Key }

func (provider keyProviderStub) Current(context.Context, string) (Key, error) {
	return provider.key, nil
}
func (provider keyProviderStub) ByID(_ context.Context, _ string, id string) (Key, error) {
	if id != provider.key.ID {
		return Key{}, ErrInvalidKey
	}
	return provider.key, nil
}

type blobStoreStub struct{ values map[string][]byte }

type payloadMetricsStub struct {
	read, write, retained int64
}

func (metrics *payloadMetricsStub) AddArtifactBytes(_ context.Context, count int64, direction string) {
	if direction == "read" {
		metrics.read += count
	} else if direction == "write" {
		metrics.write += count
	}
}

func (metrics *payloadMetricsStub) AddRetainedBytes(_ context.Context, count int64) {
	metrics.retained += count
}

func (store *blobStoreStub) Put(_ context.Context, key string, value []byte) (string, error) {
	ref := "s3://restricted/" + key
	if current, exists := store.values[ref]; exists {
		if bytes.Equal(current, value) {
			return ref, nil
		}
		return "", errors.New("immutable blob already exists")
	}
	store.values[ref] = append([]byte(nil), value...)
	return ref, nil
}
func (store *blobStoreStub) Get(_ context.Context, ref string) ([]byte, error) {
	value, ok := store.values[ref]
	if !ok {
		return nil, errors.New("not found")
	}
	return append([]byte(nil), value...), nil
}

func TestEnvelopeStoreEncryptsBindsAndAuthenticatesRestrictedPayload(t *testing.T) {
	blobs := &blobStoreStub{values: map[string][]byte{}}
	metrics := &payloadMetricsStub{}
	store := EnvelopeStore{Keys: keyProviderStub{key: Key{ID: "vault-key-v1", Material: bytes.Repeat([]byte{0x42}, 32)}}, Blobs: blobs, Random: bytes.NewReader(bytes.Repeat([]byte{0x24}, 12)), Metrics: metrics}
	descriptor := Descriptor{TenantID: "tenant-1", ObjectID: "verification-1", Class: "identity-email", ContentType: "application/json"}
	plaintext := []byte(`{"email":"user@example.com","token":"raw-secret-token"}`)
	manifest, err := store.Put(t.Context(), descriptor, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	stored := blobs.values[manifest.Ref]
	if metrics.write != int64(len(stored)) || metrics.retained != int64(len(stored)) {
		t.Fatalf("write metrics=%#v stored=%d", metrics, len(stored))
	}
	if bytes.Contains(stored, []byte("user@example.com")) || bytes.Contains(stored, []byte("raw-secret-token")) {
		t.Fatal("restricted plaintext reached object storage")
	}
	got, err := store.Get(t.Context(), descriptor, manifest)
	if err != nil || !bytes.Equal(got, plaintext) {
		t.Fatalf("plaintext=%s err=%v", got, err)
	}
	minimalManifest := Manifest{Ref: manifest.Ref, Hash: manifest.Hash}
	if _, err = store.Get(t.Context(), descriptor, minimalManifest); err != nil {
		t.Fatalf("database ref/hash manifest: %v", err)
	}
	if metrics.read < 2*int64(len(stored)) {
		t.Fatalf("read metrics=%#v stored=%d", metrics, len(stored))
	}
	wrongTenant := descriptor
	wrongTenant.TenantID = "tenant-2"
	if _, err = store.Get(t.Context(), wrongTenant, manifest); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("wrong-tenant error=%v", err)
	}
	blobs.values[manifest.Ref][len(blobs.values[manifest.Ref])-1] ^= 1
	if _, err = store.Get(t.Context(), descriptor, manifest); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("tamper error=%v", err)
	}
}
