package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

var (
	uuidPattern  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

type config struct {
	databaseURL, databaseURLFile, listenAddress, healthAddress                                     string
	serverCertificateFile, serverKeyFile, serverClientCAFile                                       string
	trustedKeyringFile, trustedIssuer, trustedAudience                                             string
	epochURL, epochTokenFile, epochCAFile, epochCertFile, epochKeyFile                             string
	secretBundleFile, publicTenantID, anonymousHandleKeyID, region, passwordRangeURL               string
	passwordRangeCAFile                                                                            string
	environment, serviceVersion                                                                    string
	passwordRangeAllowedHosts                                                                      []string
	otlpEndpoint, otlpCAFile, otlpCertFile, otlpKeyFile, otlpTLSName, otlpBearerTokenFile          string
	valkeyAddresses                                                                                []string
	valkeyUsername, valkeyPasswordFile, valkeyCAFile, valkeyCertFile, valkeyKeyFile, valkeyTLSName string
	vaultAddress, vaultNamespace, vaultMount, vaultTokenFile, vaultCAFile, vaultCertFile           string
	vaultKeyFile, vaultTLSServerName, vaultKeyPrefix                                               string
	s3Region, s3Endpoint, payloadBucket, payloadPrefix, importBucket, importPrefix, s3KMSKeyID     string
	s3Encryption                                                                                   types.ServerSideEncryption
	s3PathStyle, allowInsecureDevelopment                                                          bool
	databaseMaxConnections                                                                         int
	traceSampleRatio                                                                               float64
}

type secretBundle struct {
	Version                 string `json:"version"`
	PasswordPepper          []byte `json:"-"`
	VerificationTokenPepper []byte `json:"-"`
	PasswordResetPepper     []byte `json:"-"`
	EmailChangePepper       []byte `json:"-"`
	InvitationPepper        []byte `json:"-"`
	SessionPepper           []byte `json:"-"`
	CSRFPepper              []byte `json:"-"`
	IdempotencyPepper       []byte `json:"-"`
	RequestDigestPepper     []byte `json:"-"`
	IdentityKey             []byte `json:"-"`
	CursorKey               []byte `json:"-"`
	RateLimitPepper         []byte `json:"-"`
	AnonymousHandleKey      []byte `json:"-"`
	AnonymousHandlePepper   []byte `json:"-"`
	AnonymousCSRFKey        []byte `json:"-"`
}

type encodedSecretBundle struct {
	Version                 string `json:"version"`
	PasswordPepper          string `json:"password_pepper"`
	VerificationTokenPepper string `json:"verification_token_pepper"`
	PasswordResetPepper     string `json:"password_reset_pepper"`
	EmailChangePepper       string `json:"email_change_pepper"`
	InvitationPepper        string `json:"invitation_pepper"`
	SessionPepper           string `json:"session_pepper"`
	CSRFPepper              string `json:"csrf_pepper"`
	IdempotencyPepper       string `json:"idempotency_pepper"`
	RequestDigestPepper     string `json:"request_digest_pepper"`
	IdentityKey             string `json:"identity_key"`
	CursorKey               string `json:"cursor_key"`
	RateLimitPepper         string `json:"rate_limit_pepper"`
	AnonymousHandleKey      string `json:"anonymous_handle_signing_key"`
	AnonymousHandlePepper   string `json:"anonymous_handle_digest_pepper"`
	AnonymousCSRFKey        string `json:"anonymous_csrf_key"`
}

func loadConfig() (config, error) {
	allowInsecure, err := optionalBool("ALLOW_INSECURE_DEVELOPMENT", false)
	if err != nil {
		return config{}, err
	}
	pathStyle, err := optionalBool("S3_PATH_STYLE", false)
	if err != nil {
		return config{}, err
	}
	maxConnections, err := optionalInt("DATABASE_MAX_CONNECTIONS", 32)
	if err != nil {
		return config{}, err
	}
	traceSampleRatio, err := optionalFloat("TRACE_SAMPLE_RATIO", 0.1)
	if err != nil {
		return config{}, err
	}
	databaseURLFile := os.Getenv("DATABASE_URL_FILE")
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURLFile != "" {
		if databaseURL != "" {
			return config{}, errors.New("DATABASE_URL and DATABASE_URL_FILE are mutually exclusive")
		}
		databaseURL, err = readSecret(databaseURLFile, 8192)
		if err != nil {
			return config{}, errors.New("DATABASE_URL_FILE is unreadable")
		}
	}
	value := config{
		databaseURL: databaseURL, databaseURLFile: databaseURLFile, listenAddress: envString("LISTEN_ADDRESS", ":8443"), healthAddress: envString("HEALTH_ADDRESS", "127.0.0.1:8081"), databaseMaxConnections: maxConnections,
		serverCertificateFile: os.Getenv("SERVER_TLS_CERT_FILE"), serverKeyFile: os.Getenv("SERVER_TLS_KEY_FILE"), serverClientCAFile: os.Getenv("SERVER_CLIENT_CA_FILE"),
		trustedKeyringFile: os.Getenv("TRUSTED_CONTEXT_KEYRING_FILE"), trustedIssuer: envString("TRUSTED_CONTEXT_ISSUER", "lites-gateway"), trustedAudience: envString("TRUSTED_CONTEXT_AUDIENCE", "identity-service"),
		epochURL: os.Getenv("STORE_EPOCH_URL"), epochTokenFile: os.Getenv("STORE_EPOCH_TOKEN_FILE"), epochCAFile: os.Getenv("STORE_EPOCH_ROOT_CA_FILE"), epochCertFile: os.Getenv("STORE_EPOCH_CLIENT_CERT_FILE"), epochKeyFile: os.Getenv("STORE_EPOCH_CLIENT_KEY_FILE"),
		secretBundleFile: os.Getenv("IDENTITY_SECRET_BUNDLE_FILE"), publicTenantID: os.Getenv("IDENTITY_PUBLIC_TENANT_ID"), anonymousHandleKeyID: os.Getenv("ANONYMOUS_HANDLE_KEY_ID"), region: os.Getenv("LITES_REGION"), passwordRangeURL: envString("PASSWORD_RANGE_URL", "https://api.pwnedpasswords.com/range"), passwordRangeAllowedHosts: splitNonempty(envString("PASSWORD_RANGE_ALLOWED_HOSTS", "api.pwnedpasswords.com")), passwordRangeCAFile: os.Getenv("PASSWORD_RANGE_ROOT_CA_FILE"),
		environment: os.Getenv("LITES_ENVIRONMENT"), serviceVersion: os.Getenv("LITES_VERSION"), otlpEndpoint: os.Getenv("OTLP_GRPC_ENDPOINT"), otlpCAFile: os.Getenv("OTLP_ROOT_CA_FILE"), otlpCertFile: os.Getenv("OTLP_CLIENT_CERT_FILE"), otlpKeyFile: os.Getenv("OTLP_CLIENT_KEY_FILE"), otlpTLSName: os.Getenv("OTLP_TLS_SERVER_NAME"), otlpBearerTokenFile: os.Getenv("OTLP_BEARER_TOKEN_FILE"), traceSampleRatio: traceSampleRatio,
		valkeyAddresses: splitNonempty(os.Getenv("VALKEY_ADDRESSES")), valkeyUsername: os.Getenv("VALKEY_USERNAME"), valkeyPasswordFile: os.Getenv("VALKEY_PASSWORD_FILE"), valkeyCAFile: os.Getenv("VALKEY_ROOT_CA_FILE"), valkeyCertFile: os.Getenv("VALKEY_CLIENT_CERT_FILE"), valkeyKeyFile: os.Getenv("VALKEY_CLIENT_KEY_FILE"), valkeyTLSName: os.Getenv("VALKEY_TLS_SERVER_NAME"),
		vaultAddress: os.Getenv("VAULT_ADDR"), vaultNamespace: os.Getenv("VAULT_NAMESPACE"), vaultMount: envString("VAULT_PAYLOAD_KEY_MOUNT", "secret"), vaultTokenFile: os.Getenv("VAULT_TOKEN_FILE"), vaultCAFile: os.Getenv("VAULT_CACERT"), vaultCertFile: os.Getenv("VAULT_CLIENT_CERT_FILE"), vaultKeyFile: os.Getenv("VAULT_CLIENT_KEY_FILE"), vaultTLSServerName: os.Getenv("VAULT_TLS_SERVER_NAME"), vaultKeyPrefix: envString("VAULT_PAYLOAD_KEY_PREFIX", "lites/payload-keys"),
		s3Region: os.Getenv("S3_REGION"), s3Endpoint: os.Getenv("S3_ENDPOINT"), s3PathStyle: pathStyle, payloadBucket: os.Getenv("S3_PAYLOAD_BUCKET"), payloadPrefix: envString("S3_PAYLOAD_PREFIX", "restricted"), importBucket: os.Getenv("S3_IMPORT_BUCKET"), importPrefix: envString("S3_IMPORT_PREFIX", "identity-imports"), s3Encryption: types.ServerSideEncryption(envString("S3_SERVER_SIDE_ENCRYPTION", string(types.ServerSideEncryptionAes256))), s3KMSKeyID: os.Getenv("S3_KMS_KEY_ID"), allowInsecureDevelopment: allowInsecure,
	}
	return value, value.validate()
}

func (configuration config) validate() error {
	if configuration.databaseURL == "" || configuration.trustedKeyringFile == "" || configuration.epochURL == "" || configuration.secretBundleFile == "" || !uuidPattern.MatchString(configuration.publicTenantID) || !keyIDPattern.MatchString(configuration.anonymousHandleKeyID) || configuration.region == "" || configuration.environment == "" || configuration.serviceVersion == "" || len(configuration.passwordRangeAllowedHosts) == 0 || len(configuration.valkeyAddresses) == 0 || configuration.vaultAddress == "" || configuration.s3Region == "" || configuration.payloadBucket == "" || configuration.importBucket == "" || configuration.databaseMaxConnections < 8 || configuration.databaseMaxConnections > 256 || configuration.traceSampleRatio < 0 || configuration.traceSampleRatio > 1 || (configuration.serverCertificateFile == "") != (configuration.serverKeyFile == "") || (configuration.serverCertificateFile != "" && configuration.serverClientCAFile == "") || (configuration.epochCertFile == "") != (configuration.epochKeyFile == "") || (configuration.valkeyCertFile == "") != (configuration.valkeyKeyFile == "") || (configuration.vaultCertFile == "") != (configuration.vaultKeyFile == "") || (configuration.otlpCertFile == "") != (configuration.otlpKeyFile == "") {
		return errors.New("required identity service configuration is missing or invalid")
	}
	if configuration.s3Encryption == types.ServerSideEncryptionAwsKms && configuration.s3KMSKeyID == "" {
		return errors.New("S3_KMS_KEY_ID is required for aws:kms")
	}
	if configuration.s3Encryption != types.ServerSideEncryptionAwsKms && configuration.s3Encryption != types.ServerSideEncryptionAes256 {
		return errors.New("S3_SERVER_SIDE_ENCRYPTION must be AES256 or aws:kms")
	}
	if !configuration.allowInsecureDevelopment && (configuration.databaseURLFile == "" || configuration.serverCertificateFile == "" || configuration.serverClientCAFile == "" || (configuration.epochTokenFile == "" && configuration.epochCertFile == "") || (configuration.valkeyPasswordFile == "" && configuration.valkeyCertFile == "") || (configuration.vaultTokenFile == "" && configuration.vaultCertFile == "") || configuration.otlpEndpoint == "" || (configuration.otlpBearerTokenFile == "" && configuration.otlpCertFile == "")) {
		return errors.New("production requires file-backed credentials and authenticated TLS dependencies")
	}
	return nil
}

func loadSecretBundle(path string) (secretBundle, error) {
	file, err := os.Open(path)
	if err != nil {
		return secretBundle{}, errors.New("identity secret bundle is unavailable")
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil || len(encoded) > 64*1024 {
		return secretBundle{}, errors.New("identity secret bundle is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var source encodedSecretBundle
	if err = decoder.Decode(&source); err != nil {
		return secretBundle{}, errors.New("identity secret bundle is invalid")
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || source.Version != "1.0.0" {
		return secretBundle{}, errors.New("identity secret bundle is invalid")
	}
	values := []string{source.PasswordPepper, source.VerificationTokenPepper, source.PasswordResetPepper, source.EmailChangePepper, source.InvitationPepper, source.SessionPepper, source.CSRFPepper, source.IdempotencyPepper, source.RequestDigestPepper, source.IdentityKey, source.CursorKey, source.RateLimitPepper, source.AnonymousHandleKey, source.AnonymousHandlePepper, source.AnonymousCSRFKey}
	decoded := make([][]byte, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		decoded[index], err = base64.StdEncoding.DecodeString(value)
		if err != nil || len(decoded[index]) != 32 {
			return secretBundle{}, errors.New("identity secret bundle material is invalid")
		}
		fingerprint := base64.StdEncoding.EncodeToString(decoded[index])
		if _, duplicate := seen[fingerprint]; duplicate {
			return secretBundle{}, errors.New("identity secret bundle reuses purpose-separated material")
		}
		seen[fingerprint] = struct{}{}
	}
	return secretBundle{Version: source.Version, PasswordPepper: decoded[0], VerificationTokenPepper: decoded[1], PasswordResetPepper: decoded[2], EmailChangePepper: decoded[3], InvitationPepper: decoded[4], SessionPepper: decoded[5], CSRFPepper: decoded[6], IdempotencyPepper: decoded[7], RequestDigestPepper: decoded[8], IdentityKey: decoded[9], CursorKey: decoded[10], RateLimitPepper: decoded[11], AnonymousHandleKey: decoded[12], AnonymousHandlePepper: decoded[13], AnonymousCSRFKey: decoded[14]}, nil
}

func readSecret(path string, limit int64) (string, error) {
	if path == "" {
		return "", errors.New("secret path is empty")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, limit+1))
	value := strings.TrimSpace(string(contents))
	if err != nil || int64(len(contents)) > limit || value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("secret is empty, ambiguous, or too large")
	}
	return value, nil
}

func envString(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func optionalBool(name string, fallback bool) (bool, error) {
	value, found := os.LookupEnv(name)
	if !found {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	return parsed, nil
}

func optionalInt(name string, fallback int) (int, error) {
	value, found := os.LookupEnv(name)
	if !found {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return parsed, nil
}

func optionalFloat(name string, fallback float64) (float64, error) {
	value, found := os.LookupEnv(name)
	if !found {
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number", name)
	}
	return parsed, nil
}

func splitNonempty(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
