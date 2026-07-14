package password

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func testHasher() Hasher {
	return Hasher{Parameters: Parameters{Version: CurrentVersion, Time: 1, MemoryKiB: 8 * 1024, Threads: 1, KeyLength: 32}, Pepper: bytes.Repeat([]byte{0x42}, 32), Random: bytes.NewReader(bytes.Repeat([]byte{0x24}, 16))}
}

func TestHashVerifyUsesFullUnicodePassword(t *testing.T) {
	hasher := testHasher()
	value := strings.Repeat("界", 15) + "-complete"
	digest, parameters, err := hasher.Hash(value)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := hasher.Verify(value, digest, parameters)
	if err != nil || !verified {
		t.Fatalf("verify=%v err=%v", verified, err)
	}
	changedTail := strings.TrimSuffix(value, "complete") + "tampered"
	verified, err = hasher.Verify(changedTail, digest, parameters)
	if err != nil || verified {
		t.Fatalf("truncated or mismatched password accepted: verify=%v err=%v", verified, err)
	}
	if parameters.Salt == "" || string(digest) == value {
		t.Fatal("credential material was not separated from password")
	}
}

func TestPasswordPolicyAndStoredParameterBounds(t *testing.T) {
	if err := ValidateForRegistration(strings.Repeat("a", 14)); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("short password error=%v", err)
	}
	if err := ValidateForRegistration(strings.Repeat("a", 129)); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("long password error=%v", err)
	}
	if _, _, err := (Hasher{Parameters: ProductionParameters(), Pepper: []byte("weak")}).Hash(strings.Repeat("a", 15)); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("weak pepper error=%v", err)
	}
	hasher := testHasher()
	if _, err := hasher.Verify("valid-login-value", make([]byte, 32), Parameters{Version: 1, Time: 1, MemoryKiB: 2 * 1024 * 1024, Threads: 4, KeyLength: 32, Salt: "invalid"}); !errors.Is(err, ErrInvalidParameters) {
		t.Fatalf("resource-abuse parameters error=%v", err)
	}
}
