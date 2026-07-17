package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type config struct {
	databaseURL, databaseURLFile, healthAddress                                          string
	epochURL, epochTokenFile, epochCAFile, epochCertFile, epochKeyFile                   string
	natsURLs                                                                             []string
	natsCredentialsFile, natsCAFile, natsCertFile, natsKeyFile, streamName, consumerName string
	vaultAddress, vaultNamespace, vaultPayloadMount, vaultTransitMount                   string
	vaultTokenFile, vaultCAFile, vaultCertFile, vaultKeyFile, vaultTLSName               string
	vaultPayloadKeyPrefix                                                                string
	s3Region, s3Endpoint, payloadBucket, payloadPrefix, artifactBucket, artifactPrefix   string
	s3KMSKeyID                                                                           string
	valkeyAddresses                                                                      []string
	valkeyUsername, valkeyPasswordFile, valkeyCAFile, valkeyCertFile, valkeyKeyFile      string
	valkeyTLSName                                                                        string
	inboxPepperFile, identityKeyFile, receiptKeyFile                                     string
	environment, serviceVersion, region                                                  string
	otlpEndpoint, otlpCAFile, otlpCertFile, otlpKeyFile, otlpTLSName                     string
	otlpBearerTokenFile                                                                  string
	s3Encryption                                                                         types.ServerSideEncryption
	allowInsecureDevelopment, s3PathStyle                                                bool
	streamReplicas, concurrency                                                          int
	restoreInterval                                                                      time.Duration
	traceSampleRatio                                                                     float64
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
	replicas, err := optionalInt("NATS_STREAM_REPLICAS", 3)
	if err != nil {
		return config{}, err
	}
	concurrency, err := optionalInt("WORKER_CONCURRENCY", 4)
	if err != nil {
		return config{}, err
	}
	restoreInterval, err := optionalDuration("ERASURE_RESTORE_RECONCILE_INTERVAL", time.Minute)
	if err != nil {
		return config{}, err
	}
	traceRatio, err := optionalFloat("TRACE_SAMPLE_RATIO", 0.1)
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
		databaseURL: databaseURL, databaseURLFile: databaseURLFile, healthAddress: envString("HEALTH_ADDRESS", "127.0.0.1:8082"),
		epochURL: os.Getenv("STORE_EPOCH_URL"), epochTokenFile: os.Getenv("STORE_EPOCH_TOKEN_FILE"), epochCAFile: os.Getenv("STORE_EPOCH_ROOT_CA_FILE"), epochCertFile: os.Getenv("STORE_EPOCH_CLIENT_CERT_FILE"), epochKeyFile: os.Getenv("STORE_EPOCH_CLIENT_KEY_FILE"),
		natsURLs: splitNonempty(os.Getenv("NATS_URLS")), natsCredentialsFile: os.Getenv("NATS_CREDENTIALS_FILE"), natsCAFile: os.Getenv("NATS_ROOT_CA_FILE"), natsCertFile: os.Getenv("NATS_CLIENT_CERT_FILE"), natsKeyFile: os.Getenv("NATS_CLIENT_KEY_FILE"), streamName: envString("NATS_COMMAND_STREAM", "LITES_COMMANDS"), consumerName: envString("NATS_ACCOUNT_ERASURE_CONSUMER", "ACCOUNT_ERASURE_WORKER"),
		vaultAddress: os.Getenv("VAULT_ADDR"), vaultNamespace: os.Getenv("VAULT_NAMESPACE"), vaultPayloadMount: envString("VAULT_PAYLOAD_KEY_MOUNT", "secret"), vaultTransitMount: envString("VAULT_MEMORY_TRANSIT_MOUNT", "transit"), vaultTokenFile: os.Getenv("VAULT_TOKEN_FILE"), vaultCAFile: os.Getenv("VAULT_CACERT"), vaultCertFile: os.Getenv("VAULT_CLIENT_CERT_FILE"), vaultKeyFile: os.Getenv("VAULT_CLIENT_KEY_FILE"), vaultTLSName: os.Getenv("VAULT_TLS_SERVER_NAME"), vaultPayloadKeyPrefix: envString("VAULT_PAYLOAD_KEY_PREFIX", "lites/payload-keys"),
		s3Region: os.Getenv("S3_REGION"), s3Endpoint: os.Getenv("S3_ENDPOINT"), s3PathStyle: pathStyle, payloadBucket: os.Getenv("S3_PAYLOAD_BUCKET"), payloadPrefix: envString("S3_PAYLOAD_PREFIX", "restricted"), artifactBucket: os.Getenv("S3_ARTIFACT_BUCKET"), artifactPrefix: envString("S3_ARTIFACT_PREFIX", "artifacts"), s3KMSKeyID: os.Getenv("S3_KMS_KEY_ID"), s3Encryption: types.ServerSideEncryption(envString("S3_SERVER_SIDE_ENCRYPTION", string(types.ServerSideEncryptionAes256))),
		valkeyAddresses: splitNonempty(os.Getenv("VALKEY_ADDRESSES")), valkeyUsername: os.Getenv("VALKEY_USERNAME"), valkeyPasswordFile: os.Getenv("VALKEY_PASSWORD_FILE"), valkeyCAFile: os.Getenv("VALKEY_ROOT_CA_FILE"), valkeyCertFile: os.Getenv("VALKEY_CLIENT_CERT_FILE"), valkeyKeyFile: os.Getenv("VALKEY_CLIENT_KEY_FILE"), valkeyTLSName: os.Getenv("VALKEY_TLS_SERVER_NAME"),
		inboxPepperFile: os.Getenv("ERASURE_INBOX_LEASE_PEPPER_FILE"), identityKeyFile: os.Getenv("ERASURE_IDENTITY_KEY_FILE"), receiptKeyFile: os.Getenv("ERASURE_RECEIPT_KEY_FILE"),
		environment: os.Getenv("LITES_ENVIRONMENT"), serviceVersion: os.Getenv("LITES_VERSION"), region: os.Getenv("LITES_REGION"), otlpEndpoint: os.Getenv("OTLP_GRPC_ENDPOINT"), otlpCAFile: os.Getenv("OTLP_ROOT_CA_FILE"), otlpCertFile: os.Getenv("OTLP_CLIENT_CERT_FILE"), otlpKeyFile: os.Getenv("OTLP_CLIENT_KEY_FILE"), otlpTLSName: os.Getenv("OTLP_TLS_SERVER_NAME"), otlpBearerTokenFile: os.Getenv("OTLP_BEARER_TOKEN_FILE"),
		allowInsecureDevelopment: allowInsecure, streamReplicas: replicas, concurrency: concurrency, restoreInterval: restoreInterval, traceSampleRatio: traceRatio,
	}
	if value.databaseURL == "" || value.epochURL == "" || len(value.natsURLs) == 0 || value.vaultAddress == "" || value.vaultPayloadMount == value.vaultTransitMount || value.s3Region == "" || value.payloadBucket == "" || value.artifactBucket == "" || len(value.valkeyAddresses) == 0 || value.inboxPepperFile == "" || value.identityKeyFile == "" || value.receiptKeyFile == "" || value.environment == "" || value.serviceVersion == "" || value.region == "" || value.streamReplicas < 1 || value.concurrency < 1 || value.concurrency > 64 || value.restoreInterval < 10*time.Second || value.restoreInterval > time.Hour || value.traceSampleRatio < 0 || value.traceSampleRatio > 1 || (value.epochCertFile == "") != (value.epochKeyFile == "") || (value.natsCertFile == "") != (value.natsKeyFile == "") || (value.vaultCertFile == "") != (value.vaultKeyFile == "") || (value.valkeyCertFile == "") != (value.valkeyKeyFile == "") || (value.otlpCertFile == "") != (value.otlpKeyFile == "") {
		return config{}, errors.New("required erasure worker configuration is missing or invalid")
	}
	if value.s3Encryption == types.ServerSideEncryptionAwsKms && value.s3KMSKeyID == "" {
		return config{}, errors.New("S3_KMS_KEY_ID is required for aws:kms")
	}
	if value.s3Encryption != types.ServerSideEncryptionAwsKms && value.s3Encryption != types.ServerSideEncryptionAes256 {
		return config{}, errors.New("S3_SERVER_SIDE_ENCRYPTION must be AES256 or aws:kms")
	}
	if !value.allowInsecureDevelopment && (value.databaseURLFile == "" || value.streamReplicas < 3 || (value.natsCredentialsFile == "" && value.natsCertFile == "") || (value.epochTokenFile == "" && value.epochCertFile == "") || (value.vaultTokenFile == "" && value.vaultCertFile == "") || (value.valkeyPasswordFile == "" && value.valkeyCertFile == "") || value.otlpEndpoint == "" || (value.otlpBearerTokenFile == "" && value.otlpCertFile == "")) {
		return config{}, errors.New("production requires file-backed credentials, mTLS or tokens, and three NATS replicas")
	}
	return value, nil
}

func newEpochHTTPClient(configuration config) (*http.Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13}
	if configuration.epochCAFile != "" {
		encoded, err := os.ReadFile(configuration.epochCAFile)
		if err != nil {
			return nil, err
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(encoded) {
			return nil, errors.New("invalid epoch root CA")
		}
		tlsConfig.RootCAs = roots
	}
	if configuration.epochCertFile != "" {
		certificate, err := tls.LoadX509KeyPair(configuration.epochCertFile, configuration.epochKeyFile)
		if err != nil {
			return nil, err
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig, MaxIdleConns: 8, IdleConnTimeout: 30 * time.Second}, Timeout: 5 * time.Second}, nil
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
		return "", errors.New("secret is invalid")
	}
	return value, nil
}

func readBase64Secret(path string) ([]byte, error) {
	encoded, err := readSecret(path, 4096)
	if err != nil {
		return nil, err
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(decoded) < 32 {
		return nil, errors.New("key is invalid")
	}
	return decoded, nil
}

func envString(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func splitNonempty(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func optionalBool(name string, fallback bool) (bool, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s is invalid", name)
	}
	return parsed, nil
}

func optionalInt(name string, fallback int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s is invalid", name)
	}
	return parsed, nil
}

func optionalFloat(name string, fallback float64) (float64, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("%s is invalid", name)
	}
	return parsed, nil
}

func optionalDuration(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s is invalid", name)
	}
	return parsed, nil
}
