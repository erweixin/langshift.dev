package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type config struct {
	databaseURL, databaseURLFile, listenAddress, healthAddress                            string
	serverCertificateFile, serverKeyFile, serverClientCAFile                              string
	trustedKeyringFile, trustedIssuer, trustedAudience                                    string
	epochURL, epochTokenFile, epochCAFile, epochCertFile, epochKeyFile                    string
	secretBundleFile                                                                      string
	s3Region, s3Endpoint, payloadBucket, payloadPrefix, s3KMSKeyID                        string
	s3PathStyle                                                                           bool
	s3Encryption                                                                          types.ServerSideEncryption
	vaultAddress, vaultNamespace, vaultMount, vaultTokenFile, vaultCAFile                 string
	vaultCertFile, vaultKeyFile, vaultTLSName, vaultKeyPrefix                             string
	environment, serviceVersion, region                                                   string
	behaviorEnvironment                                                                   string
	otlpEndpoint, otlpCAFile, otlpCertFile, otlpKeyFile, otlpTLSName, otlpBearerTokenFile string
	databaseMaxConnections, runMaxSteps, runMaxAttempts, maximumCoachContextBytes         int
	runMaxCostMicrounits                                                                  int64
	idempotencyTTL, runTimeout, leaseTTL                                                  time.Duration
	traceSampleRatio                                                                      float64
	allowInsecureDevelopment                                                              bool
}

type secretBundle struct {
	IDKey, IdempotencyPepper, RequestDigestPepper, LeaseTokenPepper []byte
}

type encodedSecretBundle struct {
	Version             string `json:"version"`
	IDKey               string `json:"id_key"`
	IdempotencyPepper   string `json:"idempotency_pepper"`
	RequestDigestPepper string `json:"request_digest_pepper"`
	LeaseTokenPepper    string `json:"lease_token_pepper"`
}

func loadConfig() (config, error) {
	allow, err := optionalBool("ALLOW_INSECURE_DEVELOPMENT", false)
	if err != nil {
		return config{}, err
	}
	pathStyle, err := optionalBool("S3_PATH_STYLE", false)
	if err != nil {
		return config{}, err
	}
	maxConnections, err := optionalInt("DATABASE_MAX_CONNECTIONS", 48)
	if err != nil {
		return config{}, err
	}
	runMaxSteps, err := optionalInt("RUN_MAX_STEPS", 64)
	if err != nil {
		return config{}, err
	}
	runMaxAttempts, err := optionalInt("RUN_MAX_ATTEMPTS", 5)
	if err != nil {
		return config{}, err
	}
	maximumCoachContextBytes, err := optionalInt("COACH_CONTEXT_MAX_BYTES", 2<<20)
	if err != nil {
		return config{}, err
	}
	runMaxCost, err := optionalInt64("RUN_MAX_COST_MICROUNITS", 500000)
	if err != nil {
		return config{}, err
	}
	idempotencyTTL, err := optionalDuration("IDEMPOTENCY_TTL", 24*time.Hour)
	if err != nil {
		return config{}, err
	}
	runTimeout, err := optionalDuration("RUN_TIMEOUT", 2*time.Hour)
	if err != nil {
		return config{}, err
	}
	leaseTTL, err := optionalDuration("RUN_LEASE_TTL", 2*time.Minute)
	if err != nil {
		return config{}, err
	}
	traceRatio, err := optionalFloat("TRACE_SAMPLE_RATIO", 0.1)
	if err != nil {
		return config{}, err
	}
	databaseURL, databaseURLFile := os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_URL_FILE")
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
		databaseURL: databaseURL, databaseURLFile: databaseURLFile, databaseMaxConnections: maxConnections,
		listenAddress: env("LISTEN_ADDRESS", ":8443"), healthAddress: env("HEALTH_ADDRESS", "127.0.0.1:8088"),
		serverCertificateFile: os.Getenv("SERVER_TLS_CERT_FILE"), serverKeyFile: os.Getenv("SERVER_TLS_KEY_FILE"), serverClientCAFile: os.Getenv("SERVER_CLIENT_CA_FILE"),
		trustedKeyringFile: os.Getenv("TRUSTED_CONTEXT_KEYRING_FILE"), trustedIssuer: env("TRUSTED_CONTEXT_ISSUER", "lites-gateway"), trustedAudience: env("TRUSTED_CONTEXT_AUDIENCE", "agent-control-plane"),
		epochURL: os.Getenv("STORE_EPOCH_URL"), epochTokenFile: os.Getenv("STORE_EPOCH_TOKEN_FILE"), epochCAFile: os.Getenv("STORE_EPOCH_ROOT_CA_FILE"), epochCertFile: os.Getenv("STORE_EPOCH_CLIENT_CERT_FILE"), epochKeyFile: os.Getenv("STORE_EPOCH_CLIENT_KEY_FILE"),
		secretBundleFile: os.Getenv("AGENT_CONTROL_SECRET_BUNDLE_FILE"),
		s3Region:         os.Getenv("S3_REGION"), s3Endpoint: os.Getenv("S3_ENDPOINT"), s3PathStyle: pathStyle, payloadBucket: os.Getenv("S3_PAYLOAD_BUCKET"), payloadPrefix: env("S3_PAYLOAD_PREFIX", "restricted"), s3Encryption: types.ServerSideEncryption(env("S3_SERVER_SIDE_ENCRYPTION", string(types.ServerSideEncryptionAes256))), s3KMSKeyID: os.Getenv("S3_KMS_KEY_ID"),
		vaultAddress: os.Getenv("VAULT_ADDR"), vaultNamespace: os.Getenv("VAULT_NAMESPACE"), vaultMount: env("VAULT_PAYLOAD_KEY_MOUNT", "secret"), vaultTokenFile: os.Getenv("VAULT_TOKEN_FILE"), vaultCAFile: os.Getenv("VAULT_CACERT"), vaultCertFile: os.Getenv("VAULT_CLIENT_CERT_FILE"), vaultKeyFile: os.Getenv("VAULT_CLIENT_KEY_FILE"), vaultTLSName: os.Getenv("VAULT_TLS_SERVER_NAME"), vaultKeyPrefix: env("VAULT_PAYLOAD_KEY_PREFIX", "lites/payload-keys"),
		environment: os.Getenv("LITES_ENVIRONMENT"), serviceVersion: os.Getenv("LITES_VERSION"), region: os.Getenv("LITES_REGION"),
		behaviorEnvironment: env("CONTROL_BEHAVIOR_ENVIRONMENT", "production"),
		otlpEndpoint:        os.Getenv("OTLP_GRPC_ENDPOINT"), otlpCAFile: os.Getenv("OTLP_ROOT_CA_FILE"), otlpCertFile: os.Getenv("OTLP_CLIENT_CERT_FILE"), otlpKeyFile: os.Getenv("OTLP_CLIENT_KEY_FILE"), otlpTLSName: os.Getenv("OTLP_TLS_SERVER_NAME"), otlpBearerTokenFile: os.Getenv("OTLP_BEARER_TOKEN_FILE"),
		idempotencyTTL: idempotencyTTL, runTimeout: runTimeout, leaseTTL: leaseTTL, runMaxSteps: runMaxSteps, runMaxAttempts: runMaxAttempts, runMaxCostMicrounits: runMaxCost, maximumCoachContextBytes: maximumCoachContextBytes,
		traceSampleRatio: traceRatio, allowInsecureDevelopment: allow,
	}
	return value, value.validate()
}

func (value config) validate() error {
	required := []string{value.databaseURL, value.listenAddress, value.healthAddress, value.trustedKeyringFile, value.trustedIssuer, value.trustedAudience, value.epochURL, value.secretBundleFile, value.s3Region, value.payloadBucket, value.vaultAddress, value.vaultMount, value.vaultKeyPrefix, value.environment, value.serviceVersion, value.region}
	for _, item := range required {
		if item == "" {
			return errors.New("required agent control-plane configuration is missing")
		}
	}
	if value.listenAddress == value.healthAddress || value.databaseMaxConnections < 8 || value.databaseMaxConnections > 256 || value.runMaxSteps < 1 || value.runMaxSteps > 10000 || value.runMaxAttempts < 1 || value.runMaxAttempts > 100 || value.runMaxCostMicrounits < 1 || value.maximumCoachContextBytes < 64<<10 || value.maximumCoachContextBytes > 4<<20 || value.idempotencyTTL < time.Hour || value.idempotencyTTL > 7*24*time.Hour || value.runTimeout < time.Minute || value.runTimeout > 24*time.Hour || value.leaseTTL < 10*time.Second || value.leaseTTL > 10*time.Minute || value.traceSampleRatio < 0 || value.traceSampleRatio > 1 || value.behaviorEnvironment != "staging" && value.behaviorEnvironment != "production" || (value.serverCertificateFile == "") != (value.serverKeyFile == "") || (value.serverCertificateFile != "" && value.serverClientCAFile == "") || (value.epochCertFile == "") != (value.epochKeyFile == "") || (value.vaultCertFile == "") != (value.vaultKeyFile == "") || (value.otlpCertFile == "") != (value.otlpKeyFile == "") {
		return errors.New("agent control-plane configuration is invalid")
	}
	if value.s3Encryption == types.ServerSideEncryptionAwsKms && value.s3KMSKeyID == "" {
		return errors.New("S3_KMS_KEY_ID is required for aws:kms")
	}
	if value.s3Encryption != types.ServerSideEncryptionAwsKms && value.s3Encryption != types.ServerSideEncryptionAes256 {
		return errors.New("S3_SERVER_SIDE_ENCRYPTION must be AES256 or aws:kms")
	}
	if !value.allowInsecureDevelopment && (value.databaseURLFile == "" || value.serverCertificateFile == "" || value.serverClientCAFile == "" || (value.epochTokenFile == "" && value.epochCertFile == "") || (value.vaultTokenFile == "" && value.vaultCertFile == "") || value.otlpEndpoint == "" || (value.otlpBearerTokenFile == "" && value.otlpCertFile == "")) {
		return errors.New("production requires file-backed credentials and authenticated TLS dependencies")
	}
	return nil
}

func loadSecretBundle(path string) (secretBundle, error) {
	file, err := os.Open(path)
	if err != nil {
		return secretBundle{}, errors.New("agent control secret bundle is unavailable")
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil || len(encoded) > 64*1024 {
		return secretBundle{}, errors.New("agent control secret bundle is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var source encodedSecretBundle
	if err = decoder.Decode(&source); err != nil {
		return secretBundle{}, errors.New("agent control secret bundle is invalid")
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || source.Version != "1.0.0" {
		return secretBundle{}, errors.New("agent control secret bundle is invalid")
	}
	values := []string{source.IDKey, source.IdempotencyPepper, source.RequestDigestPepper, source.LeaseTokenPepper}
	decoded := make([][]byte, len(values))
	seen := map[string]struct{}{}
	for index, value := range values {
		decoded[index], err = base64.StdEncoding.DecodeString(value)
		if err != nil || len(decoded[index]) != 32 {
			return secretBundle{}, errors.New("agent control secret material is invalid")
		}
		fingerprint := base64.StdEncoding.EncodeToString(decoded[index])
		if _, duplicate := seen[fingerprint]; duplicate {
			return secretBundle{}, errors.New("agent control secret material is not purpose-separated")
		}
		seen[fingerprint] = struct{}{}
	}
	return secretBundle{IDKey: decoded[0], IdempotencyPepper: decoded[1], RequestDigestPepper: decoded[2], LeaseTokenPepper: decoded[3]}, nil
}

func readSecret(path string, limit int64) (string, error) {
	if path == "" {
		return "", errors.New("secret path is empty")
	}
	contents, err := os.ReadFile(path)
	value := strings.TrimSpace(string(contents))
	if err != nil || int64(len(contents)) > limit || value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("secret is empty, ambiguous, or too large")
	}
	return value, nil
}

func env(name, fallback string) string {
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

func optionalInt64(name string, fallback int64) (int64, error) {
	value, found := os.LookupEnv(name)
	if !found {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
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

func optionalDuration(name string, fallback time.Duration) (time.Duration, error) {
	value, found := os.LookupEnv(name)
	if !found {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration", name)
	}
	return parsed, nil
}
