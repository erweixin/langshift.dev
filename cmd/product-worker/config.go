package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type config struct {
	databaseURL, databaseURLFile, healthAddress, secretBundleFile, inboxPepperFile  string
	contentReleaseDirectory, routeBehaviorEnvironment                               string
	epochURL, epochTokenFile, epochCAFile, epochCertFile, epochKeyFile              string
	natsURLs                                                                        []string
	natsName, natsCredentialsFile, natsCAFile, natsCertFile, natsKeyFile            string
	streamName, consumerName                                                        string
	s3Region, s3Endpoint, payloadBucket, payloadPrefix, s3KMSKeyID                  string
	s3PathStyle                                                                     bool
	s3Encryption                                                                    types.ServerSideEncryption
	vaultAddress, vaultNamespace, vaultMount, vaultTokenFile, vaultCAFile           string
	vaultCertFile, vaultKeyFile, vaultTLSName, vaultKeyPrefix                       string
	environment, version, region                                                    string
	otlpEndpoint, otlpCAFile, otlpCertFile, otlpKeyFile, otlpTLSName, otlpTokenFile string
	reminderDeliveryURL, reminderReadinessURL, reminderTokenFile, reminderCAFile    string
	reminderCertFile, reminderKeyFile, reminderTLSName                              string
	allowInsecure                                                                   bool
	streamReplicas, concurrency, maxDeliver, runMaxSteps, runMaxAttempts            int
	runMaxCostMicrounits                                                            int64
	ackWait, pullExpires, heartbeat, busyDelay, retryDelay, ackTimeout              time.Duration
	inboxLeaseTTL, runTimeout, reconcileInterval                                    time.Duration
	reminderDeliveryTimeout, reminderReconcileInterval                              time.Duration
	portfolioReconcileInterval, portfolioRetention                                  time.Duration
	idempotencyTTL                                                                  time.Duration
	reconcileTenantBatch, reconcileRouteBatch                                       int
	reminderTenantBatch, reminderScheduleBatch                                      int
	portfolioTenantBatch, portfolioExportBatch                                      int
	traceRatio                                                                      float64
}

type workerSecrets struct {
	IDKey, IdempotencyPepper, RequestDigestPepper, CursorKey []byte
}

func loadConfig() (config, error) {
	if err := validateTypedEnvironment(); err != nil {
		return config{}, err
	}
	allow, err := boolEnv("ALLOW_INSECURE_DEVELOPMENT", false)
	if err != nil {
		return config{}, err
	}
	pathStyle, err := boolEnv("S3_PATH_STYLE", false)
	if err != nil {
		return config{}, err
	}
	value := config{
		databaseURL: os.Getenv("DATABASE_URL"), databaseURLFile: os.Getenv("DATABASE_URL_FILE"), healthAddress: env("HEALTH_ADDRESS", ":8095"), secretBundleFile: os.Getenv("PRODUCT_SECRET_BUNDLE_FILE"), inboxPepperFile: os.Getenv("PRODUCT_INBOX_LEASE_PEPPER_FILE"),
		contentReleaseDirectory: env("PRODUCT_CONTENT_RELEASE_DIR", "/app/product-content/releases/1.0.0"), routeBehaviorEnvironment: env("ROUTE_BEHAVIOR_ENVIRONMENT", "production"),
		epochURL: os.Getenv("STORE_EPOCH_URL"), epochTokenFile: os.Getenv("STORE_EPOCH_TOKEN_FILE"), epochCAFile: os.Getenv("STORE_EPOCH_ROOT_CA_FILE"), epochCertFile: os.Getenv("STORE_EPOCH_CLIENT_CERT_FILE"), epochKeyFile: os.Getenv("STORE_EPOCH_CLIENT_KEY_FILE"),
		natsURLs: split(os.Getenv("NATS_URLS")), natsName: env("NATS_CLIENT_NAME", "lites-product-worker"), natsCredentialsFile: os.Getenv("NATS_CREDENTIALS_FILE"), natsCAFile: os.Getenv("NATS_ROOT_CA_FILE"), natsCertFile: os.Getenv("NATS_CLIENT_CERT_FILE"), natsKeyFile: os.Getenv("NATS_CLIENT_KEY_FILE"), streamName: env("NATS_COMMAND_STREAM", "LITES_COMMANDS"), consumerName: env("NATS_CONSUMER_NAME", "PRODUCT_ROUTE_WORKER_V1"),
		s3Region: os.Getenv("S3_REGION"), s3Endpoint: os.Getenv("S3_ENDPOINT"), s3PathStyle: pathStyle, payloadBucket: os.Getenv("S3_PAYLOAD_BUCKET"), payloadPrefix: env("S3_PAYLOAD_PREFIX", "restricted"), s3Encryption: types.ServerSideEncryption(env("S3_SERVER_SIDE_ENCRYPTION", string(types.ServerSideEncryptionAes256))), s3KMSKeyID: os.Getenv("S3_KMS_KEY_ID"),
		vaultAddress: os.Getenv("VAULT_ADDR"), vaultNamespace: os.Getenv("VAULT_NAMESPACE"), vaultMount: env("VAULT_PAYLOAD_KEY_MOUNT", "secret"), vaultTokenFile: os.Getenv("VAULT_TOKEN_FILE"), vaultCAFile: os.Getenv("VAULT_CACERT"), vaultCertFile: os.Getenv("VAULT_CLIENT_CERT_FILE"), vaultKeyFile: os.Getenv("VAULT_CLIENT_KEY_FILE"), vaultTLSName: os.Getenv("VAULT_TLS_SERVER_NAME"), vaultKeyPrefix: env("VAULT_PAYLOAD_KEY_PREFIX", "lites/payload-keys"),
		environment: os.Getenv("LITES_ENVIRONMENT"), version: os.Getenv("LITES_VERSION"), region: os.Getenv("LITES_REGION"), otlpEndpoint: os.Getenv("OTLP_GRPC_ENDPOINT"), otlpCAFile: os.Getenv("OTLP_ROOT_CA_FILE"), otlpCertFile: os.Getenv("OTLP_CLIENT_CERT_FILE"), otlpKeyFile: os.Getenv("OTLP_CLIENT_KEY_FILE"), otlpTLSName: os.Getenv("OTLP_TLS_SERVER_NAME"), otlpTokenFile: os.Getenv("OTLP_BEARER_TOKEN_FILE"), allowInsecure: allow,
		reminderDeliveryURL: os.Getenv("REMINDER_DELIVERY_URL"), reminderReadinessURL: os.Getenv("REMINDER_DELIVERY_READINESS_URL"), reminderTokenFile: os.Getenv("REMINDER_DELIVERY_TOKEN_FILE"), reminderCAFile: os.Getenv("REMINDER_DELIVERY_ROOT_CA_FILE"), reminderCertFile: os.Getenv("REMINDER_DELIVERY_CLIENT_CERT_FILE"), reminderKeyFile: os.Getenv("REMINDER_DELIVERY_CLIENT_KEY_FILE"), reminderTLSName: os.Getenv("REMINDER_DELIVERY_TLS_SERVER_NAME"),
		streamReplicas: intEnv("NATS_STREAM_REPLICAS", 3), concurrency: intEnv("WORKER_CONCURRENCY", 16), maxDeliver: intEnv("NATS_MAX_DELIVER", 20), runMaxSteps: intEnv("ROUTE_RUN_MAX_STEPS", 24), runMaxAttempts: intEnv("ROUTE_RUN_MAX_ATTEMPTS", 5), runMaxCostMicrounits: int64Env("ROUTE_RUN_MAX_COST_MICROUNITS", 200000),
		ackWait: durationEnv("NATS_ACK_WAIT", 5*time.Minute), pullExpires: durationEnv("NATS_PULL_EXPIRES", 30*time.Second), heartbeat: durationEnv("NATS_MESSAGE_HEARTBEAT", 20*time.Second), busyDelay: durationEnv("NATS_BUSY_DELAY", 15*time.Second), retryDelay: durationEnv("NATS_RETRY_DELAY", 30*time.Second), ackTimeout: durationEnv("NATS_ACK_TIMEOUT", 4*time.Minute), inboxLeaseTTL: durationEnv("PRODUCT_INBOX_LEASE_TTL", 5*time.Minute), runTimeout: durationEnv("ROUTE_RUN_TIMEOUT", 30*time.Minute), reconcileInterval: durationEnv("ROUTE_RECONCILE_INTERVAL", 2*time.Second), idempotencyTTL: durationEnv("IDEMPOTENCY_TTL", 24*time.Hour), reconcileTenantBatch: intEnv("ROUTE_RECONCILE_TENANT_BATCH", 500), reconcileRouteBatch: intEnv("ROUTE_RECONCILE_ROUTE_BATCH", 100), traceRatio: floatEnv("TRACE_SAMPLE_RATIO", .1),
		reminderDeliveryTimeout: durationEnv("REMINDER_DELIVERY_TIMEOUT", 20*time.Second), reminderReconcileInterval: durationEnv("REMINDER_RECONCILE_INTERVAL", 15*time.Second), reminderTenantBatch: intEnv("REMINDER_RECONCILE_TENANT_BATCH", 500), reminderScheduleBatch: intEnv("REMINDER_RECONCILE_SCHEDULE_BATCH", 100),
		portfolioReconcileInterval: durationEnv("PORTFOLIO_RECONCILE_INTERVAL", 2*time.Second), portfolioRetention: durationEnv("PORTFOLIO_EXPORT_RETENTION", 30*24*time.Hour), portfolioTenantBatch: intEnv("PORTFOLIO_RECONCILE_TENANT_BATCH", 500), portfolioExportBatch: intEnv("PORTFOLIO_RECONCILE_EXPORT_BATCH", 100),
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
	return value, value.validate()
}

func (value config) validate() error {
	required := []string{value.databaseURL, value.healthAddress, value.secretBundleFile, value.inboxPepperFile, value.contentReleaseDirectory, value.epochURL, value.natsName, value.streamName, value.consumerName, value.s3Region, value.payloadBucket, value.vaultAddress, value.vaultMount, value.vaultKeyPrefix, value.environment, value.version, value.region, value.reminderDeliveryURL, value.reminderReadinessURL, value.reminderTokenFile}
	for _, item := range required {
		if item == "" {
			return errors.New("required product worker configuration is missing")
		}
	}
	if len(value.natsURLs) == 0 || value.streamReplicas < 1 || value.concurrency < 1 || value.concurrency > 128 || value.maxDeliver < 1 || value.runMaxSteps < 1 || value.runMaxAttempts < 1 || value.runMaxCostMicrounits < 1 || value.ackWait <= 0 || value.pullExpires <= 0 || value.heartbeat <= 0 || value.ackTimeout <= value.heartbeat || value.inboxLeaseTTL <= value.ackTimeout || value.runTimeout <= 0 || value.reconcileInterval <= 0 || value.reminderDeliveryTimeout <= 0 || value.reminderDeliveryTimeout >= value.inboxLeaseTTL || value.reminderReconcileInterval <= 0 || value.portfolioReconcileInterval <= 0 || value.portfolioRetention < 24*time.Hour || value.portfolioRetention > 365*24*time.Hour || value.idempotencyTTL < time.Hour || value.idempotencyTTL > 7*24*time.Hour || value.routeBehaviorEnvironment != "staging" && value.routeBehaviorEnvironment != "production" || value.reconcileTenantBatch < 1 || value.reconcileTenantBatch > 5000 || value.reconcileRouteBatch < 1 || value.reconcileRouteBatch > 500 || value.reminderTenantBatch < 1 || value.reminderTenantBatch > 5000 || value.reminderScheduleBatch < 1 || value.reminderScheduleBatch > 500 || value.portfolioTenantBatch < 1 || value.portfolioTenantBatch > 5000 || value.portfolioExportBatch < 1 || value.portfolioExportBatch > 500 || value.traceRatio < 0 || value.traceRatio > 1 || (value.epochCertFile == "") != (value.epochKeyFile == "") || (value.natsCertFile == "") != (value.natsKeyFile == "") || (value.vaultCertFile == "") != (value.vaultKeyFile == "") || (value.otlpCertFile == "") != (value.otlpKeyFile == "") || (value.reminderCertFile == "") != (value.reminderKeyFile == "") || !validReminderEndpoint(value.reminderDeliveryURL, value.allowInsecure) || !validReminderEndpoint(value.reminderReadinessURL, value.allowInsecure) {
		return errors.New("product worker configuration is invalid")
	}
	if !value.allowInsecure && (value.databaseURLFile == "" || value.streamReplicas < 3 || value.natsCAFile == "" || value.natsCredentialsFile == "" && value.natsCertFile == "" || value.epochCAFile == "" || value.epochTokenFile == "" && value.epochCertFile == "" || value.vaultCAFile == "" || value.vaultTokenFile == "" && value.vaultCertFile == "" || value.otlpEndpoint == "" || value.otlpTokenFile == "" && value.otlpCertFile == "" || value.reminderCAFile == "") {
		return errors.New("production product worker requires file-backed authenticated TLS dependencies")
	}
	if value.s3Encryption == types.ServerSideEncryptionAwsKms && value.s3KMSKeyID == "" || value.s3Encryption != types.ServerSideEncryptionAwsKms && value.s3Encryption != types.ServerSideEncryptionAes256 {
		return errors.New("product payload S3 encryption is invalid")
	}
	return nil
}

func loadWorkerSecrets(path string) (workerSecrets, error) {
	encoded, err := os.ReadFile(path)
	if err != nil || len(encoded) > 16<<10 {
		return workerSecrets{}, errors.New("product secret bundle is unavailable")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var full struct {
		Version             string `json:"version"`
		IDKey               string `json:"id_key"`
		IdempotencyPepper   string `json:"idempotency_pepper"`
		RequestDigestPepper string `json:"request_digest_pepper"`
		CursorKey           string `json:"cursor_key"`
	}
	if decoder.Decode(&full) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || full.Version != "1.0.0" {
		return workerSecrets{}, errors.New("product secret bundle is invalid")
	}
	values := []string{full.IDKey, full.IdempotencyPepper, full.RequestDigestPepper, full.CursorKey}
	decoded := make([][]byte, len(values))
	seen := map[string]struct{}{}
	for index, value := range values {
		decoded[index], err = base64.StdEncoding.DecodeString(value)
		if err != nil || len(decoded[index]) != 32 {
			return workerSecrets{}, errors.New("product secret bundle material is invalid")
		}
		fingerprint := base64.StdEncoding.EncodeToString(decoded[index])
		if _, duplicate := seen[fingerprint]; duplicate {
			return workerSecrets{}, errors.New("product secret bundle reuses purpose-separated material")
		}
		seen[fingerprint] = struct{}{}
	}
	return workerSecrets{IDKey: decoded[0], IdempotencyPepper: decoded[1], RequestDigestPepper: decoded[2], CursorKey: decoded[3]}, nil
}

func validateTypedEnvironment() error {
	for _, name := range []string{"NATS_STREAM_REPLICAS", "WORKER_CONCURRENCY", "NATS_MAX_DELIVER", "ROUTE_RUN_MAX_STEPS", "ROUTE_RUN_MAX_ATTEMPTS", "ROUTE_RECONCILE_TENANT_BATCH", "ROUTE_RECONCILE_ROUTE_BATCH", "REMINDER_RECONCILE_TENANT_BATCH", "REMINDER_RECONCILE_SCHEDULE_BATCH", "PORTFOLIO_RECONCILE_TENANT_BATCH", "PORTFOLIO_RECONCILE_EXPORT_BATCH"} {
		if value, ok := os.LookupEnv(name); ok {
			if _, err := strconv.Atoi(value); err != nil {
				return errors.New(name + " must be an integer")
			}
		}
	}
	if value, ok := os.LookupEnv("ROUTE_RUN_MAX_COST_MICROUNITS"); ok {
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return errors.New("ROUTE_RUN_MAX_COST_MICROUNITS must be an integer")
		}
	}
	for _, name := range []string{"NATS_ACK_WAIT", "NATS_PULL_EXPIRES", "NATS_MESSAGE_HEARTBEAT", "NATS_BUSY_DELAY", "NATS_RETRY_DELAY", "NATS_ACK_TIMEOUT", "PRODUCT_INBOX_LEASE_TTL", "ROUTE_RUN_TIMEOUT", "ROUTE_RECONCILE_INTERVAL", "REMINDER_DELIVERY_TIMEOUT", "REMINDER_RECONCILE_INTERVAL", "PORTFOLIO_RECONCILE_INTERVAL", "PORTFOLIO_EXPORT_RETENTION", "IDEMPOTENCY_TTL"} {
		if value, ok := os.LookupEnv(name); ok {
			if _, err := time.ParseDuration(value); err != nil {
				return errors.New(name + " must be a duration")
			}
		}
	}
	if value, ok := os.LookupEnv("TRACE_SAMPLE_RATIO"); ok {
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return errors.New("TRACE_SAMPLE_RATIO must be a number")
		}
	}
	return nil
}

func validReminderEndpoint(raw string, allowInsecure bool) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return parsed.Scheme == "https" || allowInsecure && parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1")
}

func readBase64(path string) ([]byte, error) {
	value, err := readSecret(path, 4096)
	if err != nil {
		return nil, err
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return nil, errors.New("invalid 32-byte base64 secret")
	}
	return decoded, nil
}
func readSecret(path string, limit int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	v := strings.TrimSpace(string(b))
	if err != nil || int64(len(b)) > limit || v == "" || strings.ContainsAny(v, "\x00\r\n") {
		return "", errors.New("invalid secret")
	}
	return v, nil
}
func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
func split(v string) []string {
	var out []string
	for _, x := range strings.Split(v, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}
func boolEnv(n string, d bool) (bool, error) {
	v, ok := os.LookupEnv(n)
	if !ok {
		return d, nil
	}
	return strconv.ParseBool(v)
}
func intEnv(n string, d int) int {
	v, err := strconv.Atoi(os.Getenv(n))
	if err == nil {
		return v
	}
	return d
}
func int64Env(n string, d int64) int64 {
	v, err := strconv.ParseInt(os.Getenv(n), 10, 64)
	if err == nil {
		return v
	}
	return d
}
func floatEnv(n string, d float64) float64 {
	v, err := strconv.ParseFloat(os.Getenv(n), 64)
	if err == nil {
		return v
	}
	return d
}
func durationEnv(n string, d time.Duration) time.Duration {
	v, err := time.ParseDuration(os.Getenv(n))
	if err == nil {
		return v
	}
	return d
}
