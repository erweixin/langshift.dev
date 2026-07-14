package opaque

import (
	"bytes"
	"testing"
)

func TestTokenDigestIsPurposeSeparatedAndRawValueIsNotPersistable(t *testing.T) {
	pepper := bytes.Repeat([]byte{0x42}, 32)
	manager := Manager{Purpose: "email-verification", Pepper: pepper, Random: bytes.NewReader(bytes.Repeat([]byte{0x24}, 32))}
	credential, err := manager.Issue()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := manager.Digest(credential.Raw)
	if err != nil || digest != credential.Digest {
		t.Fatalf("digest mismatch err=%v", err)
	}
	other, _ := (Manager{Purpose: "password-reset", Pepper: pepper}).Digest(credential.Raw)
	if other == digest || bytes.Contains(digest[:], []byte(credential.Raw)) {
		t.Fatal("purpose separation or raw-token secrecy failed")
	}
}
