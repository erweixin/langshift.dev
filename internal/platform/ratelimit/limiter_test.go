package ratelimit

import (
	"bytes"
	"testing"
	"time"
)

func TestSubjectDigestIsPurposeSeparatedAndOpaque(t *testing.T) {
	pepper := bytes.Repeat([]byte{0x42}, 32)
	first, err := SubjectDigest(pepper, "identity-login-email", []byte("person@example.com"), []byte("ip-hash"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := SubjectDigest(pepper, "identity-register-email", []byte("person@example.com"), []byte("ip-hash"))
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !validDigest(first) || bytes.Contains([]byte(first), []byte("person")) {
		t.Fatal("digest was not opaque and purpose separated")
	}
}

func TestLimiterRejectsUnsafeInput(t *testing.T) {
	requests := []Request{
		{},
		{Action: "login", SubjectDigest: "email@example.com", Cost: 1, Limit: Limit{Capacity: 1, Window: time.Minute}},
		{Action: "LOGIN", SubjectDigest: string(bytes.Repeat([]byte{'a'}, 64)), Cost: 1, Limit: Limit{Capacity: 1, Window: time.Minute}},
	}
	for _, request := range requests {
		if _, err := (Limiter{Namespace: "lites"}).Allow(t.Context(), request); err == nil {
			t.Fatalf("expected invalid request to fail: %#v", request)
		}
	}
}
