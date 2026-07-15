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
	databaseURL, databaseURLFile, runtimeDatabaseURL, runtimeDatabaseURLFile                                                                   string
	epochURL, epochTokenFile, epochCAFile, epochCertFile, epochKeyFile, epochTLSName                                                           string
	natsURLs                                                                                                                                   []string
	natsName, natsCredentialsFile, natsCAFile, natsCertFile, natsKeyFile, streamName, consumerName                                             string
	artifactPath, artifactHash, workerID, healthAddress                                                                                        string
	executionIDKeyFile, executionLeasePepperFile, runtimeIDKeyFile, runtimeNonceKeyFile                                                        string
	runtimeTokenPepperFile, provisionDerivationKeyFile, machineIdentityKeyFile                                                                 string
	capabilityPrivateKeyFile, capabilityKeyID, capabilityIssuer, capabilityAudience                                                            string
	hostEndpointsFile, hostCAFile, hostCertFile, hostKeyFile, hostTLSName                                                                      string
	s3Region, s3Endpoint, payloadBucket, payloadPrefix, s3KMSKeyID                                                                             string
	s3PathStyle                                                                                                                                bool
	s3Encryption                                                                                                                               types.ServerSideEncryption
	vaultAddress, vaultNamespace, vaultMount, vaultTokenFile, vaultCAFile, vaultCertFile, vaultKeyFile, vaultTLSName, vaultKeyPrefix           string
	environment, version, region, otlpEndpoint, otlpCAFile, otlpCertFile, otlpKeyFile, otlpTLSName, otlpTokenFile                              string
	allowInsecure                                                                                                                              bool
	streamReplicas, concurrency, maxDeliver, maxAckPending, maximumCommand, maximumInput, maximumResult                                        int
	ackWait, ackTimeout, pullExpires, messageHeartbeat, busyDelay, retryDelay, executionLeaseTTL, toolHeartbeat, cleanupTimeout, capabilityTTL time.Duration
	traceRatio                                                                                                                                 float64
	scratchBandwidthSize, scratchBandwidthBurst, scratchOperationsSize, scratchOperationsBurst                                                 int64
	scratchRefillMillis                                                                                                                        int64
}

func loadConfig() (config, error) {
	allow, err := optionalBool("ALLOW_INSECURE_DEVELOPMENT", false)
	if err != nil {
		return config{}, err
	}
	value := config{
		databaseURL: os.Getenv("DATABASE_URL"), databaseURLFile: os.Getenv("DATABASE_URL_FILE"), runtimeDatabaseURL: os.Getenv("RUNTIME_DATABASE_URL"), runtimeDatabaseURLFile: os.Getenv("RUNTIME_DATABASE_URL_FILE"),
		epochURL: os.Getenv("STORE_EPOCH_URL"), epochTokenFile: os.Getenv("STORE_EPOCH_TOKEN_FILE"), epochCAFile: os.Getenv("STORE_EPOCH_ROOT_CA_FILE"), epochCertFile: os.Getenv("STORE_EPOCH_CLIENT_CERT_FILE"), epochKeyFile: os.Getenv("STORE_EPOCH_CLIENT_KEY_FILE"), epochTLSName: os.Getenv("STORE_EPOCH_TLS_SERVER_NAME"),
		natsURLs: split(os.Getenv("NATS_URLS")), natsName: env("NATS_CLIENT_NAME", "lites-tool-worker"), natsCredentialsFile: os.Getenv("NATS_CREDENTIALS_FILE"), natsCAFile: os.Getenv("NATS_ROOT_CA_FILE"), natsCertFile: os.Getenv("NATS_CLIENT_CERT_FILE"), natsKeyFile: os.Getenv("NATS_CLIENT_KEY_FILE"), streamName: env("NATS_COMMAND_STREAM", "LITES_COMMANDS"), consumerName: env("NATS_CONSUMER_NAME", "tool-worker-v1"),
		artifactPath: os.Getenv("TOOL_REGISTRY_ARTIFACT_FILE"), artifactHash: os.Getenv("TOOL_REGISTRY_ARTIFACT_HASH"), workerID: os.Getenv("TOOL_WORKER_ID"), healthAddress: env("HEALTH_ADDRESS", "127.0.0.1:8090"),
		executionIDKeyFile: os.Getenv("EXECUTION_ID_KEY_FILE"), executionLeasePepperFile: os.Getenv("EXECUTION_LEASE_PEPPER_FILE"), runtimeIDKeyFile: os.Getenv("RUNTIME_ID_KEY_FILE"), runtimeNonceKeyFile: os.Getenv("RUNTIME_NONCE_KEY_FILE"), runtimeTokenPepperFile: os.Getenv("RUNTIME_TOKEN_PEPPER_FILE"), provisionDerivationKeyFile: os.Getenv("RUNTIME_PROVISION_DERIVATION_KEY_FILE"), machineIdentityKeyFile: os.Getenv("RUNTIME_MACHINE_IDENTITY_KEY_FILE"),
		capabilityPrivateKeyFile: os.Getenv("RUNTIME_CAPABILITY_PRIVATE_KEY_FILE"), capabilityKeyID: os.Getenv("RUNTIME_CAPABILITY_KEY_ID"), capabilityIssuer: env("RUNTIME_CAPABILITY_ISSUER", "event-service"), capabilityAudience: env("RUNTIME_CAPABILITY_AUDIENCE", "runtime-manager"),
		hostEndpointsFile: os.Getenv("RUNTIME_HOST_ENDPOINTS_FILE"), hostCAFile: os.Getenv("RUNTIME_HOST_ROOT_CA_FILE"), hostCertFile: os.Getenv("RUNTIME_HOST_CLIENT_CERT_FILE"), hostKeyFile: os.Getenv("RUNTIME_HOST_CLIENT_KEY_FILE"), hostTLSName: os.Getenv("RUNTIME_HOST_TLS_SERVER_NAME"),
		s3Region: os.Getenv("S3_REGION"), s3Endpoint: os.Getenv("S3_ENDPOINT"), payloadBucket: os.Getenv("S3_PAYLOAD_BUCKET"), payloadPrefix: env("S3_PAYLOAD_PREFIX", "restricted"), s3KMSKeyID: os.Getenv("S3_KMS_KEY_ID"), s3Encryption: types.ServerSideEncryption(env("S3_SERVER_SIDE_ENCRYPTION", string(types.ServerSideEncryptionAes256))),
		vaultAddress: os.Getenv("VAULT_ADDR"), vaultNamespace: os.Getenv("VAULT_NAMESPACE"), vaultMount: env("VAULT_KV_MOUNT", "secret"), vaultTokenFile: os.Getenv("VAULT_TOKEN_FILE"), vaultCAFile: os.Getenv("VAULT_CACERT"), vaultCertFile: os.Getenv("VAULT_CLIENT_CERT_FILE"), vaultKeyFile: os.Getenv("VAULT_CLIENT_KEY_FILE"), vaultTLSName: os.Getenv("VAULT_TLS_SERVER_NAME"), vaultKeyPrefix: env("VAULT_PAYLOAD_KEY_PREFIX", "lites/payload-keys"),
		environment: os.Getenv("LITES_ENVIRONMENT"), version: os.Getenv("LITES_VERSION"), region: os.Getenv("LITES_REGION"), otlpEndpoint: os.Getenv("OTLP_GRPC_ENDPOINT"), otlpCAFile: os.Getenv("OTLP_ROOT_CA_FILE"), otlpCertFile: os.Getenv("OTLP_CLIENT_CERT_FILE"), otlpKeyFile: os.Getenv("OTLP_CLIENT_KEY_FILE"), otlpTLSName: os.Getenv("OTLP_TLS_SERVER_NAME"), otlpTokenFile: os.Getenv("OTLP_BEARER_TOKEN_FILE"), allowInsecure: allow,
	}
	if value.databaseURL, err = loadDatabaseURL(value.databaseURL, value.databaseURLFile); err != nil {
		return config{}, err
	}
	if value.runtimeDatabaseURL, err = loadDatabaseURL(value.runtimeDatabaseURL, value.runtimeDatabaseURLFile); err != nil {
		return config{}, err
	}
	if value.s3PathStyle, err = optionalBool("S3_PATH_STYLE", false); err != nil {
		return config{}, err
	}
	ints := []struct {
		name     string
		target   *int
		fallback int
	}{
		{"NATS_STREAM_REPLICAS", &value.streamReplicas, 3}, {"TOOL_WORKER_CONCURRENCY", &value.concurrency, 32}, {"NATS_MAX_DELIVER", &value.maxDeliver, 20}, {"NATS_MAX_ACK_PENDING", &value.maxAckPending, 64}, {"TOOL_MAX_COMMAND_BYTES", &value.maximumCommand, 1 << 20}, {"TOOL_MAX_INPUT_BYTES", &value.maximumInput, 16 << 20}, {"TOOL_MAX_RESULT_BYTES", &value.maximumResult, 16 << 20},
	}
	for _, item := range ints {
		if *item.target, err = optionalInt(item.name, item.fallback); err != nil {
			return config{}, err
		}
	}
	durations := []struct {
		name     string
		target   *time.Duration
		fallback time.Duration
	}{
		{"NATS_ACK_WAIT", &value.ackWait, 70 * time.Minute}, {"TOOL_ACK_TIMEOUT", &value.ackTimeout, 65 * time.Minute}, {"NATS_PULL_EXPIRES", &value.pullExpires, 5 * time.Second}, {"NATS_MESSAGE_HEARTBEAT", &value.messageHeartbeat, 10 * time.Second}, {"NATS_BUSY_DELAY", &value.busyDelay, 5 * time.Second}, {"NATS_RETRY_DELAY", &value.retryDelay, 10 * time.Second}, {"EXECUTION_LEASE_TTL", &value.executionLeaseTTL, 65 * time.Minute}, {"TOOL_HEARTBEAT_INTERVAL", &value.toolHeartbeat, 10 * time.Second}, {"SANDBOX_CLEANUP_TIMEOUT", &value.cleanupTimeout, 30 * time.Second}, {"RUNTIME_CAPABILITY_TTL", &value.capabilityTTL, 5 * time.Minute},
	}
	for _, item := range durations {
		if *item.target, err = optionalDuration(item.name, item.fallback); err != nil {
			return config{}, err
		}
	}
	int64s := []struct {
		name     string
		target   *int64
		fallback int64
	}{
		{"SCRATCH_BANDWIDTH_SIZE", &value.scratchBandwidthSize, 64 << 20}, {"SCRATCH_BANDWIDTH_BURST", &value.scratchBandwidthBurst, 128 << 20}, {"SCRATCH_OPERATIONS_SIZE", &value.scratchOperationsSize, 2000}, {"SCRATCH_OPERATIONS_BURST", &value.scratchOperationsBurst, 4000}, {"SCRATCH_REFILL_MILLIS", &value.scratchRefillMillis, 1000},
	}
	for _, item := range int64s {
		if *item.target, err = optionalInt64(item.name, item.fallback); err != nil {
			return config{}, err
		}
	}
	if value.traceRatio, err = optionalFloat("TRACE_SAMPLE_RATIO", .1); err != nil {
		return config{}, err
	}
	return value, value.validate()
}

func (value config) validate() error {
	required := []string{value.databaseURL, value.runtimeDatabaseURL, value.epochURL, value.artifactPath, value.artifactHash, value.workerID, value.executionIDKeyFile, value.executionLeasePepperFile, value.runtimeIDKeyFile, value.runtimeNonceKeyFile, value.runtimeTokenPepperFile, value.provisionDerivationKeyFile, value.machineIdentityKeyFile, value.capabilityPrivateKeyFile, value.capabilityKeyID, value.capabilityIssuer, value.capabilityAudience, value.hostEndpointsFile, value.s3Region, value.payloadBucket, value.vaultAddress, value.vaultMount, value.vaultKeyPrefix, value.environment, value.version, value.region}
	for _, item := range required {
		if item == "" {
			return errors.New("required tool worker configuration is missing")
		}
	}
	if len(value.natsURLs) == 0 || value.streamReplicas < 1 || value.concurrency < 1 || value.concurrency > 1024 || value.maxDeliver < 5 || value.maxAckPending < value.concurrency || value.maximumCommand < 1 || value.maximumCommand > 16<<20 || value.maximumInput < 1 || value.maximumInput > 16<<20 || value.maximumResult < 1024 || value.maximumResult > 16<<20 || value.ackWait <= value.ackTimeout || value.ackTimeout <= value.messageHeartbeat || value.executionLeaseTTL < value.ackTimeout || value.toolHeartbeat <= 0 || value.toolHeartbeat >= value.executionLeaseTTL || value.cleanupTimeout < time.Second || value.cleanupTimeout > time.Minute || value.capabilityTTL < time.Second || value.capabilityTTL > 5*time.Minute || value.traceRatio < 0 || value.traceRatio > 1 || value.scratchRefillMillis < 1 || value.scratchBandwidthSize < 1 || value.scratchBandwidthBurst < value.scratchBandwidthSize || value.scratchOperationsSize < 1 || value.scratchOperationsBurst < value.scratchOperationsSize || (value.natsCertFile == "") != (value.natsKeyFile == "") || (value.epochCertFile == "") != (value.epochKeyFile == "") || (value.hostCertFile == "") != (value.hostKeyFile == "") || (value.vaultCertFile == "") != (value.vaultKeyFile == "") || (value.otlpCertFile == "") != (value.otlpKeyFile == "") {
		return errors.New("tool worker configuration is invalid")
	}
	if value.s3Encryption == types.ServerSideEncryptionAwsKms && value.s3KMSKeyID == "" || value.s3Encryption != types.ServerSideEncryptionAwsKms && value.s3Encryption != types.ServerSideEncryptionAes256 {
		return errors.New("tool worker S3 encryption is invalid")
	}
	if err := secureEndpoint(value.epochURL, value.allowInsecure); err != nil {
		return errors.New("store epoch endpoint is invalid")
	}
	if !value.allowInsecure && (value.databaseURLFile == "" || value.runtimeDatabaseURLFile == "" || value.streamReplicas < 3 || value.natsCAFile == "" || value.natsCertFile == "" || value.epochCAFile == "" || value.epochTokenFile == "" && value.epochCertFile == "" || value.hostCAFile == "" || value.hostCertFile == "" || value.vaultCAFile == "" || value.vaultTokenFile == "" && value.vaultCertFile == "" || value.otlpEndpoint == "" || value.otlpCAFile == "" || value.otlpTokenFile == "" && value.otlpCertFile == "") {
		return errors.New("production tool worker requires file credentials, mTLS, authenticated telemetry, and three JetStream replicas")
	}
	return nil
}

func loadDatabaseURL(direct, file string) (string, error) {
	if file != "" {
		if direct != "" {
			return "", errors.New("database URL sources are mutually exclusive")
		}
		return readSecret(file, 8192)
	}
	if direct == "" {
		return "", errors.New("database URL is empty")
	}
	return direct, nil
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
		return "", errors.New("secret is invalid")
	}
	return value, nil
}
func readBase64(path string, size int) ([]byte, error) {
	value, err := readSecret(path, 8192)
	if err != nil {
		return nil, err
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(decoded) != size {
		return nil, errors.New("base64 secret length is invalid")
	}
	return decoded, nil
}
func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func split(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
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
func optionalInt64(name string, fallback int64) (int64, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	return strconv.ParseInt(value, 10, 64)
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
func secureEndpoint(value string, allow bool) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return errors.New("invalid endpoint")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	ip := net.ParseIP(parsed.Hostname())
	if allow && parsed.Scheme == "http" && (strings.EqualFold(parsed.Hostname(), "localhost") || ip != nil && ip.IsLoopback()) {
		return nil
	}
	return errors.New("insecure endpoint")
}
