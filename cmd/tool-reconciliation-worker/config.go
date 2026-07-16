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
	databaseURL, databaseURLFile                                                                                                     string
	epochURL, epochTokenFile, epochCAFile, epochCertFile, epochKeyFile, epochTLSName                                                 string
	natsURLs                                                                                                                         []string
	natsName, natsCredentialsFile, natsCAFile, natsCertFile, natsKeyFile, streamName, consumerName                                   string
	artifactPath, artifactHash, adapterFile, workerID, executionIDKeyFile, executionLeasePepperFile, healthAddress                   string
	s3Region, s3Endpoint, payloadBucket, payloadPrefix, s3KMSKeyID                                                                   string
	s3PathStyle                                                                                                                      bool
	s3Encryption                                                                                                                     types.ServerSideEncryption
	vaultAddress, vaultNamespace, vaultMount, vaultTokenFile, vaultCAFile, vaultCertFile, vaultKeyFile, vaultTLSName, vaultKeyPrefix string
	environment, version, region, otlpEndpoint, otlpCAFile, otlpCertFile, otlpKeyFile, otlpTLSName, otlpTokenFile                    string
	allowInsecure                                                                                                                    bool
	streamReplicas, concurrency, maxDeliver, maxAckPending, maximumCommand, maximumEvidence, maximumRounds                           int
	shardCount, tenantPage, effectPage                                                                                               int
	ackWait, ackTimeout, pullExpires, messageHeartbeat, busyDelay, natsRetryDelay, executionLeaseTTL, reconciliationHeartbeat        time.Duration
	reconciliationRetryDelay, maximumReconciliationRetryDelay, lookupTimeout                                                         time.Duration
	sweeperInterval, sweeperCycleTimeout, sweeperUnlockTimeout, abandonedReconcileDelay                                              time.Duration
	traceRatio                                                                                                                       float64
}

func loadConfig() (config, error) {
	allow, err := optionalBool("ALLOW_INSECURE_DEVELOPMENT", false)
	if err != nil {
		return config{}, err
	}
	value := config{
		databaseURL: os.Getenv("DATABASE_URL"), databaseURLFile: os.Getenv("DATABASE_URL_FILE"),
		epochURL: os.Getenv("STORE_EPOCH_URL"), epochTokenFile: os.Getenv("STORE_EPOCH_TOKEN_FILE"), epochCAFile: os.Getenv("STORE_EPOCH_ROOT_CA_FILE"), epochCertFile: os.Getenv("STORE_EPOCH_CLIENT_CERT_FILE"), epochKeyFile: os.Getenv("STORE_EPOCH_CLIENT_KEY_FILE"), epochTLSName: os.Getenv("STORE_EPOCH_TLS_SERVER_NAME"),
		natsURLs: split(os.Getenv("NATS_URLS")), natsName: env("NATS_CLIENT_NAME", "lites-tool-reconciliation-worker"), natsCredentialsFile: os.Getenv("NATS_CREDENTIALS_FILE"), natsCAFile: os.Getenv("NATS_ROOT_CA_FILE"), natsCertFile: os.Getenv("NATS_CLIENT_CERT_FILE"), natsKeyFile: os.Getenv("NATS_CLIENT_KEY_FILE"), streamName: env("NATS_COMMAND_STREAM", "LITES_COMMANDS"), consumerName: env("NATS_CONSUMER_NAME", "tool-reconciliation-worker-v1"),
		artifactPath: os.Getenv("TOOL_REGISTRY_ARTIFACT_FILE"), artifactHash: os.Getenv("TOOL_REGISTRY_ARTIFACT_HASH"), adapterFile: os.Getenv("TOOL_RECONCILIATION_ADAPTERS_FILE"), workerID: env("TOOL_RECONCILIATION_WORKER_ID", os.Getenv("HOSTNAME")), executionIDKeyFile: os.Getenv("EXECUTION_ID_KEY_FILE"), executionLeasePepperFile: os.Getenv("EXECUTION_LEASE_PEPPER_FILE"), healthAddress: env("HEALTH_ADDRESS", "127.0.0.1:8091"),
		s3Region: os.Getenv("S3_REGION"), s3Endpoint: os.Getenv("S3_ENDPOINT"), payloadBucket: os.Getenv("S3_PAYLOAD_BUCKET"), payloadPrefix: env("S3_PAYLOAD_PREFIX", "restricted"), s3KMSKeyID: os.Getenv("S3_KMS_KEY_ID"), s3Encryption: types.ServerSideEncryption(env("S3_SERVER_SIDE_ENCRYPTION", string(types.ServerSideEncryptionAes256))),
		vaultAddress: os.Getenv("VAULT_ADDR"), vaultNamespace: os.Getenv("VAULT_NAMESPACE"), vaultMount: env("VAULT_KV_MOUNT", "secret"), vaultTokenFile: os.Getenv("VAULT_TOKEN_FILE"), vaultCAFile: os.Getenv("VAULT_CACERT"), vaultCertFile: os.Getenv("VAULT_CLIENT_CERT_FILE"), vaultKeyFile: os.Getenv("VAULT_CLIENT_KEY_FILE"), vaultTLSName: os.Getenv("VAULT_TLS_SERVER_NAME"), vaultKeyPrefix: env("VAULT_PAYLOAD_KEY_PREFIX", "lites/payload-keys"),
		environment: os.Getenv("LITES_ENVIRONMENT"), version: os.Getenv("LITES_VERSION"), region: os.Getenv("LITES_REGION"), otlpEndpoint: os.Getenv("OTLP_GRPC_ENDPOINT"), otlpCAFile: os.Getenv("OTLP_ROOT_CA_FILE"), otlpCertFile: os.Getenv("OTLP_CLIENT_CERT_FILE"), otlpKeyFile: os.Getenv("OTLP_CLIENT_KEY_FILE"), otlpTLSName: os.Getenv("OTLP_TLS_SERVER_NAME"), otlpTokenFile: os.Getenv("OTLP_BEARER_TOKEN_FILE"), allowInsecure: allow,
	}
	if value.databaseURL, err = loadDatabaseURL(value.databaseURL, value.databaseURLFile); err != nil {
		return config{}, err
	}
	if value.s3PathStyle, err = optionalBool("S3_PATH_STYLE", false); err != nil {
		return config{}, err
	}
	ints := []struct {
		name     string
		target   *int
		fallback int
	}{{"NATS_STREAM_REPLICAS", &value.streamReplicas, 3}, {"TOOL_RECONCILIATION_CONCURRENCY", &value.concurrency, 16}, {"NATS_MAX_DELIVER", &value.maxDeliver, 20}, {"NATS_MAX_ACK_PENDING", &value.maxAckPending, 32}, {"TOOL_RECONCILIATION_MAX_COMMAND_BYTES", &value.maximumCommand, 1 << 20}, {"TOOL_RECONCILIATION_MAX_EVIDENCE_BYTES", &value.maximumEvidence, 4 << 20}, {"TOOL_RECONCILIATION_MAX_ROUNDS", &value.maximumRounds, 8}, {"TOOL_EFFECT_SWEEPER_SHARD_COUNT", &value.shardCount, 16}, {"TOOL_EFFECT_SWEEPER_TENANT_PAGE", &value.tenantPage, 500}, {"TOOL_EFFECT_SWEEPER_EFFECT_PAGE", &value.effectPage, 250}}
	for _, item := range ints {
		if *item.target, err = optionalInt(item.name, item.fallback); err != nil {
			return config{}, err
		}
	}
	durations := []struct {
		name     string
		target   *time.Duration
		fallback time.Duration
	}{{"NATS_ACK_WAIT", &value.ackWait, 10 * time.Minute}, {"TOOL_RECONCILIATION_ACK_TIMEOUT", &value.ackTimeout, 9 * time.Minute}, {"NATS_PULL_EXPIRES", &value.pullExpires, 5 * time.Second}, {"NATS_MESSAGE_HEARTBEAT", &value.messageHeartbeat, 10 * time.Second}, {"NATS_BUSY_DELAY", &value.busyDelay, 5 * time.Second}, {"NATS_RETRY_DELAY", &value.natsRetryDelay, 10 * time.Second}, {"EXECUTION_LEASE_TTL", &value.executionLeaseTTL, 10 * time.Minute}, {"TOOL_RECONCILIATION_HEARTBEAT_INTERVAL", &value.reconciliationHeartbeat, 10 * time.Second}, {"TOOL_RECONCILIATION_RETRY_DELAY", &value.reconciliationRetryDelay, time.Minute}, {"TOOL_RECONCILIATION_MAX_RETRY_DELAY", &value.maximumReconciliationRetryDelay, 6 * time.Hour}, {"TOOL_RECONCILIATION_LOOKUP_TIMEOUT", &value.lookupTimeout, 2 * time.Minute}, {"TOOL_EFFECT_SWEEPER_INTERVAL", &value.sweeperInterval, 5 * time.Second}, {"TOOL_EFFECT_SWEEPER_CYCLE_TIMEOUT", &value.sweeperCycleTimeout, 4 * time.Minute}, {"TOOL_EFFECT_SWEEPER_UNLOCK_TIMEOUT", &value.sweeperUnlockTimeout, 3 * time.Second}, {"TOOL_EFFECT_ABANDONED_RECONCILE_DELAY", &value.abandonedReconcileDelay, time.Minute}}
	for _, item := range durations {
		if *item.target, err = optionalDuration(item.name, item.fallback); err != nil {
			return config{}, err
		}
	}
	if value.traceRatio, err = optionalFloat("TRACE_SAMPLE_RATIO", .1); err != nil {
		return config{}, err
	}
	return value, value.validate()
}

func (value config) validate() error {
	required := []string{value.databaseURL, value.epochURL, value.artifactPath, value.artifactHash, value.adapterFile, value.workerID, value.executionIDKeyFile, value.executionLeasePepperFile, value.s3Region, value.payloadBucket, value.vaultAddress, value.vaultMount, value.vaultKeyPrefix, value.healthAddress, value.environment, value.version, value.region}
	for _, item := range required {
		if item == "" {
			return errors.New("required tool reconciliation worker configuration is missing")
		}
	}
	if len(value.natsURLs) == 0 || value.streamReplicas < 1 || value.concurrency < 1 || value.concurrency > 512 || value.maxDeliver < 5 || value.maxAckPending < value.concurrency || value.maximumCommand < 1 || value.maximumCommand > 16<<20 || value.maximumEvidence < 1 || value.maximumEvidence > 16<<20 || value.maximumRounds < 1 || value.maximumRounds > 100 || value.shardCount < 1 || value.shardCount > 256 || value.tenantPage < 1 || value.tenantPage > 5000 || value.effectPage < 1 || value.effectPage > 500 || value.ackWait <= value.ackTimeout || value.ackTimeout <= value.messageHeartbeat || value.executionLeaseTTL < value.ackTimeout || value.reconciliationHeartbeat <= 0 || value.reconciliationHeartbeat >= value.executionLeaseTTL || value.reconciliationRetryDelay < time.Second || value.maximumReconciliationRetryDelay < value.reconciliationRetryDelay || value.maximumReconciliationRetryDelay > 7*24*time.Hour || value.lookupTimeout < time.Second || value.lookupTimeout > 5*time.Minute || value.sweeperInterval <= 0 || value.sweeperCycleTimeout < value.sweeperInterval || value.sweeperCycleTimeout > 15*time.Minute || value.sweeperUnlockTimeout <= 0 || value.sweeperUnlockTimeout > 10*time.Second || value.abandonedReconcileDelay <= 0 || value.abandonedReconcileDelay > 24*time.Hour || value.traceRatio < 0 || value.traceRatio > 1 || (value.natsCertFile == "") != (value.natsKeyFile == "") || (value.epochCertFile == "") != (value.epochKeyFile == "") || (value.vaultCertFile == "") != (value.vaultKeyFile == "") || (value.otlpCertFile == "") != (value.otlpKeyFile == "") {
		return errors.New("tool reconciliation worker configuration is invalid")
	}
	if value.s3Encryption == types.ServerSideEncryptionAwsKms && value.s3KMSKeyID == "" || value.s3Encryption != types.ServerSideEncryptionAwsKms && value.s3Encryption != types.ServerSideEncryptionAes256 {
		return errors.New("tool reconciliation worker S3 encryption is invalid")
	}
	if secureEndpoint(value.epochURL, value.allowInsecure) != nil {
		return errors.New("store epoch endpoint is invalid")
	}
	if !value.allowInsecure && (value.databaseURLFile == "" || value.streamReplicas < 3 || value.natsCAFile == "" || value.natsCertFile == "" || value.epochCAFile == "" || value.epochTokenFile == "" && value.epochCertFile == "" || value.vaultCAFile == "" || value.vaultTokenFile == "" && value.vaultCertFile == "" || value.otlpEndpoint == "" || value.otlpCAFile == "" || value.otlpTokenFile == "" && value.otlpCertFile == "") {
		return errors.New("production tool reconciliation worker requires file credentials, mTLS, authenticated telemetry, and three JetStream replicas")
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
