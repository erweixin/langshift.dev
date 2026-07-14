package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSecretBundleRequiresUniquePurposeKeys(t *testing.T) {
	values := make([]string, 12)
	for index := range values {
		values[index] = base64.StdEncoding.EncodeToString([]byte(strings.Repeat(string(rune('a'+index)), 32)))
	}
	encoded := fmt.Sprintf(`{"version":"1.0.0","password_pepper":%q,"verification_token_pepper":%q,"password_reset_pepper":%q,"email_change_pepper":%q,"invitation_pepper":%q,"session_pepper":%q,"csrf_pepper":%q,"idempotency_pepper":%q,"request_digest_pepper":%q,"identity_key":%q,"cursor_key":%q,"rate_limit_pepper":%q}`, values[0], values[1], values[2], values[3], values[4], values[5], values[6], values[7], values[8], values[9], values[10], values[11])
	path := filepath.Join(t.TempDir(), "bundle.json")
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSecretBundle(path); err != nil {
		t.Fatal(err)
	}
	encoded = strings.Replace(encoded, values[11], values[0], 1)
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSecretBundle(path); err == nil {
		t.Fatal("purpose-reused secret bundle accepted")
	}
}

func TestProductionConfigRequiresTLSAndFileCredentials(t *testing.T) {
	configuration := config{databaseURL: "postgres://db", trustedKeyringFile: "keys.json", epochURL: "https://epoch", secretBundleFile: "bundle.json", publicTenantID: "20000000-0000-4000-8000-000000000001", region: "US", passwordRangeAllowedHosts: []string{"api.pwnedpasswords.com"}, valkeyAddresses: []string{"cache:6379"}, vaultAddress: "https://vault", s3Region: "us-east-1", payloadBucket: "payloads", importBucket: "imports", databaseMaxConnections: 32, s3Encryption: "AES256"}
	if err := configuration.validate(); err == nil {
		t.Fatal("production configuration without mTLS and file credentials accepted")
	}
	configuration.allowInsecureDevelopment = true
	if err := configuration.validate(); err != nil {
		t.Fatal(err)
	}
}
