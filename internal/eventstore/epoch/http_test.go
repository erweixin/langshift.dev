package epoch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestHTTPAuthorityFailsClosedAndAuthenticates(t *testing.T) {
	const expected = "10000000-0000-4000-8000-000000000001"
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer service-token" {
			return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("unauthorized"))}, nil
		}
		header := make(http.Header)
		header.Set("Content-Type", "application/json")
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(`{"store_epoch":"` + expected + `"}`))}, nil
	})}
	authority := HTTPAuthority{Endpoint: "http://127.0.0.1/v1/store-epoch", BearerToken: "service-token", Client: client, AllowInsecureLoopback: true}
	actual, err := authority.CurrentStoreEpoch(context.Background())
	if err != nil || actual != expected {
		t.Fatalf("epoch=%q err=%v", actual, err)
	}
	authority.BearerToken = "wrong"
	if _, err = authority.CurrentStoreEpoch(context.Background()); !errors.Is(err, ErrAuthorityUnavailable) {
		t.Fatalf("expected fail closed, got %v", err)
	}
}

func TestHTTPAuthorityRejectsInsecureRemoteAndAmbiguousJSON(t *testing.T) {
	authority := HTTPAuthority{Endpoint: "http://example.com/v1/store-epoch", Client: http.DefaultClient, AllowInsecureLoopback: true}
	if _, err := authority.CurrentStoreEpoch(context.Background()); !errors.Is(err, ErrAuthorityUnavailable) {
		t.Fatalf("expected insecure endpoint rejection, got %v", err)
	}
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(`{"store_epoch":"10000000-0000-4000-8000-000000000001","extra":true}`))}, nil
	})}
	authority = HTTPAuthority{Endpoint: "http://localhost/v1/store-epoch", Client: client, AllowInsecureLoopback: true}
	if _, err := authority.CurrentStoreEpoch(context.Background()); !errors.Is(err, ErrAuthorityUnavailable) {
		t.Fatalf("expected strict JSON rejection, got %v", err)
	}
}
