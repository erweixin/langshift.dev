package password

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestRangeCheckerSendsOnlyPrefixAndFindsExactSuffix(t *testing.T) {
	const candidate = "a breached password candidate"
	digest := sha1.Sum([]byte(candidate))
	encoded := strings.ToUpper(hex.EncodeToString(digest[:]))
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/range/"+encoded[:5] || request.Header.Get("Add-Padding") != "true" || strings.Contains(request.URL.String(), candidate) || strings.Contains(request.URL.String(), encoded[5:]) {
			t.Fatalf("request path=%q headers=%v", request.URL.Path, request.Header)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("00000000000000000000000000000000000:0\r\n" + encoded[5:] + ":42\r\n")), Request: request}, nil
	})
	checker, err := NewRangeChecker("https://breach.example/range", []string{"breach.example"}, &http.Client{Transport: transport, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	compromised, err := checker.Compromised(context.Background(), candidate)
	if err != nil || !compromised {
		t.Fatalf("compromised=%v error=%v", compromised, err)
	}
}

func TestRangeCheckerRejectsRedirectsMalformedResponsesAndHosts(t *testing.T) {
	redirectTransport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://elsewhere.example/"}}, Body: io.NopCloser(strings.NewReader("redirect")), Request: request}, nil
	})
	client := &http.Client{Transport: redirectTransport, Timeout: time.Second}
	checker, err := NewRangeChecker("https://breach.example/range", []string{"breach.example"}, client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = checker.Compromised(context.Background(), "a sufficiently long password"); err == nil {
		t.Fatal("redirect was accepted")
	}
	malformedTransport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("not-a-range")), Request: request}, nil
	})
	checker, err = NewRangeChecker("https://breach.example/range", []string{"breach.example"}, &http.Client{Transport: malformedTransport, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = checker.Compromised(context.Background(), "a sufficiently long password"); err == nil {
		t.Fatal("malformed response was accepted")
	}
	for _, endpoint := range []string{"http://example.com/range", "https://user@example.com/range", "https://example.com/range?secret=1"} {
		if _, err = NewRangeChecker(endpoint, []string{"allowed.example"}, client); !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("endpoint=%q error=%v", endpoint, err)
		}
	}
}
