//go:build integration

package ratelimit

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"
)

func TestValkeyGCRABurstAndRecovery(t *testing.T) {
	address := os.Getenv("VALKEY_TEST_ADDRESS")
	if address == "" {
		t.Skip("VALKEY_TEST_ADDRESS is required")
	}
	client, err := NewClient(ClientConfig{Addresses: []string{address}, AllowInsecureDevelopment: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = Ready(ctx, client); err != nil {
		t.Fatal(err)
	}
	digest, err := SubjectDigest(bytes.Repeat([]byte{0x61}, 32), "integration", []byte(t.Name()), []byte(time.Now().String()))
	if err != nil {
		t.Fatal(err)
	}
	limiter := Limiter{Client: client, Namespace: "lites-it"}
	request := Request{Action: "login", SubjectDigest: digest, Cost: 1, Limit: Limit{Capacity: 3, Window: 150 * time.Millisecond}}
	for remaining := int64(2); remaining >= 0; remaining-- {
		decision, allowErr := limiter.Allow(ctx, request)
		if allowErr != nil || !decision.Allowed || decision.Remaining != remaining {
			t.Fatalf("remaining=%d decision=%#v err=%v", remaining, decision, allowErr)
		}
	}
	denied, err := limiter.Allow(ctx, request)
	if err != nil || denied.Allowed || denied.RetryAfter <= 0 {
		t.Fatalf("expected limited decision, got %#v err=%v", denied, err)
	}
	time.Sleep(denied.RetryAfter + 20*time.Millisecond)
	recovered, err := limiter.Allow(ctx, request)
	if err != nil || !recovered.Allowed {
		t.Fatalf("expected recovered capacity, got %#v err=%v", recovered, err)
	}
}
