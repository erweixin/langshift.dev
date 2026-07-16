package toolreconciler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestHTTPLookupBindsProviderIdentityAndResponse(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.Header.Get("Idempotency-Key") != "provider-request-42" || request.Header.Get("Authorization") != "Bearer adapter-token" {
			t.Fatalf("request headers=%v", request.Header)
		}
		var body httpLookupRequest
		if json.NewDecoder(request.Body).Decode(&body) != nil || body.EffectKey != "workspace:42" || body.ReconciliationRound != 2 {
			t.Fatalf("request body=%#v", body)
		}
		encoded, _ := json.Marshal(httpLookupResponse{SchemaVersion: 1, ProviderRequestID: body.ProviderRequestID, Disposition: LookupConfirmed, ExternalResourceRef: "provider://workspace/42", Evidence: json.RawMessage(`{"provider_status":"succeeded"}`)})
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(encoded)))}, nil
	})}
	endpoint, err := url.Parse("https://adapter.example/reconcile")
	if err != nil {
		t.Fatal(err)
	}
	command := validCommandPayload(time.Now().UTC(), 2)
	result, err := (HTTPLookup{Endpoint: endpoint, Client: client, BearerToken: "adapter-token", MaximumResponse: 1 << 20, Timeout: time.Second}).Lookup(t.Context(), LookupRequest{Command: command})
	if err != nil || result.Disposition != LookupConfirmed || result.ExternalResourceRef != "provider://workspace/42" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestHTTPLookupRejectsMismatchedProviderResponse(t *testing.T) {
	encoded, _ := json.Marshal(httpLookupResponse{SchemaVersion: 1, ProviderRequestID: "other", Disposition: LookupNotApplied, Evidence: json.RawMessage(`{"status":"missing"}`)})
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(encoded)))}, nil
	})}
	endpoint, _ := url.Parse("https://adapter.example/reconcile")
	if _, err := (HTTPLookup{Endpoint: endpoint, Client: client, MaximumResponse: 1 << 20, Timeout: time.Second}).Lookup(t.Context(), LookupRequest{Command: validCommandPayload(time.Now().UTC(), 1)}); err != ErrHTTPLookupProtocol {
		t.Fatalf("error=%v", err)
	}
}
