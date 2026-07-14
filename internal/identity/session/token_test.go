package session

import (
	"bytes"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSessionTokenPersistsOnlyKeyedDigest(t *testing.T) {
	pepper := bytes.Repeat([]byte{0x42}, 32)
	credential, err := NewFrom(bytes.NewReader(bytes.Repeat([]byte{0x24}, 32)), pepper)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := Digest(credential.Raw, pepper)
	if err != nil {
		t.Fatal(err)
	}
	if !ConstantTimeEqual(credential.Digest, digest) {
		t.Fatal("digest mismatch")
	}
	if strings.Contains(hex.EncodeToString(digest[:]), credential.Raw) {
		t.Fatal("digest exposed raw token")
	}
	cookie, err := Cookie(credential.Raw, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatalf("unsafe cookie: %#v", cookie)
	}
	csrfCookie, err := CSRFCookie(credential.Raw, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !csrfCookie.Secure || csrfCookie.HttpOnly || csrfCookie.SameSite != http.SameSiteLaxMode || csrfCookie.Name != CSRFCookieName {
		t.Fatalf("unsafe csrf cookie: %#v", csrfCookie)
	}
}

func TestSessionTokenRejectsWeakPepperAndMalformedValues(t *testing.T) {
	if _, err := NewFrom(bytes.NewReader(make([]byte, 32)), []byte("weak")); err == nil {
		t.Fatal("weak pepper accepted")
	}
	if _, err := Digest("not-a-token", bytes.Repeat([]byte{1}, 32)); err == nil {
		t.Fatal("malformed token accepted")
	}
}
