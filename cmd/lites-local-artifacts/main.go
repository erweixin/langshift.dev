// lites-local-artifacts creates ephemeral credentials for the macOS Compose
// product stack. It is a development utility, not a production secret issuer.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/langshift/lites/internal/execution/scheduler"
	"github.com/langshift/lites/internal/localengineeringassets"
	"github.com/langshift/lites/internal/statuspage"
)

const (
	publicTenantID         = "20000000-0000-4000-8000-000000000001"
	behaviorAutomationUser = "20000000-0000-4000-8000-000000000003"
	storeEpoch             = "60000000-0000-4000-8000-000000000001"
	trustedContextKeyID    = "local-gateway-1"
	statusKeyID            = "local-status-1"
)

type fileRecord struct {
	Name   string `json:"name"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type manifest struct {
	SchemaVersion string       `json:"schema_version"`
	Kind          string       `json:"kind"`
	GeneratedAt   time.Time    `json:"generated_at"`
	ValidUntil    time.Time    `json:"valid_until"`
	Files         []fileRecord `json:"files"`
}

type writer struct {
	directory string
	files     []fileRecord
}

func main() {
	output := flag.String("output", "", "absolute, empty output directory")
	flag.Parse()
	if flag.NArg() != 0 || !filepath.IsAbs(*output) {
		fatal(errors.New("--output must be an absolute empty directory"))
	}
	if err := generate(*output, time.Now().UTC().Truncate(time.Second)); err != nil {
		fatal(err)
	}
	fmt.Printf("local Compose artifacts generated at %s\n", *output)
}

func generate(directory string, now time.Time) error {
	if err := prepareDirectory(directory); err != nil {
		return err
	}
	w := &writer{directory: directory}
	validFrom, validUntil := now.Add(-5*time.Minute), now.Add(24*time.Hour)

	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	caTemplate := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: "Lites local Compose CA"}, NotBefore: validFrom, NotAfter: validUntil, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPublic, caPrivate)
	if err != nil {
		return err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return err
	}
	if err = w.pem("local-ca.pem", "CERTIFICATE", caDER); err != nil {
		return err
	}
	for _, name := range []string{"api-gateway", "identity-service", "realtime-gateway", "product-service", "agent-control-plane", "behavior-control-plane", "contract-service", "store-epoch-authority", "local-mailbox"} {
		if err = issue(w, ca, caPrivate, name, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, validFrom, validUntil); err != nil {
			return err
		}
	}
	if err = issueForDNS(w, ca, caPrivate, "local-model-adapter", []string{localengineeringassets.ProviderHost, "password-range.lites.test"}, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, validFrom, validUntil); err != nil {
		return err
	}
	if err = issue(w, ca, caPrivate, "local-service-client", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, validFrom, validUntil); err != nil {
		return err
	}

	contextPublic, contextPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	sharedSession, sharedCSRF := secret(), secret()
	anonymousKey, anonymousDigest, anonymousCSRF := secret(), secret(), secret()
	gateway := map[string]any{
		"version": "1.0.0", "trusted_context_key_id": trustedContextKeyID,
		"trusted_context_private_key": base64.StdEncoding.EncodeToString(contextPrivate),
		"trusted_context_not_before":  validFrom, "trusted_context_not_after": validUntil,
		"session_pepper": sharedSession, "csrf_pepper": sharedCSRF, "fingerprint_pepper": secret(),
		"anonymous_handle_key_id": "local-anonymous-1", "anonymous_handle_signing_key": anonymousKey,
		"anonymous_handle_digest_pepper": anonymousDigest, "anonymous_csrf_key": anonymousCSRF,
	}
	keyring := map[string]any{"version": "1.0.0", "keys": []map[string]any{{"id": trustedContextKeyID, "public_key": base64.RawURLEncoding.EncodeToString(contextPublic), "not_before": validFrom, "not_after": validUntil}}}
	invitationPepper, identityKey := secret(), secret()
	identity := map[string]any{
		"version": "1.0.0", "password_pepper": secret(), "verification_token_pepper": secret(),
		"password_reset_pepper": secret(), "email_change_pepper": secret(), "invitation_pepper": invitationPepper,
		"session_pepper": sharedSession, "csrf_pepper": sharedCSRF, "idempotency_pepper": secret(),
		"request_digest_pepper": secret(), "identity_key": identityKey, "cursor_key": secret(),
		"rate_limit_pepper": secret(), "anonymous_handle_signing_key": anonymousKey,
		"anonymous_handle_digest_pepper": anonymousDigest, "anonymous_csrf_key": anonymousCSRF,
	}
	product := map[string]any{"version": "1.0.0", "id_key": secret(), "idempotency_pepper": secret(), "request_digest_pepper": secret(), "cursor_key": secret()}
	agentControl := map[string]any{"version": "1.0.0", "id_key": secret(), "idempotency_pepper": secret(), "request_digest_pepper": secret(), "lease_token_pepper": secret()}
	behavior := map[string]any{"version": "1.0.0", "id_key": secret(), "idempotency_pepper": secret(), "request_digest_pepper": secret()}
	contract := map[string]any{"version": "1.0.0", "id_key": secret(), "idempotency_pepper": secret(), "request_digest_pepper": secret()}
	for name, value := range map[string]any{"gateway-bundle.json": gateway, "trusted-context-keyring.json": keyring, "identity-bundle.json": identity, "product-bundle.json": product, "agent-control-bundle.json": agentControl, "behavior-bundle.json": behavior, "contract-bundle.json": contract} {
		if err = w.json(name, value); err != nil {
			return err
		}
	}
	behaviorKeys := make([]map[string]any, 0, 3)
	for _, purpose := range []string{"risk_owner", "release_owner", "rollback_automation"} {
		publicKey, _, keyErr := ed25519.GenerateKey(rand.Reader)
		if keyErr != nil {
			return keyErr
		}
		behaviorKeys = append(behaviorKeys, map[string]any{
			"id": purpose + "-local-1", "purpose": purpose,
			"public_key": base64.RawURLEncoding.EncodeToString(publicKey),
			"not_before": validFrom, "not_after": validUntil,
		})
	}
	if err = w.json("behavior-signing-keyring.json", map[string]any{"version": "1.0.0", "keys": behaviorKeys}); err != nil {
		return err
	}

	statusPublic, statusPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	statusDocument := statuspage.Document{SchemaVersion: 1, Overall: statuspage.Operational, GeneratedAt: now, ValidUntil: now.Add(5 * time.Minute), Components: []statuspage.Component{{ID: "local_product", Name: "Local product stack", State: statuspage.Operational}}, Incidents: []statuspage.Incident{}}
	signedStatus, err := statuspage.Sign(statusDocument, statusKeyID, statusPrivate, now)
	if err != nil {
		return err
	}
	if err = w.bytes("public-status.json", signedStatus); err != nil {
		return err
	}
	statusKeyring := map[string]any{"version": "1.0.0", "keys": []map[string]any{{"id": statusKeyID, "public_key": base64.RawURLEncoding.EncodeToString(statusPublic), "not_before": validFrom, "not_after": validUntil}}}
	if err = w.json("public-status-keyring.json", statusKeyring); err != nil {
		return err
	}

	agentBundle, err := localengineeringassets.Build(now)
	if err != nil {
		return err
	}
	for _, name := range []string{"prompts.json", "routes.json", "tools.json", "providers.json"} {
		if err = w.bytes(name, agentBundle.Files[name]); err != nil {
			return err
		}
	}
	if err = w.bytes("agent-release-manifest.json", append(agentBundle.Files["manifest.json"], '\n')); err != nil {
		return err
	}
	for artifact, filename := range map[string]string{"prompts": "prompt-artifact-hash", "routes": "model-route-artifact-hash", "tools": "tool-registry-artifact-hash", "providers": "provider-registry-artifact-hash"} {
		if err = w.bytes(filename, []byte(agentBundle.Manifest.Artifacts[artifact].SHA256+"\n")); err != nil {
			return err
		}
	}
	schedulerPolicy := scheduler.Config{
		Resources: map[string]scheduler.ResourcePolicy{"llm": {
			Capacity: 8, InteractiveReserved: 2, BackgroundReserved: 2,
			HighPriorityThreshold: 80, HighPriorityMaxPercentage: 75, QuantumUnits: 64,
		}},
		DefaultTenant: scheduler.TenantPolicy{Weight: 1, ActiveConcurrencyCap: 4, BurstUnits: 256, RefillUnitsPerSecond: 64},
		Tenants:       map[string]scheduler.TenantPolicy{}, BatchLimit: 100,
	}
	if err = scheduler.ValidateConfig(schedulerPolicy); err != nil {
		return errors.New("local scheduler policy is invalid")
	}
	if err = w.json("scheduler-config.json", schedulerPolicy); err != nil {
		return err
	}
	// MinIO requires a KMS for SSE-S3/AES256 requests. The local stack uses
	// one short-lived static KMS key so the real object-store encryption path
	// is exercised without introducing an external cloud KMS dependency.
	if err = w.bytes("minio-kms-secret-key", []byte("lites-local:"+secret())); err != nil {
		return err
	}

	postgresPassword := token()
	values := map[string]string{
		"store-epoch": storeEpoch, "epoch-token": token(), "vault-token": token(),
		"postgres-password": postgresPassword, "valkey-password": token(), "nats-password": token(),
		"minio-root-user": "liteslocal", "minio-root-password": token(), "grafana-admin-password": token(),
		"payload-key-seed":     secret(),
		"product-inbox-pepper": secret(), "identity-inbox-pepper": secret(), "outbox-lease-pepper": secret(),
		"public-tenant-id": publicTenantID, "behavior-automation-user-id": behaviorAutomationUser, "invitation-pepper": invitationPepper, "claim-identity-key": identityKey,
		"execution-id-key": secret(), "execution-lease-pepper": secret(), "llm-id-key": secret(), "llm-token-pepper": secret(), "billing-id-key": secret(), "agent-id-key": secret(),
		"scheduler-resource-pepper": secret(), "scheduler-dispatch-pepper": secret(),
		"model-adapter-token": token(), "reminder-token": token(),
	}
	for fileName, role := range map[string]string{
		"database-url-postgres": "postgres", "database-url-identity": "lites_identity_service",
		"database-url-product": "lites_product_service", "database-url-agent": "lites_agent_service",
		"database-url-scheduler": "lites_scheduler_service", "database-url-behavior": "lites_behavior_service",
		"database-url-contract": "lites_contract_service",
	} {
		values[fileName] = fmt.Sprintf("postgresql://%s:%s@postgres:5432/lites?sslmode=disable", role, postgresPassword)
	}
	for name, value := range values {
		if err = w.bytes(name, []byte(value+"\n")); err != nil {
			return err
		}
	}
	sort.Slice(w.files, func(left, right int) bool { return w.files[left].Name < w.files[right].Name })
	return w.json("manifest.json", manifest{SchemaVersion: "1.0.0", Kind: "lites-local-compose-artifacts", GeneratedAt: now, ValidUntil: validUntil, Files: append([]fileRecord(nil), w.files...)})
}

func prepareDirectory(directory string) error {
	clean := filepath.Clean(directory)
	if clean == string(filepath.Separator) || clean != directory {
		return errors.New("output directory is unsafe")
	}
	info, err := os.Lstat(clean)
	if errors.Is(err, os.ErrNotExist) {
		return os.Mkdir(clean, 0o700)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("output must be a real directory")
	}
	entries, err := os.ReadDir(clean)
	if err != nil || len(entries) != 0 {
		return errors.New("output directory must be empty")
	}
	return os.Chmod(clean, 0o700)
}

func issue(w *writer, ca *x509.Certificate, caPrivate ed25519.PrivateKey, name string, usages []x509.ExtKeyUsage, notBefore, notAfter time.Time) error {
	return issueForDNS(w, ca, caPrivate, name, []string{name}, usages, notBefore, notAfter)
}

func issueForDNS(w *writer, ca *x509.Certificate, caPrivate ed25519.PrivateKey, name string, dnsNames []string, usages []x509.ExtKeyUsage, notBefore, notAfter time.Time) error {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	template := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: name}, DNSNames: append([]string(nil), dnsNames...), NotBefore: notBefore, NotAfter: notAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: usages}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, publicKey, caPrivate)
	if err != nil {
		return err
	}
	encodedKey, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return err
	}
	if err = w.pem(name+".pem", "CERTIFICATE", der); err != nil {
		return err
	}
	return w.pem(name+"-key.pem", "PRIVATE KEY", encodedKey)
}

func (w *writer) pem(name, kind string, contents []byte) error {
	return w.bytes(name, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: contents}))
}

func (w *writer) json(name string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return w.bytes(name, append(encoded, '\n'))
}

func (w *writer) bytes(name string, contents []byte) error {
	if filepath.Base(name) != name || strings.ContainsAny(name, "\x00\r\n") || len(contents) == 0 {
		return errors.New("invalid local artifact")
	}
	path := filepath.Join(w.directory, name)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		return err
	}
	digest := sha256.Sum256(contents)
	w.files = append(w.files, fileRecord{Name: name, Bytes: len(contents), SHA256: hex.EncodeToString(digest[:])})
	return nil
}

func secret() string {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(value)
}

func token() string {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value)
}

func serial() *big.Int {
	limit := new(big.Int).Lsh(big.NewInt(1), 120)
	value, err := rand.Int(rand.Reader, limit)
	if err != nil {
		panic(err)
	}
	return value
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
