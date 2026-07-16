// Package payload encrypts restricted payloads before they reach object
// storage. Database rows and outbox commands keep only content-addressed refs.
package payload

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const EnvelopeVersion = 1

var (
	ErrInvalidDescriptor = errors.New("payload descriptor is invalid")
	ErrInvalidKey        = errors.New("payload encryption key is invalid")
	ErrIntegrity         = errors.New("payload integrity check failed")
)

type Descriptor struct {
	TenantID    string `json:"tenant_id"`
	ObjectID    string `json:"object_id"`
	Class       string `json:"class"`
	ContentType string `json:"content_type"`
}

type Manifest struct {
	Ref     string `json:"ref"`
	Hash    string `json:"hash"`
	KeyID   string `json:"key_id"`
	AADHash string `json:"aad_hash"`
}

type Key struct {
	ID       string
	Material []byte
}

type KeyProvider interface {
	Current(context.Context, string) (Key, error)
	ByID(context.Context, string, string) (Key, error)
}

type BlobStore interface {
	// Put creates an immutable object at key. Reusing a key is permitted only
	// when the stored bytes are identical; implementations must never overwrite.
	Put(context.Context, string, []byte) (string, error)
	Get(context.Context, string) ([]byte, error)
}

type Store interface {
	Put(context.Context, Descriptor, []byte) (Manifest, error)
	Get(context.Context, Descriptor, Manifest) ([]byte, error)
}

type EnvelopeStore struct {
	Keys    KeyProvider
	Blobs   BlobStore
	Random  io.Reader
	Metrics Metrics
}

type Metrics interface {
	AddArtifactBytes(context.Context, int64, string)
	AddRetainedBytes(context.Context, int64)
}

type envelope struct {
	Version    int    `json:"version"`
	KeyID      string `json:"key_id"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
	AADHash    string `json:"aad_hash"`
}

func (store EnvelopeStore) Put(ctx context.Context, descriptor Descriptor, plaintext []byte) (Manifest, error) {
	if store.Keys == nil || store.Blobs == nil || !validDescriptor(descriptor) || len(plaintext) == 0 {
		return Manifest{}, ErrInvalidDescriptor
	}
	aad, err := json.Marshal(descriptor)
	if err != nil {
		return Manifest{}, err
	}
	key, err := store.Keys.Current(ctx, descriptor.TenantID)
	if err != nil {
		return Manifest{}, err
	}
	aead, err := newAEAD(key)
	if err != nil {
		return Manifest{}, err
	}
	randomSource := store.Random
	if randomSource == nil {
		randomSource = rand.Reader
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = io.ReadFull(randomSource, nonce); err != nil {
		return Manifest{}, fmt.Errorf("read payload nonce: %w", err)
	}
	aadHash := sha256Hex(aad)
	encoded, err := json.Marshal(envelope{Version: EnvelopeVersion, KeyID: key.ID, Nonce: nonce, Ciphertext: aead.Seal(nil, nonce, plaintext, aad), AADHash: aadHash})
	if err != nil {
		return Manifest{}, err
	}
	hash := sha256Hex(encoded)
	// The blob key is content-addressed. Concurrent idempotent attempts may
	// stage different candidate payloads, but can never overwrite the manifest
	// selected by the database transaction.
	ref, err := store.Blobs.Put(ctx, descriptor.TenantID+"/"+descriptor.Class+"/"+descriptor.ObjectID+"/"+hash, encoded)
	if err != nil {
		return Manifest{}, err
	}
	if ref == "" {
		return Manifest{}, ErrIntegrity
	}
	if store.Metrics != nil {
		store.Metrics.AddArtifactBytes(ctx, int64(len(encoded)), "write")
		store.Metrics.AddRetainedBytes(ctx, int64(len(encoded)))
	}
	return Manifest{Ref: ref, Hash: hash, KeyID: key.ID, AADHash: aadHash}, nil
}

func (store EnvelopeStore) Get(ctx context.Context, descriptor Descriptor, manifest Manifest) ([]byte, error) {
	if store.Keys == nil || store.Blobs == nil || !validDescriptor(descriptor) || manifest.Ref == "" || manifest.Hash == "" {
		return nil, ErrInvalidDescriptor
	}
	encoded, err := store.Blobs.Get(ctx, manifest.Ref)
	if err != nil {
		return nil, err
	}
	if store.Metrics != nil {
		store.Metrics.AddArtifactBytes(ctx, int64(len(encoded)), "read")
	}
	if !hmac.Equal([]byte(sha256Hex(encoded)), []byte(manifest.Hash)) {
		return nil, ErrIntegrity
	}
	var value envelope
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&value); err != nil || value.Version != EnvelopeVersion || value.KeyID == "" || value.AADHash == "" || (manifest.KeyID != "" && value.KeyID != manifest.KeyID) || (manifest.AADHash != "" && value.AADHash != manifest.AADHash) {
		return nil, ErrIntegrity
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, ErrIntegrity
	}
	aad, err := json.Marshal(descriptor)
	if err != nil || !hmac.Equal([]byte(sha256Hex(aad)), []byte(value.AADHash)) {
		return nil, ErrIntegrity
	}
	key, err := store.Keys.ByID(ctx, descriptor.TenantID, value.KeyID)
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(key)
	if err != nil || len(value.Nonce) != aead.NonceSize() {
		return nil, ErrIntegrity
	}
	plaintext, err := aead.Open(nil, value.Nonce, value.Ciphertext, aad)
	if err != nil {
		return nil, ErrIntegrity
	}
	return plaintext, nil
}

func newAEAD(key Key) (cipher.AEAD, error) {
	if key.ID == "" || len(key.Material) != 32 {
		return nil, ErrInvalidKey
	}
	block, err := aes.NewCipher(key.Material)
	if err != nil {
		return nil, ErrInvalidKey
	}
	return cipher.NewGCM(block)
}

func validDescriptor(value Descriptor) bool {
	return value.TenantID != "" && value.ObjectID != "" && value.Class != "" && value.ContentType != ""
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
