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
	databaseURL, databaseURLFile                                                      string
	epochURL, epochTokenFile, epochCAFile, epochCertFile, epochKeyFile, epochTLSName  string
	natsURLs                                                                          []string
	natsName, natsCredentialsFile, natsCAFile, natsCertFile, natsKeyFile              string
	streamName, consumerName, workerID, healthAddress                                 string
	promptPath, promptHash, routePath, routeHash, toolPath, toolHash                  string
	providerPath, providerHash                                                        string
	providerRootCAFile, privateEngineeringProviderHost                                string
	executionIDKeyFile, executionLeasePepperFile, llmIDKeyFile, llmTokenPepperFile    string
	billingIDKeyFile, agentIDKeyFile                                                  string
	s3Region, s3Endpoint, payloadBucket, payloadPrefix, s3KMSKeyID                    string
	s3PathStyle                                                                       bool
	s3Encryption                                                                      types.ServerSideEncryption
	vaultAddress, vaultNamespace, vaultMount, vaultTokenFile, vaultCAFile             string
	vaultCertFile, vaultKeyFile, vaultTLSName, vaultKeyPrefix, providerSecretPrefix   string
	byokSecretPrefix                                                                  string
	environment, version, region, otlpEndpoint, otlpCAFile, otlpCertFile              string
	otlpKeyFile, otlpTLSName, otlpTokenFile                                           string
	allowInsecure                                                                     bool
	streamReplicas, concurrency, maxDeliver, maxAckPending                            int
	maximumCommand, maximumMessageBytes, maximumMessages, maximumCalls                int
	maximumOutputTokens                                                               uint32
	ackWait, ackTimeout, pullExpires, messageHeartbeat, busyDelay, retryDelay         time.Duration
	executionLeaseTTL, agentHeartbeat, prepareTTL, completionTTL, reconciliationDelay time.Duration
	approvalTTL, bucketTTL                                                            time.Duration
	traceRatio                                                                        float64
}

func loadConfig() (config, error) {
	allow, err := optionalBool("ALLOW_INSECURE_DEVELOPMENT", false)
	if err != nil {
		return config{}, err
	}
	value := config{
		databaseURL: os.Getenv("DATABASE_URL"), databaseURLFile: os.Getenv("DATABASE_URL_FILE"),
		epochURL: os.Getenv("STORE_EPOCH_URL"), epochTokenFile: os.Getenv("STORE_EPOCH_TOKEN_FILE"), epochCAFile: os.Getenv("STORE_EPOCH_ROOT_CA_FILE"), epochCertFile: os.Getenv("STORE_EPOCH_CLIENT_CERT_FILE"), epochKeyFile: os.Getenv("STORE_EPOCH_CLIENT_KEY_FILE"), epochTLSName: os.Getenv("STORE_EPOCH_TLS_SERVER_NAME"),
		natsURLs: split(os.Getenv("NATS_URLS")), natsName: env("NATS_CLIENT_NAME", "lites-agent-worker"), natsCredentialsFile: os.Getenv("NATS_CREDENTIALS_FILE"), natsCAFile: os.Getenv("NATS_ROOT_CA_FILE"), natsCertFile: os.Getenv("NATS_CLIENT_CERT_FILE"), natsKeyFile: os.Getenv("NATS_CLIENT_KEY_FILE"), streamName: env("NATS_COMMAND_STREAM", "LITES_COMMANDS"), consumerName: env("NATS_CONSUMER_NAME", "agent-worker-v1"), workerID: env("AGENT_WORKER_ID", os.Getenv("HOSTNAME")), healthAddress: env("HEALTH_ADDRESS", "127.0.0.1:8089"),
		promptPath: os.Getenv("PROMPT_ARTIFACT_FILE"), promptHash: os.Getenv("PROMPT_ARTIFACT_HASH"), routePath: os.Getenv("MODEL_ROUTE_ARTIFACT_FILE"), routeHash: os.Getenv("MODEL_ROUTE_ARTIFACT_HASH"), toolPath: os.Getenv("TOOL_REGISTRY_ARTIFACT_FILE"), toolHash: os.Getenv("TOOL_REGISTRY_ARTIFACT_HASH"), providerPath: os.Getenv("PROVIDER_REGISTRY_FILE"), providerHash: os.Getenv("PROVIDER_REGISTRY_FILE_HASH"),
		providerRootCAFile: os.Getenv("PROVIDER_ROOT_CA_FILE"), privateEngineeringProviderHost: os.Getenv("PRIVATE_ENGINEERING_PROVIDER_HOST"),
		executionIDKeyFile: os.Getenv("EXECUTION_ID_KEY_FILE"), executionLeasePepperFile: os.Getenv("EXECUTION_LEASE_PEPPER_FILE"), llmIDKeyFile: os.Getenv("LLM_ID_KEY_FILE"), llmTokenPepperFile: os.Getenv("LLM_TOKEN_PEPPER_FILE"), billingIDKeyFile: os.Getenv("BILLING_ID_KEY_FILE"), agentIDKeyFile: os.Getenv("AGENT_ID_KEY_FILE"),
		s3Region: os.Getenv("S3_REGION"), s3Endpoint: os.Getenv("S3_ENDPOINT"), payloadBucket: os.Getenv("S3_PAYLOAD_BUCKET"), payloadPrefix: env("S3_PAYLOAD_PREFIX", "restricted"), s3KMSKeyID: os.Getenv("S3_KMS_KEY_ID"), s3Encryption: types.ServerSideEncryption(env("S3_SERVER_SIDE_ENCRYPTION", string(types.ServerSideEncryptionAes256))),
		vaultAddress: os.Getenv("VAULT_ADDR"), vaultNamespace: os.Getenv("VAULT_NAMESPACE"), vaultMount: env("VAULT_KV_MOUNT", "secret"), vaultTokenFile: os.Getenv("VAULT_TOKEN_FILE"), vaultCAFile: os.Getenv("VAULT_CACERT"), vaultCertFile: os.Getenv("VAULT_CLIENT_CERT_FILE"), vaultKeyFile: os.Getenv("VAULT_CLIENT_KEY_FILE"), vaultTLSName: os.Getenv("VAULT_TLS_SERVER_NAME"), vaultKeyPrefix: env("VAULT_PAYLOAD_KEY_PREFIX", "lites/payload-keys"), providerSecretPrefix: env("VAULT_PROVIDER_SECRET_PREFIX", "lites/providers"), byokSecretPrefix: env("VAULT_BYOK_SECRET_PREFIX", "lites/byok"),
		environment: os.Getenv("LITES_ENVIRONMENT"), version: os.Getenv("LITES_VERSION"), region: os.Getenv("LITES_REGION"), otlpEndpoint: os.Getenv("OTLP_GRPC_ENDPOINT"), otlpCAFile: os.Getenv("OTLP_ROOT_CA_FILE"), otlpCertFile: os.Getenv("OTLP_CLIENT_CERT_FILE"), otlpKeyFile: os.Getenv("OTLP_CLIENT_KEY_FILE"), otlpTLSName: os.Getenv("OTLP_TLS_SERVER_NAME"), otlpTokenFile: os.Getenv("OTLP_BEARER_TOKEN_FILE"), allowInsecure: allow,
	}
	if value.databaseURL, err = loadDatabaseURL(value.databaseURL, value.databaseURLFile); err != nil {
		return config{}, err
	}
	if value.s3PathStyle, err = optionalBool("S3_PATH_STYLE", false); err != nil {
		return config{}, err
	}
	for _, item := range []struct {
		name     string
		target   *int
		fallback int
	}{
		{"NATS_STREAM_REPLICAS", &value.streamReplicas, 3}, {"AGENT_WORKER_CONCURRENCY", &value.concurrency, 32}, {"NATS_MAX_DELIVER", &value.maxDeliver, 20}, {"NATS_MAX_ACK_PENDING", &value.maxAckPending, 64}, {"AGENT_MAX_COMMAND_BYTES", &value.maximumCommand, 1 << 20}, {"AGENT_MAX_MESSAGE_BYTES", &value.maximumMessageBytes, 4 << 20}, {"AGENT_MAX_MESSAGES", &value.maximumMessages, 4096}, {"AGENT_MAX_TOOL_CALLS", &value.maximumCalls, 16},
	} {
		if *item.target, err = optionalInt(item.name, item.fallback); err != nil {
			return config{}, err
		}
	}
	outputTokens, err := optionalInt("AGENT_MAX_OUTPUT_TOKENS", 32768)
	if err != nil || outputTokens < 1 || uint64(outputTokens) > uint64(^uint32(0)) {
		return config{}, errors.New("agent output token limit is invalid")
	}
	value.maximumOutputTokens = uint32(outputTokens)
	for _, item := range []struct {
		name     string
		target   *time.Duration
		fallback time.Duration
	}{
		{"NATS_ACK_WAIT", &value.ackWait, 25 * time.Minute}, {"AGENT_ACK_TIMEOUT", &value.ackTimeout, 20 * time.Minute}, {"NATS_PULL_EXPIRES", &value.pullExpires, 5 * time.Second}, {"NATS_MESSAGE_HEARTBEAT", &value.messageHeartbeat, 10 * time.Second}, {"NATS_BUSY_DELAY", &value.busyDelay, 5 * time.Second}, {"NATS_RETRY_DELAY", &value.retryDelay, 10 * time.Second}, {"EXECUTION_LEASE_TTL", &value.executionLeaseTTL, 22 * time.Minute}, {"AGENT_HEARTBEAT_INTERVAL", &value.agentHeartbeat, 10 * time.Second}, {"LLM_PREPARE_TTL", &value.prepareTTL, 2 * time.Minute}, {"LLM_COMPLETION_TTL", &value.completionTTL, 15 * time.Minute}, {"LLM_RECONCILIATION_DELAY", &value.reconciliationDelay, 2 * time.Minute}, {"TOOL_APPROVAL_TTL", &value.approvalTTL, 30 * time.Minute}, {"CREDIT_BUCKET_REQUIRED_TTL", &value.bucketTTL, 20 * time.Minute},
	} {
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
	required := []string{value.databaseURL, value.epochURL, value.workerID, value.promptPath, value.promptHash, value.routePath, value.routeHash, value.toolPath, value.toolHash, value.providerPath, value.providerHash, value.executionIDKeyFile, value.executionLeasePepperFile, value.llmIDKeyFile, value.llmTokenPepperFile, value.billingIDKeyFile, value.agentIDKeyFile, value.s3Region, value.payloadBucket, value.vaultAddress, value.vaultMount, value.vaultKeyPrefix, value.providerSecretPrefix, value.byokSecretPrefix, value.environment, value.version, value.region}
	for _, item := range required {
		if item == "" {
			return errors.New("required agent worker configuration is missing")
		}
	}
	if len(value.natsURLs) == 0 || value.streamReplicas < 1 || value.concurrency < 1 || value.concurrency > 1024 || value.maxDeliver < 5 || value.maxAckPending < value.concurrency || value.maximumCommand < 1024 || value.maximumCommand > 16<<20 || value.maximumMessageBytes < 1024 || value.maximumMessageBytes > 16<<20 || value.maximumMessages < 1 || value.maximumMessages > 4096 || value.maximumCalls < 1 || value.maximumCalls > 32 || value.ackWait <= value.ackTimeout || value.ackTimeout <= value.messageHeartbeat || value.executionLeaseTTL < value.ackTimeout || value.agentHeartbeat <= 0 || value.agentHeartbeat >= value.executionLeaseTTL || value.prepareTTL <= 0 || value.completionTTL <= value.prepareTTL || value.reconciliationDelay <= 0 || value.approvalTTL < time.Minute || value.approvalTTL > 24*time.Hour || value.bucketTTL < value.completionTTL || value.traceRatio < 0 || value.traceRatio > 1 || secretPrefixesOverlap(value.providerSecretPrefix, value.byokSecretPrefix) || (value.natsCertFile == "") != (value.natsKeyFile == "") || (value.epochCertFile == "") != (value.epochKeyFile == "") || (value.vaultCertFile == "") != (value.vaultKeyFile == "") || (value.otlpCertFile == "") != (value.otlpKeyFile == "") {
		return errors.New("agent worker configuration is invalid")
	}
	if value.s3Encryption == types.ServerSideEncryptionAwsKms && value.s3KMSKeyID == "" || value.s3Encryption != types.ServerSideEncryptionAwsKms && value.s3Encryption != types.ServerSideEncryptionAes256 {
		return errors.New("agent worker S3 encryption is invalid")
	}
	if secureEndpoint(value.epochURL, value.allowInsecure) != nil {
		return errors.New("store epoch endpoint is invalid")
	}
	if value.privateEngineeringProviderHost != "" && (value.environment != "engineering-test" || value.privateEngineeringProviderHost != "model-adapter.lites.test" || value.providerRootCAFile == "") {
		return errors.New("private provider access is restricted to the macOS engineering adapter")
	}
	if !value.allowInsecure && (value.databaseURLFile == "" || value.streamReplicas < 3 || value.natsCAFile == "" || value.natsCertFile == "" || value.epochCAFile == "" || value.epochTokenFile == "" && value.epochCertFile == "" || value.vaultCAFile == "" || value.vaultTokenFile == "" && value.vaultCertFile == "" || value.otlpEndpoint == "" || value.otlpCAFile == "" || value.otlpTokenFile == "" && value.otlpCertFile == "") {
		return errors.New("production agent worker requires file credentials, mTLS, authenticated telemetry, and three JetStream replicas")
	}
	return nil
}

func secretPrefixesOverlap(left, right string) bool {
	return left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
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
