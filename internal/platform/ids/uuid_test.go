package ids

import (
	"bytes"
	"errors"
	"testing"
)

func TestUUIDsHaveVersionAndVariantAndDeterministicDomains(t *testing.T) {
	value, err := NewUUIDFrom(bytes.NewReader(bytes.Repeat([]byte{0x11}, 16)))
	if err != nil {
		t.Fatal(err)
	}
	if value != "11111111-1111-4111-9111-111111111111" {
		t.Fatalf("uuid=%s", value)
	}
	key := bytes.Repeat([]byte{0x42}, 32)
	left, _ := DeterministicUUID(key, "registration", "user@example.com")
	right, _ := DeterministicUUID(key, "registration", "user@example.com")
	other, _ := DeterministicUUID(key, "login", "user@example.com")
	if left != right || left == other {
		t.Fatalf("left=%s right=%s other=%s", left, right, other)
	}
	if _, err = DeterministicUUID([]byte("weak"), "registration", "x"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("error=%v", err)
	}
	if _, err = NewUUIDFrom(nil); !errors.Is(err, ErrInvalidRandom) {
		t.Fatalf("nil random error=%v", err)
	}
}
