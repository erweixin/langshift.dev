package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	identitymail "github.com/langshift/lites/internal/identity/mail"
)

type config struct {
	databaseURL, databaseURLFile, healthAddress                                                        string
	epochURL, epochTokenFile, epochCAFile, epochCertFile, epochKeyFile                                 string
	natsURLs                                                                                           []string
	natsCredentialsFile, natsCAFile, natsCertFile, natsKeyFile, streamName, consumerName               string
	vaultAddress, vaultNamespace, vaultMount, vaultTokenFile, vaultCAFile, vaultCertFile, vaultKeyFile string
	vaultTLSServerName, vaultKeyPrefix                                                                 string
	s3Region, s3Endpoint, payloadBucket, payloadPrefix, s3KMSKeyID                                     string
	inboxPepperFile, publicAppURL                                                                      string
	smtpAddress, smtpServerName, smtpFromAddress, smtpFromName, smtpUsername                           string
	smtpPasswordFile, smtpCAFile, smtpCertFile, smtpKeyFile                                            string
	environment, serviceVersion, region                                                                string
	otlpEndpoint, otlpCAFile, otlpCertFile, otlpKeyFile, otlpTLSName, otlpBearerTokenFile              string
	s3Encryption                                                                                       types.ServerSideEncryption
	allowInsecureDevelopment, s3PathStyle                                                              bool
	streamReplicas, concurrency                                                                        int
	smtpDialTimeout                                                                                    time.Duration
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
	smtpDialTimeout, err := optionalDuration("SMTP_DIAL_TIMEOUT", 15*time.Second)
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
		databaseURL: databaseURL, databaseURLFile: databaseURLFile, healthAddress: envString("HEALTH_ADDRESS", "127.0.0.1:8083"),
		epochURL: os.Getenv("STORE_EPOCH_URL"), epochTokenFile: os.Getenv("STORE_EPOCH_TOKEN_FILE"), epochCAFile: os.Getenv("STORE_EPOCH_ROOT_CA_FILE"), epochCertFile: os.Getenv("STORE_EPOCH_CLIENT_CERT_FILE"), epochKeyFile: os.Getenv("STORE_EPOCH_CLIENT_KEY_FILE"),
		natsURLs: splitNonempty(os.Getenv("NATS_URLS")), natsCredentialsFile: os.Getenv("NATS_CREDENTIALS_FILE"), natsCAFile: os.Getenv("NATS_ROOT_CA_FILE"), natsCertFile: os.Getenv("NATS_CLIENT_CERT_FILE"), natsKeyFile: os.Getenv("NATS_CLIENT_KEY_FILE"), streamName: envString("NATS_COMMAND_STREAM", "LITES_COMMANDS"), consumerName: envString("NATS_IDENTITY_MAIL_CONSUMER", "IDENTITY_MAIL_WORKER"),
		vaultAddress: os.Getenv("VAULT_ADDR"), vaultNamespace: os.Getenv("VAULT_NAMESPACE"), vaultMount: envString("VAULT_PAYLOAD_KEY_MOUNT", "secret"), vaultTokenFile: os.Getenv("VAULT_TOKEN_FILE"), vaultCAFile: os.Getenv("VAULT_CACERT"), vaultCertFile: os.Getenv("VAULT_CLIENT_CERT_FILE"), vaultKeyFile: os.Getenv("VAULT_CLIENT_KEY_FILE"), vaultTLSServerName: os.Getenv("VAULT_TLS_SERVER_NAME"), vaultKeyPrefix: envString("VAULT_PAYLOAD_KEY_PREFIX", "lites/payload-keys"),
		s3Region: os.Getenv("S3_REGION"), s3Endpoint: os.Getenv("S3_ENDPOINT"), s3PathStyle: s3PathStyle, payloadBucket: os.Getenv("S3_PAYLOAD_BUCKET"), payloadPrefix: envString("S3_PAYLOAD_PREFIX", "restricted"), s3Encryption: types.ServerSideEncryption(envString("S3_SERVER_SIDE_ENCRYPTION", string(types.ServerSideEncryptionAes256))), s3KMSKeyID: os.Getenv("S3_KMS_KEY_ID"),
		inboxPepperFile: os.Getenv("IDENTITY_INBOX_LEASE_PEPPER_FILE"), publicAppURL: os.Getenv("PUBLIC_APP_URL"), smtpAddress: os.Getenv("SMTP_ADDRESS"), smtpServerName: os.Getenv("SMTP_SERVER_NAME"), smtpFromAddress: os.Getenv("SMTP_FROM_ADDRESS"), smtpFromName: envString("SMTP_FROM_NAME", "Lites"), smtpUsername: os.Getenv("SMTP_USERNAME"), smtpPasswordFile: os.Getenv("SMTP_PASSWORD_FILE"), smtpCAFile: os.Getenv("SMTP_ROOT_CA_FILE"), smtpCertFile: os.Getenv("SMTP_CLIENT_CERT_FILE"), smtpKeyFile: os.Getenv("SMTP_CLIENT_KEY_FILE"), smtpDialTimeout: smtpDialTimeout,
		allowInsecureDevelopment: allowInsecure, streamReplicas: replicas, concurrency: concurrency, environment: os.Getenv("LITES_ENVIRONMENT"), serviceVersion: os.Getenv("LITES_VERSION"), region: os.Getenv("LITES_REGION"), otlpEndpoint: os.Getenv("OTLP_GRPC_ENDPOINT"), otlpCAFile: os.Getenv("OTLP_ROOT_CA_FILE"), otlpCertFile: os.Getenv("OTLP_CLIENT_CERT_FILE"), otlpKeyFile: os.Getenv("OTLP_CLIENT_KEY_FILE"), otlpTLSName: os.Getenv("OTLP_TLS_SERVER_NAME"), otlpBearerTokenFile: os.Getenv("OTLP_BEARER_TOKEN_FILE"), traceSampleRatio: traceSampleRatio,
	}
	return value, value.validate()
}

func (value config) validate() error {
	appURL, appErr := url.Parse(value.publicAppURL)
	loopbackHTTP := appErr == nil && appURL.Scheme == "http" && appURL.Hostname() == "127.0.0.1" && value.allowInsecureDevelopment
	validAppURL := appErr == nil && appURL.Host != "" && appURL.User == nil && appURL.RawQuery == "" && appURL.Fragment == "" && (appURL.Scheme == "https" || loopbackHTTP)
	if value.databaseURL == "" || value.epochURL == "" || len(value.natsURLs) == 0 || value.vaultAddress == "" || value.s3Region == "" || value.payloadBucket == "" || value.inboxPepperFile == "" || !validAppURL || value.smtpAddress == "" || value.smtpServerName == "" || value.smtpFromAddress == "" || value.smtpDialTimeout <= 0 || value.smtpDialTimeout > 2*time.Minute || value.environment == "" || value.serviceVersion == "" || value.region == "" || value.traceSampleRatio < 0 || value.traceSampleRatio > 1 || value.streamReplicas < 1 || value.concurrency < 1 || value.concurrency > 128 || (value.epochCertFile == "") != (value.epochKeyFile == "") || (value.natsCertFile == "") != (value.natsKeyFile == "") || (value.vaultCertFile == "") != (value.vaultKeyFile == "") || (value.smtpCertFile == "") != (value.smtpKeyFile == "") || (value.smtpUsername == "") != (value.smtpPasswordFile == "") || (value.otlpCertFile == "") != (value.otlpKeyFile == "") {
		return errors.New("required mail worker configuration is missing or invalid")
	}
	if value.s3Encryption == types.ServerSideEncryptionAwsKms && value.s3KMSKeyID == "" {
		return errors.New("S3_KMS_KEY_ID is required for aws:kms")
	}
	if value.s3Encryption != types.ServerSideEncryptionAwsKms && value.s3Encryption != types.ServerSideEncryptionAes256 {
		return errors.New("S3_SERVER_SIDE_ENCRYPTION must be AES256 or aws:kms")
	}
	if !value.allowInsecureDevelopment && (value.databaseURLFile == "" || value.streamReplicas < 3 || (value.natsCredentialsFile == "" && value.natsCertFile == "") || (value.epochTokenFile == "" && value.epochCertFile == "") || (value.vaultTokenFile == "" && value.vaultCertFile == "") || (value.smtpPasswordFile == "" && value.smtpCertFile == "") || value.otlpEndpoint == "" || (value.otlpBearerTokenFile == "" && value.otlpCertFile == "")) {
		return errors.New("production requires file-backed credentials, three replicas, and authenticated dependencies")
	}
	return nil
}

func (value config) smtp() (identitymail.SMTPSender, *url.URL, error) {
	appURL, err := url.Parse(value.publicAppURL)
	if err != nil {
		return identitymail.SMTPSender{}, nil, errors.New("PUBLIC_APP_URL is invalid")
	}
	password := ""
	if value.smtpPasswordFile != "" {
		password, err = readSecret(value.smtpPasswordFile, 8192)
		if err != nil {
			return identitymail.SMTPSender{}, nil, errors.New("SMTP password is unavailable")
		}
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return identitymail.SMTPSender{}, nil, errors.New("system trust store is unavailable")
	}
	if value.smtpCAFile != "" {
		contents, readErr := os.ReadFile(value.smtpCAFile)
		if readErr != nil || !roots.AppendCertsFromPEM(contents) {
			return identitymail.SMTPSender{}, nil, errors.New("SMTP root CA is invalid")
		}
	}
	var certificate *tls.Certificate
	if value.smtpCertFile != "" {
		loaded, loadErr := tls.LoadX509KeyPair(value.smtpCertFile, value.smtpKeyFile)
		if loadErr != nil {
			return identitymail.SMTPSender{}, nil, errors.New("SMTP client certificate is invalid")
		}
		certificate = &loaded
	}
	return identitymail.SMTPSender{Config: identitymail.SMTPConfig{Address: value.smtpAddress, ServerName: value.smtpServerName, FromAddress: value.smtpFromAddress, FromName: value.smtpFromName, Username: value.smtpUsername, Password: password, RootCAs: roots, ClientCertificate: certificate, DialTimeout: value.smtpDialTimeout}}, appURL, nil
}

func (value config) epochClient() (*http.Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13}
	if value.epochCAFile != "" {
		contents, err := os.ReadFile(value.epochCAFile)
		if err != nil {
			return nil, errors.New("load epoch CA")
		}
		roots, err := x509.SystemCertPool()
		if err != nil || !roots.AppendCertsFromPEM(contents) {
			return nil, errors.New("epoch CA is invalid")
		}
		tlsConfig.RootCAs = roots
	}
	if value.epochCertFile != "" {
		certificate, err := tls.LoadX509KeyPair(value.epochCertFile, value.epochKeyFile)
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
func splitNonempty(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
