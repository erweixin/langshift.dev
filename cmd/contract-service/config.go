package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type config struct {
	databaseURL, databaseURLFile                                                          string
	listenAddress, healthAddress                                                          string
	serverCertificateFile, serverKeyFile, serverClientCAFile                              string
	trustedKeyringFile, trustedIssuer, trustedAudience                                    string
	secretBundleFile                                                                      string
	epochURL, epochTokenFile, epochCAFile, epochCertFile, epochKeyFile                    string
	s3Region, s3Endpoint, payloadBucket, payloadPrefix, s3KMSKeyID                        string
	s3PathStyle                                                                           bool
	s3Encryption                                                                          types.ServerSideEncryption
	vaultAddress, vaultNamespace, vaultMount, vaultTokenFile, vaultCAFile                 string
	vaultCertFile, vaultKeyFile, vaultTLSName, vaultKeyPrefix                             string
	environment, serviceVersion, region                                                   string
	otlpEndpoint, otlpCAFile, otlpCertFile, otlpKeyFile, otlpTLSName, otlpBearerTokenFile string
	databaseMaxConnections                                                                int
	idempotencyTTL, proposalTTL                                                           time.Duration
	traceSampleRatio                                                                      float64
	allowInsecureDevelopment                                                              bool
}

type secretBundle struct {
	IDKey, IdempotencyPepper, RequestDigestPepper []byte
}

type encodedSecretBundle struct {
	Version             string `json:"version"`
	IDKey               string `json:"id_key"`
	IdempotencyPepper   string `json:"idempotency_pepper"`
	RequestDigestPepper string `json:"request_digest_pepper"`
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
	maxConnections, err := optionalInt("DATABASE_MAX_CONNECTIONS", 32)
	if err != nil {
		return config{}, err
	}
	idempotencyTTL, err := optionalDuration("IDEMPOTENCY_TTL", 24*time.Hour)
	if err != nil {
		return config{}, err
	}
	proposalTTL, err := optionalDuration("CONTRACT_PROPOSAL_TTL", 5*time.Minute)
	if err != nil {
		return config{}, err
	}
	traceSampleRatio, err := optionalFloat("TRACE_SAMPLE_RATIO", 0.1)
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
		listenAddress: env("LISTEN_ADDRESS", ":8443"), healthAddress: env("HEALTH_ADDRESS", "127.0.0.1:8089"),
		serverCertificateFile: os.Getenv("SERVER_TLS_CERT_FILE"), serverKeyFile: os.Getenv("SERVER_TLS_KEY_FILE"), serverClientCAFile: os.Getenv("SERVER_CLIENT_CA_FILE"),
		trustedKeyringFile: os.Getenv("TRUSTED_CONTEXT_KEYRING_FILE"), trustedIssuer: env("TRUSTED_CONTEXT_ISSUER", "lites-gateway"), trustedAudience: env("TRUSTED_CONTEXT_AUDIENCE", "contract-service"),
		secretBundleFile: os.Getenv("CONTRACT_SECRET_BUNDLE_FILE"),
		epochURL:         os.Getenv("STORE_EPOCH_URL"), epochTokenFile: os.Getenv("STORE_EPOCH_TOKEN_FILE"), epochCAFile: os.Getenv("STORE_EPOCH_ROOT_CA_FILE"), epochCertFile: os.Getenv("STORE_EPOCH_CLIENT_CERT_FILE"), epochKeyFile: os.Getenv("STORE_EPOCH_CLIENT_KEY_FILE"),
		s3Region: os.Getenv("S3_REGION"), s3Endpoint: os.Getenv("S3_ENDPOINT"), s3PathStyle: pathStyle, payloadBucket: os.Getenv("S3_PAYLOAD_BUCKET"), payloadPrefix: env("S3_PAYLOAD_PREFIX", "restricted/contracts"), s3Encryption: types.ServerSideEncryption(env("S3_SERVER_SIDE_ENCRYPTION", string(types.ServerSideEncryptionAes256))), s3KMSKeyID: os.Getenv("S3_KMS_KEY_ID"),
		vaultAddress: os.Getenv("VAULT_ADDR"), vaultNamespace: os.Getenv("VAULT_NAMESPACE"), vaultMount: env("VAULT_PAYLOAD_KEY_MOUNT", "secret"), vaultTokenFile: os.Getenv("VAULT_TOKEN_FILE"), vaultCAFile: os.Getenv("VAULT_CACERT"), vaultCertFile: os.Getenv("VAULT_CLIENT_CERT_FILE"), vaultKeyFile: os.Getenv("VAULT_CLIENT_KEY_FILE"), vaultTLSName: os.Getenv("VAULT_TLS_SERVER_NAME"), vaultKeyPrefix: env("VAULT_PAYLOAD_KEY_PREFIX", "lites/payload-keys/contracts"),
		environment: os.Getenv("LITES_ENVIRONMENT"), serviceVersion: os.Getenv("LITES_VERSION"), region: os.Getenv("LITES_REGION"),
		otlpEndpoint: os.Getenv("OTLP_GRPC_ENDPOINT"), otlpCAFile: os.Getenv("OTLP_ROOT_CA_FILE"), otlpCertFile: os.Getenv("OTLP_CLIENT_CERT_FILE"), otlpKeyFile: os.Getenv("OTLP_CLIENT_KEY_FILE"), otlpTLSName: os.Getenv("OTLP_TLS_SERVER_NAME"), otlpBearerTokenFile: os.Getenv("OTLP_BEARER_TOKEN_FILE"),
		idempotencyTTL: idempotencyTTL, proposalTTL: proposalTTL, traceSampleRatio: traceSampleRatio, allowInsecureDevelopment: allow,
	}
	return value, value.validate()
}

func (value config) validate() error {
	required := []string{value.databaseURL, value.listenAddress, value.healthAddress, value.trustedKeyringFile, value.trustedIssuer, value.trustedAudience, value.secretBundleFile, value.epochURL, value.s3Region, value.payloadBucket, value.vaultAddress, value.vaultMount, value.vaultKeyPrefix, value.environment, value.serviceVersion, value.region}
	for _, item := range required {
		if item == "" {
			return errors.New("required contract service configuration is missing")
		}
	}
	if value.listenAddress == value.healthAddress || value.allowInsecureDevelopment && !listenLoopback(value.healthAddress) || value.trustedAudience != "contract-service" || value.trustedIssuer == value.trustedAudience || value.databaseMaxConnections < 8 || value.databaseMaxConnections > 256 || value.idempotencyTTL < time.Hour || value.idempotencyTTL > 7*24*time.Hour || value.proposalTTL != 5*time.Minute || value.traceSampleRatio < 0 || value.traceSampleRatio > 1 || (value.serverCertificateFile == "") != (value.serverKeyFile == "") || (value.epochCertFile == "") != (value.epochKeyFile == "") || (value.vaultCertFile == "") != (value.vaultKeyFile == "") || (value.otlpCertFile == "") != (value.otlpKeyFile == "") {
		return errors.New("contract service configuration is invalid")
	}
	epochEndpoint, err := url.Parse(value.epochURL)
	if err != nil || epochEndpoint.Host == "" || epochEndpoint.User != nil || epochEndpoint.Path == "" || (!value.allowInsecureDevelopment && epochEndpoint.Scheme != "https") || value.allowInsecureDevelopment && epochEndpoint.Scheme == "http" && !loopback(epochEndpoint.Hostname()) {
		return errors.New("store epoch endpoint is invalid")
	}
	if value.s3Encryption == types.ServerSideEncryptionAwsKms && value.s3KMSKeyID == "" || value.s3Encryption != types.ServerSideEncryptionAwsKms && value.s3Encryption != types.ServerSideEncryptionAes256 {
		return errors.New("contract payload S3 encryption is invalid")
	}
	if value.allowInsecureDevelopment && value.serverCertificateFile == "" && !listenLoopback(value.listenAddress) {
		return errors.New("insecure contract service must bind to loopback")
	}
	if !value.allowInsecureDevelopment && (value.databaseURLFile == "" || value.serverCertificateFile == "" || value.serverClientCAFile == "" || value.epochCAFile == "" || value.epochTokenFile == "" && value.epochCertFile == "" || value.vaultCAFile == "" || value.vaultTokenFile == "" && value.vaultCertFile == "" || value.otlpEndpoint == "" || value.otlpBearerTokenFile == "" && value.otlpCertFile == "") {
		return errors.New("production contract service requires file-backed authenticated TLS dependencies")
	}
	return nil
}

func loadSecretBundle(path string) (secretBundle, error) {
	file, err := os.Open(path)
	if err != nil {
		return secretBundle{}, errors.New("contract secret bundle is unavailable")
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, 16*1024+1))
	if err != nil || len(encoded) > 16*1024 {
		return secretBundle{}, errors.New("contract secret bundle is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var source encodedSecretBundle
	if err = decoder.Decode(&source); err != nil {
		return secretBundle{}, errors.New("contract secret bundle is invalid")
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || source.Version != "1.0.0" {
		return secretBundle{}, errors.New("contract secret bundle is invalid")
	}
	values := []string{source.IDKey, source.IdempotencyPepper, source.RequestDigestPepper}
	decoded := make([][]byte, len(values))
	seen := map[string]struct{}{}
	for index, value := range values {
		decoded[index], err = base64.StdEncoding.DecodeString(value)
		if err != nil || len(decoded[index]) != 32 {
			return secretBundle{}, errors.New("contract secret bundle material is invalid")
		}
		fingerprint := base64.StdEncoding.EncodeToString(decoded[index])
		if _, duplicate := seen[fingerprint]; duplicate {
			return secretBundle{}, errors.New("contract secret bundle reuses purpose-separated material")
		}
		seen[fingerprint] = struct{}{}
	}
	return secretBundle{IDKey: decoded[0], IdempotencyPepper: decoded[1], RequestDigestPepper: decoded[2]}, nil
}

func readSecret(path string, limit int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, limit+1))
	value := strings.TrimSpace(string(contents))
	if err != nil || int64(len(contents)) > limit || value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("invalid secret")
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
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return fallback, nil
	}
	return strconv.ParseBool(value)
}

func optionalInt(name string, fallback int) (int, error) {
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return fallback, nil
	}
	return strconv.Atoi(value)
}

func optionalDuration(name string, fallback time.Duration) (time.Duration, error) {
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return fallback, nil
	}
	return time.ParseDuration(value)
}

func optionalFloat(name string, fallback float64) (float64, error) {
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return fallback, nil
	}
	return strconv.ParseFloat(value, 64)
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func listenLoopback(address string) bool {
	host, _, err := net.SplitHostPort(address)
	return err == nil && loopback(host)
}
