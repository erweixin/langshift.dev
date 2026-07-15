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
	databaseURL, databaseURLFile                                                                                                string
	epochURL, epochTokenFile, epochCAFile, epochCertFile, epochKeyFile, epochTLSName                                            string
	idKeyFile, tokenPepperFile                                                                                                  string
	s3Region, s3Endpoint, payloadBucket, payloadPrefix, s3KMSKeyID                                                              string
	s3PathStyle                                                                                                                 bool
	s3Encryption                                                                                                                types.ServerSideEncryption
	vaultAddress, vaultNamespace, vaultMount, vaultTokenFile, vaultCAFile, vaultCertFile, vaultKeyFile, vaultTLSName, keyPrefix string
	healthAddress, environment, serviceVersion, region                                                                          string
	otlpEndpoint, otlpCAFile, otlpCertFile, otlpKeyFile, otlpTLSName, otlpBearerTokenFile                                       string
	interval, cycleTimeout, unlockTimeout                                                                                       time.Duration
	shardCount, tenantPage, sessionPage                                                                                         int
	traceSampleRatio                                                                                                            float64
	allowInsecureDevelopment                                                                                                    bool
}

func loadConfig() (config, error) {
	allow, err := optionalBool("ALLOW_INSECURE_DEVELOPMENT", false)
	if err != nil {
		return config{}, err
	}
	value := config{
		databaseURL: os.Getenv("DATABASE_URL"), databaseURLFile: os.Getenv("DATABASE_URL_FILE"),
		epochURL: os.Getenv("STORE_EPOCH_URL"), epochTokenFile: os.Getenv("STORE_EPOCH_TOKEN_FILE"), epochCAFile: os.Getenv("STORE_EPOCH_ROOT_CA_FILE"), epochCertFile: os.Getenv("STORE_EPOCH_CLIENT_CERT_FILE"), epochKeyFile: os.Getenv("STORE_EPOCH_CLIENT_KEY_FILE"), epochTLSName: os.Getenv("STORE_EPOCH_TLS_SERVER_NAME"),
		idKeyFile: os.Getenv("RUNTIME_ID_KEY_FILE"), tokenPepperFile: os.Getenv("RUNTIME_TOKEN_PEPPER_FILE"),
		s3Region: os.Getenv("S3_REGION"), s3Endpoint: os.Getenv("S3_ENDPOINT"), payloadBucket: os.Getenv("S3_PAYLOAD_BUCKET"), payloadPrefix: env("S3_PAYLOAD_PREFIX", "restricted"), s3KMSKeyID: os.Getenv("S3_KMS_KEY_ID"), s3Encryption: types.ServerSideEncryption(env("S3_SERVER_SIDE_ENCRYPTION", string(types.ServerSideEncryptionAes256))),
		vaultAddress: os.Getenv("VAULT_ADDR"), vaultNamespace: os.Getenv("VAULT_NAMESPACE"), vaultMount: env("VAULT_KV_MOUNT", "secret"), vaultTokenFile: os.Getenv("VAULT_TOKEN_FILE"), vaultCAFile: os.Getenv("VAULT_CACERT"), vaultCertFile: os.Getenv("VAULT_CLIENT_CERT_FILE"), vaultKeyFile: os.Getenv("VAULT_CLIENT_KEY_FILE"), vaultTLSName: os.Getenv("VAULT_TLS_SERVER_NAME"), keyPrefix: env("VAULT_PAYLOAD_KEY_PREFIX", "lites/payload-keys"),
		healthAddress: env("HEALTH_ADDRESS", "127.0.0.1:8086"), environment: os.Getenv("LITES_ENVIRONMENT"), serviceVersion: os.Getenv("LITES_VERSION"), region: os.Getenv("LITES_REGION"),
		otlpEndpoint: os.Getenv("OTLP_GRPC_ENDPOINT"), otlpCAFile: os.Getenv("OTLP_ROOT_CA_FILE"), otlpCertFile: os.Getenv("OTLP_CLIENT_CERT_FILE"), otlpKeyFile: os.Getenv("OTLP_CLIENT_KEY_FILE"), otlpTLSName: os.Getenv("OTLP_TLS_SERVER_NAME"), otlpBearerTokenFile: os.Getenv("OTLP_BEARER_TOKEN_FILE"),
		allowInsecureDevelopment: allow,
	}
	if value.databaseURLFile != "" {
		if value.databaseURL != "" {
			return config{}, errors.New("DATABASE_URL and DATABASE_URL_FILE are mutually exclusive")
		}
		value.databaseURL, err = readSecret(value.databaseURLFile, 8192)
		if err != nil {
			return config{}, errors.New("DATABASE_URL_FILE is unreadable")
		}
	}
	if value.s3PathStyle, err = optionalBool("S3_PATH_STYLE", false); err != nil {
		return config{}, err
	}
	if value.interval, err = optionalDuration("RUNTIME_SWEEPER_INTERVAL", 5*time.Second); err != nil {
		return config{}, err
	}
	if value.cycleTimeout, err = optionalDuration("RUNTIME_SWEEPER_CYCLE_TIMEOUT", 4*time.Minute); err != nil {
		return config{}, err
	}
	if value.unlockTimeout, err = optionalDuration("RUNTIME_SWEEPER_UNLOCK_TIMEOUT", 3*time.Second); err != nil {
		return config{}, err
	}
	if value.shardCount, err = optionalInt("RUNTIME_SWEEPER_SHARD_COUNT", 16); err != nil {
		return config{}, err
	}
	if value.tenantPage, err = optionalInt("RUNTIME_SWEEPER_TENANT_PAGE", 500); err != nil {
		return config{}, err
	}
	if value.sessionPage, err = optionalInt("RUNTIME_SWEEPER_SESSION_PAGE", 250); err != nil {
		return config{}, err
	}
	if value.traceSampleRatio, err = optionalFloat("TRACE_SAMPLE_RATIO", 0.1); err != nil {
		return config{}, err
	}
	return value, value.validate()
}

func (value config) validate() error {
	required := []string{value.databaseURL, value.epochURL, value.idKeyFile, value.tokenPepperFile, value.s3Region, value.payloadBucket, value.vaultAddress, value.vaultMount, value.keyPrefix, value.healthAddress, value.environment, value.serviceVersion, value.region}
	for _, item := range required {
		if item == "" {
			return errors.New("required runtime sweeper configuration is missing")
		}
	}
	if value.interval <= 0 || value.cycleTimeout < value.interval || value.cycleTimeout > 15*time.Minute || value.unlockTimeout <= 0 || value.unlockTimeout > 10*time.Second || value.shardCount < 1 || value.shardCount > 256 || value.tenantPage < 1 || value.tenantPage > 5000 || value.sessionPage < 1 || value.sessionPage > 1000 || value.traceSampleRatio < 0 || value.traceSampleRatio > 1 || (value.epochCertFile == "") != (value.epochKeyFile == "") || (value.vaultCertFile == "") != (value.vaultKeyFile == "") || (value.otlpCertFile == "") != (value.otlpKeyFile == "") {
		return errors.New("runtime sweeper configuration is invalid")
	}
	if value.s3Encryption == types.ServerSideEncryptionAwsKms && value.s3KMSKeyID == "" || value.s3Encryption != types.ServerSideEncryptionAwsKms && value.s3Encryption != types.ServerSideEncryptionAes256 {
		return errors.New("runtime sweeper S3 encryption is invalid")
	}
	parsed, err := url.Parse(value.epochURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || (!value.allowInsecureDevelopment && parsed.Scheme != "https") || value.allowInsecureDevelopment && parsed.Scheme == "http" && !loopback(parsed.Hostname()) {
		return errors.New("runtime sweeper epoch endpoint is invalid")
	}
	if !value.allowInsecureDevelopment && (value.databaseURLFile == "" || value.epochCAFile == "" || value.epochTokenFile == "" && value.epochCertFile == "" || value.vaultCAFile == "" || value.vaultTokenFile == "" && value.vaultCertFile == "" || value.otlpEndpoint == "" || value.otlpBearerTokenFile == "" && value.otlpCertFile == "") {
		return errors.New("production runtime sweeper requires file-backed authenticated dependencies")
	}
	return nil
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
