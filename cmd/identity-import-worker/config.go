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
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

var workerUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type config struct {
	databaseURL, databaseURLFile, healthAddress                                                        string
	epochURL, epochTokenFile, epochCAFile, epochCertFile, epochKeyFile                                 string
	natsURLs                                                                                           []string
	natsCredentialsFile, natsCAFile, natsCertFile, natsKeyFile, streamName, consumerName               string
	vaultAddress, vaultNamespace, vaultMount, vaultTokenFile, vaultCAFile, vaultCertFile, vaultKeyFile string
	vaultTLSServerName, vaultKeyPrefix                                                                 string
	s3Region, s3Endpoint, payloadBucket, payloadPrefix, importBucket, importPrefix, s3KMSKeyID         string
	inboxPepperFile, invitationPepperFile, claimIdentityKeyFile, publicTenantID                        string
	contentReleaseDirectory                                                                            string
	environment, serviceVersion, region                                                                string
	otlpEndpoint, otlpCAFile, otlpCertFile, otlpKeyFile, otlpTLSName, otlpBearerTokenFile              string
	s3Encryption                                                                                       types.ServerSideEncryption
	allowInsecureDevelopment, s3PathStyle                                                              bool
	streamReplicas, concurrency                                                                        int
	traceSampleRatio                                                                                   float64
}

func loadConfig() (config, error) {
	allowInsecure, err := optionalBool("ALLOW_INSECURE_DEVELOPMENT", false)
	if err != nil {
		return config{}, err
	}
	replicas, err := optionalInt("NATS_STREAM_REPLICAS", 3)
	if err != nil {
		return config{}, err
	}
	concurrency, err := optionalInt("WORKER_CONCURRENCY", 8)
	if err != nil {
		return config{}, err
	}
	s3PathStyle, err := optionalBool("S3_PATH_STYLE", false)
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
	encryption := types.ServerSideEncryption(envString("S3_SERVER_SIDE_ENCRYPTION", string(types.ServerSideEncryptionAes256)))
	value := config{
		databaseURL: databaseURL, databaseURLFile: databaseURLFile, healthAddress: envString("HEALTH_ADDRESS", "127.0.0.1:8082"),
		epochURL: os.Getenv("STORE_EPOCH_URL"), epochTokenFile: os.Getenv("STORE_EPOCH_TOKEN_FILE"), epochCAFile: os.Getenv("STORE_EPOCH_ROOT_CA_FILE"), epochCertFile: os.Getenv("STORE_EPOCH_CLIENT_CERT_FILE"), epochKeyFile: os.Getenv("STORE_EPOCH_CLIENT_KEY_FILE"),
		natsURLs: splitNonempty(os.Getenv("NATS_URLS")), natsCredentialsFile: os.Getenv("NATS_CREDENTIALS_FILE"), natsCAFile: os.Getenv("NATS_ROOT_CA_FILE"), natsCertFile: os.Getenv("NATS_CLIENT_CERT_FILE"), natsKeyFile: os.Getenv("NATS_CLIENT_KEY_FILE"), streamName: envString("NATS_COMMAND_STREAM", "LITES_COMMANDS"), consumerName: envString("NATS_IDENTITY_IMPORT_CONSUMER", "IDENTITY_IMPORT_WORKER"),
		vaultAddress: os.Getenv("VAULT_ADDR"), vaultNamespace: os.Getenv("VAULT_NAMESPACE"), vaultMount: envString("VAULT_PAYLOAD_KEY_MOUNT", "secret"), vaultTokenFile: os.Getenv("VAULT_TOKEN_FILE"), vaultCAFile: os.Getenv("VAULT_CACERT"), vaultCertFile: os.Getenv("VAULT_CLIENT_CERT_FILE"), vaultKeyFile: os.Getenv("VAULT_CLIENT_KEY_FILE"), vaultTLSServerName: os.Getenv("VAULT_TLS_SERVER_NAME"), vaultKeyPrefix: envString("VAULT_PAYLOAD_KEY_PREFIX", "lites/payload-keys"),
		s3Region: os.Getenv("S3_REGION"), s3Endpoint: os.Getenv("S3_ENDPOINT"), s3PathStyle: s3PathStyle, payloadBucket: os.Getenv("S3_PAYLOAD_BUCKET"), payloadPrefix: envString("S3_PAYLOAD_PREFIX", "restricted"), importBucket: os.Getenv("S3_IMPORT_BUCKET"), importPrefix: envString("S3_IMPORT_PREFIX", "identity-imports"), s3Encryption: encryption, s3KMSKeyID: os.Getenv("S3_KMS_KEY_ID"),
		inboxPepperFile: os.Getenv("IDENTITY_INBOX_LEASE_PEPPER_FILE"), invitationPepperFile: os.Getenv("IDENTITY_INVITATION_TOKEN_PEPPER_FILE"), claimIdentityKeyFile: os.Getenv("CLAIM_IDENTITY_KEY_FILE"), publicTenantID: os.Getenv("IDENTITY_PUBLIC_TENANT_ID"), allowInsecureDevelopment: allowInsecure, streamReplicas: replicas, concurrency: concurrency,
		contentReleaseDirectory: envString("PRODUCT_CONTENT_RELEASE_DIR", "/app/product-content/releases/1.0.0"),
		environment:             os.Getenv("LITES_ENVIRONMENT"), serviceVersion: os.Getenv("LITES_VERSION"), region: os.Getenv("LITES_REGION"), otlpEndpoint: os.Getenv("OTLP_GRPC_ENDPOINT"), otlpCAFile: os.Getenv("OTLP_ROOT_CA_FILE"), otlpCertFile: os.Getenv("OTLP_CLIENT_CERT_FILE"), otlpKeyFile: os.Getenv("OTLP_CLIENT_KEY_FILE"), otlpTLSName: os.Getenv("OTLP_TLS_SERVER_NAME"), otlpBearerTokenFile: os.Getenv("OTLP_BEARER_TOKEN_FILE"), traceSampleRatio: traceSampleRatio,
	}
	if value.databaseURL == "" || value.epochURL == "" || len(value.natsURLs) == 0 || value.vaultAddress == "" || value.s3Region == "" || value.payloadBucket == "" || value.importBucket == "" || value.inboxPepperFile == "" || value.invitationPepperFile == "" || value.claimIdentityKeyFile == "" || value.contentReleaseDirectory == "" || !workerUUIDPattern.MatchString(value.publicTenantID) || value.environment == "" || value.serviceVersion == "" || value.region == "" || value.traceSampleRatio < 0 || value.traceSampleRatio > 1 || value.streamReplicas < 1 || value.concurrency < 1 || value.concurrency > 128 || (value.epochCertFile == "") != (value.epochKeyFile == "") || (value.natsCertFile == "") != (value.natsKeyFile == "") || (value.vaultCertFile == "") != (value.vaultKeyFile == "") || (value.otlpCertFile == "") != (value.otlpKeyFile == "") {
		return config{}, errors.New("required worker configuration is missing or invalid")
	}
	if value.s3Encryption == types.ServerSideEncryptionAwsKms && value.s3KMSKeyID == "" {
		return config{}, errors.New("S3_KMS_KEY_ID is required for aws:kms")
	}
	if value.s3Encryption != types.ServerSideEncryptionAwsKms && value.s3Encryption != types.ServerSideEncryptionAes256 {
		return config{}, errors.New("S3_SERVER_SIDE_ENCRYPTION must be AES256 or aws:kms")
	}
	if !value.allowInsecureDevelopment && (value.databaseURLFile == "" || value.streamReplicas < 3 || (value.natsCredentialsFile == "" && value.natsCertFile == "") || (value.epochTokenFile == "" && value.epochCertFile == "") || (value.vaultTokenFile == "" && value.vaultCertFile == "") || value.otlpEndpoint == "" || (value.otlpBearerTokenFile == "" && value.otlpCertFile == "")) {
		return config{}, errors.New("production requires file-backed database credentials, three replicas, and authenticated NATS/epoch/Vault clients")
	}
	return value, nil
}

func newEpochHTTPClient(configuration config) (*http.Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13}
	if configuration.epochCAFile != "" {
		contents, err := os.ReadFile(configuration.epochCAFile)
		if err != nil {
			return nil, fmt.Errorf("load epoch CA: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil || !roots.AppendCertsFromPEM(contents) {
			return nil, errors.New("epoch CA is invalid")
		}
		tlsConfig.RootCAs = roots
	}
	if configuration.epochCertFile != "" {
		certificate, err := tls.LoadX509KeyPair(configuration.epochCertFile, configuration.epochKeyFile)
		if err != nil {
			return nil, errors.New("epoch client certificate is invalid")
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	return &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func readBase64Secret(path string) ([]byte, error) {
	encoded, err := readSecret(path, 8192)
	if err != nil {
		return nil, err
	}
	value, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(value) < 32 {
		return nil, errors.New("secret does not meet encoding or length requirements")
	}
	return value, nil
}

func readSecret(path string, limit int64) (string, error) {
	if path == "" {
		return "", errors.New("secret file path is empty")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, limit+1))
	value := strings.TrimSpace(string(contents))
	if err != nil || int64(len(contents)) > limit || value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("secret file is empty, ambiguous, or too large")
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
