package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTrustedProxyUsesOnlySingleAddressFromAllowlistedPeer(t *testing.T) {
	networks, err := ParseTrustedProxyCIDRs([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	handler := TrustedProxy{Networks: networks}.Wrap(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.RemoteAddr != "203.0.113.8:443" || request.Header.Get("X-Forwarded-For") != "" {
			t.Fatalf("remote=%s headers=%v", request.RemoteAddr, request.Header)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "https://api.lites.dev/v1/account", nil)
	request.RemoteAddr = "10.2.3.4:443"
	request.Header.Set("X-Forwarded-For", "203.0.113.8")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status=%d", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "https://api.lites.dev/v1/account", nil)
	request.RemoteAddr = "10.2.3.4:443"
	request.Header.Set("X-Forwarded-For", "203.0.113.8, 198.51.100.9")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("ambiguous chain status=%d", recorder.Code)
	}
}

func TestUntrustedPeerCannotSpoofForwardedAddress(t *testing.T) {
	networks, _ := ParseTrustedProxyCIDRs([]string{"10.0.0.0/8"})
	handler := TrustedProxy{Networks: networks}.Wrap(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.RemoteAddr != "198.51.100.7:8443" || request.Header.Get("X-Forwarded-For") != "" {
			t.Fatalf("remote=%s headers=%v", request.RemoteAddr, request.Header)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "https://api.lites.dev/v1/account", nil)
	request.RemoteAddr = "198.51.100.7:8443"
	request.Header.Set("X-Forwarded-For", "203.0.113.8")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status=%d", recorder.Code)
	}
}

func TestTrustedProxyCIDRsMustBeCanonicalAndUnique(t *testing.T) {
	for _, values := range [][]string{{"10.1.2.3/8"}, {"10.0.0.0/8", "10.0.0.0/8"}, {"not-a-network"}} {
		if _, err := ParseTrustedProxyCIDRs(values); err == nil {
			t.Fatalf("accepted invalid networks %v", values)
		}
	}
}
