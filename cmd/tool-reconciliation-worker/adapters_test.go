package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/langshift/lites/internal/toolreconciler"
)

func TestAdapterInventoryRequiresHTTPSAuthenticationAndTLS13(t *testing.T) {
	directory := t.TempDir()
	caFile := filepath.Join(directory, "ca.pem")
	tokenFile := filepath.Join(directory, "token")
	inventoryFile := filepath.Join(directory, "adapters.json")
	writeTestCA(t, caFile)
	if err := os.WriteFile(tokenFile, []byte("adapter-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeAdapterInventory(t, inventoryFile, adapterEnvelope{SchemaVersion: 1, Handlers: []adapterConfig{{
		Name: "career_evidence.reconcile", Endpoint: "https://provider.example.test/v1/effects/lookup",
		RootCAFile: caFile, TLSServerName: "provider.example.test", BearerTokenFile: tokenFile,
	}}})
	registrations, err := loadLookupRegistrations(inventoryFile, 15*time.Second, 1<<20)
	if err != nil || len(registrations) != 1 || registrations[0].Name != "career_evidence.reconcile" {
		t.Fatalf("registrations=%#v err=%v", registrations, err)
	}
	lookup, ok := registrations[0].Lookup.(toolreconciler.HTTPLookup)
	if !ok || lookup.BearerToken != "adapter-token" || lookup.Endpoint.Scheme != "https" {
		t.Fatalf("lookup=%#v", registrations[0].Lookup)
	}
	transport, ok := lookup.Client.Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil || transport.TLSClientConfig.MinVersion != tls.VersionTLS13 || transport.TLSClientConfig.ServerName != "provider.example.test" {
		t.Fatalf("transport=%#v", lookup.Client.Transport)
	}

	writeAdapterInventory(t, inventoryFile, adapterEnvelope{SchemaVersion: 1, Handlers: []adapterConfig{{
		Name: "career_evidence.reconcile", Endpoint: "http://127.0.0.1:8080/lookup", RootCAFile: caFile,
		TLSServerName: "provider.example.test", BearerTokenFile: tokenFile,
	}}})
	if _, err = loadLookupRegistrations(inventoryFile, 15*time.Second, 1<<20); err == nil {
		t.Fatal("insecure adapter endpoint accepted")
	}
}

func TestAdapterInventoryRejectsDuplicatesAndUnknownFields(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "adapters.json")
	caFile := filepath.Join(directory, "ca.pem")
	tokenFile := filepath.Join(directory, "token")
	writeTestCA(t, caFile)
	if err := os.WriteFile(tokenFile, []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"handlers":[],"unexpected":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadLookupRegistrations(path, time.Second, 1024); err == nil {
		t.Fatal("unknown inventory field accepted")
	}
	entry := adapterConfig{Name: "duplicate", Endpoint: "https://provider.example.test/lookup", RootCAFile: caFile, TLSServerName: "provider.example.test", BearerTokenFile: tokenFile}
	writeAdapterInventory(t, path, adapterEnvelope{SchemaVersion: 1, Handlers: []adapterConfig{entry, entry}})
	if _, err := loadLookupRegistrations(path, time.Second, 1024); err == nil {
		t.Fatal("invalid duplicate inventory accepted")
	}
}

func writeAdapterInventory(t *testing.T, path string, envelope adapterEnvelope) {
	t.Helper()
	contents, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeTestCA(t *testing.T, path string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	template := x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}
