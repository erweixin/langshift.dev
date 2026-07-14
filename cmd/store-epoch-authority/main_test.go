package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	storeepoch "github.com/langshift/lites/internal/eventstore/epoch"
)

type handlerRoundTrip func(*http.Request) (*http.Response, error)

func (roundTrip handlerRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestAuthorityRequiresBearerAndReloadsIndependentEpochFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store-epoch")
	first := "10000000-0000-4000-8000-000000000001"
	second := "20000000-0000-4000-8000-000000000002"
	if err := os.WriteFile(path, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	token := "a-production-length-store-epoch-token"
	handler := authorityHandler{epochFile: path, tokenHash: sha256.Sum256([]byte(token))}
	for _, candidate := range []string{"", "Bearer wrong"} {
		request := httptest.NewRequest(http.MethodGet, "https://epoch.internal/v1/store-epoch", nil)
		if candidate != "" {
			request.Header.Set("Authorization", candidate)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("candidate=%q status=%d", candidate, recorder.Code)
		}
	}
	read := func(want string) {
		request := httptest.NewRequest(http.MethodGet, "https://epoch.internal/v1/store-epoch", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		var response struct {
			StoreEpoch string `json:"store_epoch"`
		}
		if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &response) != nil || response.StoreEpoch != want || recorder.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("status=%d response=%#v headers=%v", recorder.Code, response, recorder.Header())
		}
	}
	read(first)
	if err := os.WriteFile(path, []byte(second), 0o600); err != nil {
		t.Fatal(err)
	}
	read(second)
}

func TestAuthorityProtocolMatchesProductionConsumer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store-epoch")
	want := "30000000-0000-4000-8000-000000000003"
	if err := os.WriteFile(path, []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}
	token := "a-production-length-store-epoch-token"
	handler := authorityHandler{epochFile: path, tokenHash: sha256.Sum256([]byte(token))}
	client := &http.Client{Transport: handlerRoundTrip(func(request *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Result(), nil
	})}
	authority := storeepoch.HTTPAuthority{Endpoint: "http://127.0.0.1/v1/store-epoch", BearerToken: token, Client: client, AllowInsecureLoopback: true}
	actual, err := authority.CurrentStoreEpoch(context.Background())
	if err != nil || actual != want {
		t.Fatalf("epoch=%q error=%v", actual, err)
	}
}

func TestAuthorityFailsClosedForInvalidEpochAndAmbiguousRoute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store-epoch")
	if err := os.WriteFile(path, []byte("not-an-epoch"), 0o600); err != nil {
		t.Fatal(err)
	}
	token := "a-production-length-store-epoch-token"
	handler := authorityHandler{epochFile: path, tokenHash: sha256.Sum256([]byte(token))}
	request := httptest.NewRequest(http.MethodGet, "https://epoch.internal/v1/store-epoch", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "https://epoch.internal/v1/store-epoch?extra=true", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("ambiguous route status=%d", recorder.Code)
	}
}
