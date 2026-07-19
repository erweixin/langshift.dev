package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"strings"
	"testing"
	"time"
)

func TestMailboxAcceptsSTARTTLSAndExposesMailpitCompatibleAPI(t *testing.T) {
	certificate, roots := testCertificate(t)
	box := &mailbox{}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("sandbox cannot bind test listener: %v", err)
	}
	defer listener.Close()
	go func() { _ = serveSMTP(listener, certificate, box) }()
	connection, err := net.DialTimeout("tcp4", listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err = connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	client, err := smtp.NewClient(connection, "local-mailbox")
	if err != nil {
		t.Fatal(err)
	}
	if err = client.StartTLS(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: "local-mailbox", RootCAs: roots}); err != nil {
		t.Fatal(err)
	}
	if err = client.Mail("sender@example.test"); err != nil {
		t.Fatal(err)
	}
	if err = client.Rcpt("person@example.test"); err != nil {
		t.Fatal(err)
	}
	data, err := client.Data()
	if err != nil {
		t.Fatal(err)
	}
	verificationURL := "http://127.0.0.1/verify-email?token=abc"
	encodedURL := base64.StdEncoding.EncodeToString([]byte(verificationURL))
	_, _ = io.WriteString(data, "Subject: Verify\r\nMIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=fixture\r\n\r\n--fixture\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\n"+encodedURL+"\r\n--fixture--\r\n")
	if err = data.Close(); err != nil {
		t.Fatal(err)
	}
	_ = client.Quit()

	server := httptest.NewServer(box.handler("0123456789abcdef0123456789abcdef"))
	defer server.Close()
	response, err := http.Get(server.URL + "/api/v1/messages?query=to%3Aperson%40example.test")
	if err != nil {
		t.Fatal(err)
	}
	listed, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(listed), "local-1") {
		t.Fatalf("status=%d body=%s", response.StatusCode, listed)
	}
	response, err = http.Get(server.URL + "/api/v1/message/local-1")
	if err != nil {
		t.Fatal(err)
	}
	detail, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if !strings.Contains(string(detail), verificationURL) {
		t.Fatalf("detail=%s", detail)
	}
}

func testCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "local-mailbox"}, DNSNames: []string{"local-mailbox"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := x509.MarshalPKCS8PrivateKey(private)
	certificate, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	// Trust the parsed certificate carrying the signed Raw bytes. Adding the
	// unsigned construction template does not create a valid trust anchor and
	// makes cold macOS test runs fail with an unknown-authority error.
	roots.AddCert(parsed)
	return certificate, roots
}
