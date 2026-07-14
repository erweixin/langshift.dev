package api

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	platformratelimit "github.com/langshift/lites/internal/platform/ratelimit"
)

type rateLimiterStub struct {
	request  platformratelimit.Request
	decision platformratelimit.Decision
	err      error
}

func (stub *rateLimiterStub) Allow(_ context.Context, request platformratelimit.Request) (platformratelimit.Decision, error) {
	stub.request = request
	return stub.decision, stub.err
}

func TestAllowRequestUsesOpaqueDigestAndRetryAfter(t *testing.T) {
	stub := &rateLimiterStub{decision: platformratelimit.Decision{RetryAfter: 1500 * time.Millisecond}}
	handler := Handler{RateLimiter: stub, RateLimitPepper: bytes.Repeat([]byte{0x72}, 32)}
	request := httptest.NewRequest("POST", "/v1/auth/login", nil)
	writer := httptest.NewRecorder()
	allowed := handler.allowRequest(writer, request, "identity-login-subject", loginSubjectLimit, []byte("person@example.com"), []byte("private-ip"))
	if allowed || writer.Code != 429 || writer.Header().Get("Retry-After") != "2" {
		t.Fatalf("allowed=%v status=%d retry=%q", allowed, writer.Code, writer.Header().Get("Retry-After"))
	}
	if stub.request.Action != "identity-login-subject" || len(stub.request.SubjectDigest) != 64 || strings.Contains(stub.request.SubjectDigest, "person") {
		t.Fatalf("unsafe or malformed limiter request: %#v", stub.request)
	}
}

func TestAllowRequestFailsClosedWhenLimiterUnavailable(t *testing.T) {
	stub := &rateLimiterStub{err: platformratelimit.ErrUnavailable}
	handler := Handler{RateLimiter: stub, RateLimitPepper: bytes.Repeat([]byte{0x72}, 32)}
	request := httptest.NewRequest("POST", "/v1/auth/login", nil)
	writer := httptest.NewRecorder()
	if handler.allowRequest(writer, request, "identity-login-ip", loginIPLimit, []byte("ip-hash")) || writer.Code != 503 {
		t.Fatalf("limiter dependency did not fail closed: status=%d", writer.Code)
	}
}
