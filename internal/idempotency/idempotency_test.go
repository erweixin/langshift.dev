package idempotency

import (
	"bytes"
	"errors"
	"testing"
)

func TestKeyDigestIsScopedSecretMaterial(t *testing.T) {
	pepper := bytes.Repeat([]byte{0x41}, 32)
	left, err := KeyDigest("request-key-0001", pepper)
	if err != nil {
		t.Fatal(err)
	}
	right, err := KeyDigest("request-key-0001", pepper)
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatal("digest is not deterministic")
	}
	other, _ := KeyDigest("request-key-0001", bytes.Repeat([]byte{0x42}, 32))
	if left == other {
		t.Fatal("pepper did not isolate digest")
	}
	if _, err = KeyDigest("short", pepper); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("error=%v", err)
	}
}

func TestRequestHashIsCanonicalInputHash(t *testing.T) {
	if RequestHash([]byte(`{"a":1}`)) == RequestHash([]byte(`{"a":2}`)) {
		t.Fatal("request hashes collided")
	}
}
