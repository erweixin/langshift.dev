package main

import (
	"encoding/base64"
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
	databaseURL, databaseURLFile, runtimeDatabaseURL, runtimeDatabaseURLFile                                                    string
	epochURL, epochTokenFile, epochCAFile, epochCertFile, epochKeyFile, epochTLSName                                            string
	executionIDKeyFile, executionLeasePepperFile, llmIDKeyFile, llmTokenPepperFile, billingIDKeyFile                            string
	runtimeIDKeyFile, runtimeTokenPepperFile                                                                                    string
	s3Region, s3Endpoint, payloadBucket, payloadPrefix, s3KMSKeyID                                                              string
	s3PathStyle                                                                                                                 bool
	s3Encryption                                                                                                                types.ServerSideEncryption
	vaultAddress, vaultNamespace, vaultMount, vaultTokenFile, vaultCAFile, vaultCertFile, vaultKeyFile, vaultTLSName, keyPrefix string
	healthAddress, environment, serviceVersion, region                                                                          string
	otlpEndpoint, otlpCAFile, otlpCertFile, otlpKeyFile, otlpTLSName, otlpBearerTokenFile                                       string
	interval, cycleTimeout, unlockTimeout, executionLeaseTTL                                                                    time.Duration
	shardCount, tenantPage, cancellationPage                                                                                    int
	traceSampleRatio                                                                                                            float64
	allowInsecureDevelopment                                                                                                    bool
}

func loadConfig() (config, error) {
	allow, err := optionalBool("ALLOW_INSECURE_DEVELOPMENT", false)
	if err != nil {
		return config{}, err
	}
	value := config{
		databaseURL: os.Getenv("DATABASE_URL"), databaseURLFile: os.Getenv("DATABASE_URL_FILE"), runtimeDatabaseURL: os.Getenv("RUNTIME_DATABASE_URL"), runtimeDatabaseURLFile: os.Getenv("RUNTIME_DATABASE_URL_FILE"),
		epochURL: os.Getenv("STORE_EPOCH_URL"), epochTokenFile: os.Getenv("STORE_EPOCH_TOKEN_FILE"), epochCAFile: os.Getenv("STORE_EPOCH_ROOT_CA_FILE"), epochCertFile: os.Getenv("STORE_EPOCH_CLIENT_CERT_FILE"), epochKeyFile: os.Getenv("STORE_EPOCH_CLIENT_KEY_FILE"), epochTLSName: os.Getenv("STORE_EPOCH_TLS_SERVER_NAME"),
		executionIDKeyFile: os.Getenv("EXECUTION_ID_KEY_FILE"), executionLeasePepperFile: os.Getenv("EXECUTION_LEASE_PEPPER_FILE"), llmIDKeyFile: os.Getenv("LLM_ID_KEY_FILE"), llmTokenPepperFile: os.Getenv("LLM_TOKEN_PEPPER_FILE"), billingIDKeyFile: os.Getenv("BILLING_ID_KEY_FILE"),
		runtimeIDKeyFile: os.Getenv("RUNTIME_ID_KEY_FILE"), runtimeTokenPepperFile: os.Getenv("RUNTIME_TOKEN_PEPPER_FILE"),
		s3Region: os.Getenv("S3_REGION"), s3Endpoint: os.Getenv("S3_ENDPOINT"), payloadBucket: os.Getenv("S3_PAYLOAD_BUCKET"), payloadPrefix: env("S3_PAYLOAD_PREFIX", "restricted"), s3KMSKeyID: os.Getenv("S3_KMS_KEY_ID"), s3Encryption: types.ServerSideEncryption(env("S3_SERVER_SIDE_ENCRYPTION", string(types.ServerSideEncryptionAes256))),
		vaultAddress: os.Getenv("VAULT_ADDR"), vaultNamespace: os.Getenv("VAULT_NAMESPACE"), vaultMount: env("VAULT_KV_MOUNT", "secret"), vaultTokenFile: os.Getenv("VAULT_TOKEN_FILE"), vaultCAFile: os.Getenv("VAULT_CACERT"), vaultCertFile: os.Getenv("VAULT_CLIENT_CERT_FILE"), vaultKeyFile: os.Getenv("VAULT_CLIENT_KEY_FILE"), vaultTLSName: os.Getenv("VAULT_TLS_SERVER_NAME"), keyPrefix: env("VAULT_PAYLOAD_KEY_PREFIX", "lites/payload-keys"),
		healthAddress: env("HEALTH_ADDRESS", "127.0.0.1:8088"), environment: os.Getenv("LITES_ENVIRONMENT"), serviceVersion: os.Getenv("LITES_VERSION"), region: os.Getenv("LITES_REGION"),
		otlpEndpoint: os.Getenv("OTLP_GRPC_ENDPOINT"), otlpCAFile: os.Getenv("OTLP_ROOT_CA_FILE"), otlpCertFile: os.Getenv("OTLP_CLIENT_CERT_FILE"), otlpKeyFile: os.Getenv("OTLP_CLIENT_KEY_FILE"), otlpTLSName: os.Getenv("OTLP_TLS_SERVER_NAME"), otlpBearerTokenFile: os.Getenv("OTLP_BEARER_TOKEN_FILE"),
		allowInsecureDevelopment: allow,
	}
	if value.databaseURL, err = loadDatabaseURL(value.databaseURL, value.databaseURLFile); err != nil {
		return config{}, errors.New("execution database configuration is invalid")
	}
	if value.runtimeDatabaseURL, err = loadDatabaseURL(value.runtimeDatabaseURL, value.runtimeDatabaseURLFile); err != nil {
		return config{}, errors.New("Runtime database configuration is invalid")
	}
	if value.s3PathStyle, err = optionalBool("S3_PATH_STYLE", false); err != nil {
		return config{}, err
	}
	if value.interval, err = optionalDuration("RUN_CANCELLATION_INTERVAL", 2*time.Second); err != nil {
		return config{}, err
	}
	if value.cycleTimeout, err = optionalDuration("RUN_CANCELLATION_CYCLE_TIMEOUT", 90*time.Second); err != nil {
		return config{}, err
	}
	if value.unlockTimeout, err = optionalDuration("RUN_CANCELLATION_UNLOCK_TIMEOUT", 3*time.Second); err != nil {
		return config{}, err
	}
	if value.executionLeaseTTL, err = optionalDuration("EXECUTION_LEASE_TTL", 2*time.Minute); err != nil {
		return config{}, err
	}
	if value.shardCount, err = optionalInt("RUN_CANCELLATION_SHARD_COUNT", 16); err != nil {
		return config{}, err
	}
	if value.tenantPage, err = optionalInt("RUN_CANCELLATION_TENANT_PAGE", 500); err != nil {
		return config{}, err
	}
	if value.cancellationPage, err = optionalInt("RUN_CANCELLATION_PAGE", 250); err != nil {
		return config{}, err
	}
	if value.traceSampleRatio, err = optionalFloat("TRACE_SAMPLE_RATIO", 0.1); err != nil {
		return config{}, err
	}
	return value, value.validate()
}

func (value config) validate() error {
	required := []string{value.databaseURL, value.runtimeDatabaseURL, value.epochURL, value.executionIDKeyFile, value.executionLeasePepperFile, value.llmIDKeyFile, value.llmTokenPepperFile, value.billingIDKeyFile, value.runtimeIDKeyFile, value.runtimeTokenPepperFile, value.s3Region, value.payloadBucket, value.vaultAddress, value.vaultMount, value.keyPrefix, value.healthAddress, value.environment, value.serviceVersion, value.region}
	for _, item := range required {
		if item == "" {
			return errors.New("required run cancellation reconciler configuration is missing")
		}
	}
	if value.interval <= 0 || value.cycleTimeout < value.interval || value.cycleTimeout > 15*time.Minute || value.unlockTimeout <= 0 || value.unlockTimeout > 10*time.Second || value.executionLeaseTTL <= 0 || value.executionLeaseTTL > 15*time.Minute || value.shardCount < 1 || value.shardCount > 256 || value.tenantPage < 1 || value.tenantPage > 5000 || value.cancellationPage < 1 || value.cancellationPage > 500 || value.traceSampleRatio < 0 || value.traceSampleRatio > 1 || (value.epochCertFile == "") != (value.epochKeyFile == "") || (value.vaultCertFile == "") != (value.vaultKeyFile == "") || (value.otlpCertFile == "") != (value.otlpKeyFile == "") {
		return errors.New("run cancellation reconciler configuration is invalid")
	}
	if value.s3Encryption == types.ServerSideEncryptionAwsKms && value.s3KMSKeyID == "" || value.s3Encryption != types.ServerSideEncryptionAwsKms && value.s3Encryption != types.ServerSideEncryptionAes256 {
		return errors.New("run cancellation reconciler S3 encryption is invalid")
	}
	parsed, err := url.Parse(value.epochURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || (!value.allowInsecureDevelopment && parsed.Scheme != "https") || value.allowInsecureDevelopment && parsed.Scheme == "http" && !loopback(parsed.Hostname()) {
		return errors.New("run cancellation reconciler epoch endpoint is invalid")
	}
	if !value.allowInsecureDevelopment && (value.databaseURLFile == "" || value.runtimeDatabaseURLFile == "" || value.epochCAFile == "" || value.epochTokenFile == "" && value.epochCertFile == "" || value.vaultCAFile == "" || value.vaultTokenFile == "" && value.vaultCertFile == "" || value.otlpEndpoint == "" || value.otlpBearerTokenFile == "" && value.otlpCertFile == "") {
		return errors.New("production run cancellation reconciler requires file-backed authenticated dependencies")
	}
	return nil
}

func loadDatabaseURL(value, file string) (string, error) {
	if value != "" && file != "" {
		return "", errors.New("database URL and file are mutually exclusive")
	}
	if file != "" {
		return readSecret(file, 8192)
	}
	if value == "" {
		return "", errors.New("database URL is missing")
	}
	return value, nil
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

func readBase64Key(path string) ([]byte, error) {
	value, err := readSecret(path, 256)
	if err != nil {
		return nil, err
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return nil, errors.New("invalid 32-byte key")
	}
	return decoded, nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func optionalBool(name string, fallback bool) (bool, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	return strconv.ParseBool(value)
}

func optionalInt(name string, fallback int) (int, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	return strconv.Atoi(value)
}

func optionalDuration(name string, fallback time.Duration) (time.Duration, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	return time.ParseDuration(value)
}

func optionalFloat(name string, fallback float64) (float64, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
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
