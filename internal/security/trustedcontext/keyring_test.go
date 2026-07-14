package trustedcontext

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadPublicKeyringAndIssuanceWindow(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	path := filepath.Join(t.TempDir(), "keyring.json")
	encoded := fmt.Sprintf(`{"version":"1.0.0","keys":[{"id":"gateway-1","public_key":"%s","not_before":"%s","not_after":"%s"}]}`, base64.RawURLEncoding.EncodeToString(publicKey), now.Add(-time.Hour).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339))
	if err = os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, windows, err := LoadPublicKeyring(path)
	if err != nil {
		t.Fatal(err)
	}
	claims := Claims{PrincipalKind: PublicRequest, Issuer: "gateway", Audience: "identity", RequestID: "request", RequestMethod: "POST", RequestTarget: "/", ClientIPHash: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), UserAgentHash: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), CSRFVerified: true, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Minute).Unix(), Nonce: "nonce"}
	token, err := Sign(claims, "gateway-1", privateKey, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	verifier := Verifier{Issuer: "gateway", Audience: "identity", Keys: keys, KeyWindows: windows, MaximumTTL: 5 * time.Minute}
	if _, err = verifier.Verify(token, now); err != nil {
		t.Fatal(err)
	}
	window := windows["gateway-1"]
	window.NotAfter = now
	windows["gateway-1"] = window
	if _, err = verifier.Verify(token, now); err == nil {
		t.Fatal("token issued outside key window accepted")
	}
}

func TestLoadPublicKeyringRejectsUnknownFieldsAndDuplicateKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	if err := os.WriteFile(path, []byte(`{"version":"1.0.0","keys":[],"extra":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadPublicKeyring(path); err == nil {
		t.Fatal("ambiguous keyring accepted")
	}
}
