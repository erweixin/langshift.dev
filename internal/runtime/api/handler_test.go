package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/langshift/lites/internal/runtime/controller"
	runtimepostgres "github.com/langshift/lites/internal/runtime/postgres"
)

type controllerStub struct{ provisioned controller.ProvisionRequest }

func (stub *controllerStub) Provision(_ context.Context, request controller.ProvisionRequest) (controller.Provisioned, error) {
	stub.provisioned = request
	return controller.Provisioned{Provision: runtimepostgres.ProvisionResult{SessionID: "session-1"}, Ready: runtimepostgres.LifecycleResult{Status: "ready", Version: 3}}, nil
}
func (stub *controllerStub) Terminate(_ context.Context, session, _ string) (runtimepostgres.LifecycleResult, error) {
	return runtimepostgres.LifecycleResult{SessionID: session, Status: "terminated"}, nil
}

func TestHandlerRequiresMTLSStrictJSONAndPinnedHost(t *testing.T) {
	stub := &controllerStub{}
	const clientID = "spiffe://lites.internal/tool-worker"
	handler := Handler{Controller: stub, HostID: "host-1", RequireVerifiedClientCertificate: true, AllowedClientSPIFFEID: clientID, RequestTimeout: time.Second}
	identity, err := url.Parse(clientID)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"tenant_id":"tenant-1","capability_token":"secret-capability","host_id":"host-1","machine_id":"machine-1","guest_cid":42,"provision_lease":"secret-lease","provision_lease_expires_at":"2026-07-15T22:05:00Z","payload":{"ref":"encrypted://provision","hash":"hash"},"actor":{"kind":"system"},"correlation_id":"correlation-1","scratch_rate":{}}`
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/runtime/provisions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("without mTLS status=%d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/internal/v1/runtime/provisions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{URIs: []*url.URL{identity}}}}}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || stub.provisioned.Command.HostID != "host-1" || stub.provisioned.Command.CapabilityToken == "" {
		t.Fatalf("provision status=%d request=%#v body=%s", response.Code, stub.provisioned, response.Body.String())
	}
	var decoded map[string]any
	if json.Unmarshal(response.Body.Bytes(), &decoded) != nil || decoded["provision"] == nil {
		t.Fatalf("invalid response: %s", response.Body.String())
	}
	tampered := strings.Replace(body, `"host_id":"host-1"`, `"host_id":"host-2"`, 1)
	request = httptest.NewRequest(http.MethodPost, "/internal/v1/runtime/provisions", strings.NewReader(tampered))
	request.Header.Set("Content-Type", "application/json")
	request.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{URIs: []*url.URL{identity}}}}}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("cross-host status=%d", response.Code)
	}
}

func TestHandlerRejectsCertificateFromSameCAWithDifferentWorkloadIdentity(t *testing.T) {
	identity, err := url.Parse("spiffe://lites.internal/another-service")
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler{Controller: &controllerStub{}, HostID: "host-1", RequireVerifiedClientCertificate: true, AllowedClientSPIFFEID: "spiffe://lites.internal/tool-worker", RequestTimeout: time.Second}
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/runtime/terminations", strings.NewReader(`{"session_id":"session-1","reason":"expired"}`))
	request.Header.Set("Content-Type", "application/json")
	request.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{URIs: []*url.URL{identity}}}}}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("cross-workload certificate status=%d", response.Code)
	}
}
