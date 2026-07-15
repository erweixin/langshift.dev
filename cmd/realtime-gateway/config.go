package main

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type config struct {
	databaseURL, databaseURLFile, listenAddress, healthAddress       string
	serverCertificateFile, serverKeyFile, serverClientCAFile         string
	trustedKeyringFile, trustedIssuer, trustedAudience               string
	natsURLs                                                         []string
	natsCredentialsFile, natsCAFile, natsCertFile, natsKeyFile       string
	environment, serviceVersion, region                              string
	otlpEndpoint, otlpCAFile, otlpCertFile, otlpKeyFile, otlpTLSName string
	otlpBearerTokenFile                                              string
	pageSize, databaseMaxConnections                                 int
	heartbeat, catchUpInterval, wakeRetry, sendTimeout, reauthLead   time.Duration
	traceSampleRatio                                                 float64
	allowInsecureDevelopment                                         bool
}

func loadConfig() (config, error) {
	allowInsecure, err := realtimeOptionalBool("ALLOW_INSECURE_DEVELOPMENT", false)
	if err != nil {
		return config{}, err
	}
	maxConnections, err := realtimeOptionalInt("DATABASE_MAX_CONNECTIONS", 64)
	if err != nil {
		return config{}, err
	}
	pageSize, err := realtimeOptionalInt("REALTIME_PAGE_SIZE", 200)
	if err != nil {
		return config{}, err
	}
	heartbeat, err := realtimeOptionalDuration("REALTIME_HEARTBEAT", 15*time.Second)
	if err != nil {
		return config{}, err
	}
	catchUp, err := realtimeOptionalDuration("REALTIME_CATCH_UP_INTERVAL", 2*time.Second)
	if err != nil {
		return config{}, err
	}
	wakeRetry, err := realtimeOptionalDuration("REALTIME_WAKE_RETRY", 5*time.Second)
	if err != nil {
		return config{}, err
	}
	sendTimeout, err := realtimeOptionalDuration("REALTIME_SEND_TIMEOUT", 10*time.Second)
	if err != nil {
		return config{}, err
	}
	reauthLead, err := realtimeOptionalDuration("REALTIME_REAUTH_LEAD", 30*time.Second)
	if err != nil {
		return config{}, err
	}
	traceRatio, err := realtimeOptionalFloat("TRACE_SAMPLE_RATIO", 0.1)
	if err != nil {
		return config{}, err
	}
	databaseURLFile := os.Getenv("DATABASE_URL_FILE")
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURLFile != "" {
		if databaseURL != "" {
			return config{}, errors.New("DATABASE_URL and DATABASE_URL_FILE are mutually exclusive")
		}
		databaseURL, err = realtimeReadSecret(databaseURLFile, 8192)
		if err != nil {
			return config{}, errors.New("DATABASE_URL_FILE is unreadable")
		}
	}
	value := config{
		databaseURL: databaseURL, databaseURLFile: databaseURLFile, databaseMaxConnections: maxConnections,
		listenAddress: realtimeEnv("LISTEN_ADDRESS", ":8443"), healthAddress: realtimeEnv("HEALTH_ADDRESS", "127.0.0.1:8084"),
		serverCertificateFile: os.Getenv("SERVER_TLS_CERT_FILE"), serverKeyFile: os.Getenv("SERVER_TLS_KEY_FILE"), serverClientCAFile: os.Getenv("SERVER_CLIENT_CA_FILE"),
		trustedKeyringFile: os.Getenv("TRUSTED_CONTEXT_KEYRING_FILE"), trustedIssuer: realtimeEnv("TRUSTED_CONTEXT_ISSUER", "lites-gateway"), trustedAudience: realtimeEnv("TRUSTED_CONTEXT_AUDIENCE", "realtime-gateway"),
		natsURLs: realtimeSplit(os.Getenv("NATS_URLS")), natsCredentialsFile: os.Getenv("NATS_CREDENTIALS_FILE"), natsCAFile: os.Getenv("NATS_ROOT_CA_FILE"), natsCertFile: os.Getenv("NATS_CLIENT_CERT_FILE"), natsKeyFile: os.Getenv("NATS_CLIENT_KEY_FILE"),
		environment: os.Getenv("LITES_ENVIRONMENT"), serviceVersion: os.Getenv("LITES_VERSION"), region: os.Getenv("LITES_REGION"),
		otlpEndpoint: os.Getenv("OTLP_GRPC_ENDPOINT"), otlpCAFile: os.Getenv("OTLP_ROOT_CA_FILE"), otlpCertFile: os.Getenv("OTLP_CLIENT_CERT_FILE"), otlpKeyFile: os.Getenv("OTLP_CLIENT_KEY_FILE"), otlpTLSName: os.Getenv("OTLP_TLS_SERVER_NAME"), otlpBearerTokenFile: os.Getenv("OTLP_BEARER_TOKEN_FILE"),
		pageSize: pageSize, heartbeat: heartbeat, catchUpInterval: catchUp, wakeRetry: wakeRetry, sendTimeout: sendTimeout, reauthLead: reauthLead, traceSampleRatio: traceRatio, allowInsecureDevelopment: allowInsecure,
	}
	return value, value.validate()
}

func (value config) validate() error {
	if value.databaseURL == "" || value.listenAddress == "" || value.healthAddress == "" || value.trustedKeyringFile == "" || value.trustedIssuer == "" || value.trustedAudience == "" || len(value.natsURLs) == 0 || value.environment == "" || value.serviceVersion == "" || value.region == "" || value.databaseMaxConnections < 8 || value.databaseMaxConnections > 512 || value.pageSize < 1 || value.pageSize > 1000 || value.heartbeat <= 0 || value.catchUpInterval <= 0 || value.wakeRetry <= 0 || value.sendTimeout <= 0 || value.reauthLead < 0 || value.traceSampleRatio < 0 || value.traceSampleRatio > 1 || (value.serverCertificateFile == "") != (value.serverKeyFile == "") || (value.natsCertFile == "") != (value.natsKeyFile == "") || (value.otlpCertFile == "") != (value.otlpKeyFile == "") {
		return errors.New("required realtime gateway configuration is missing or invalid")
	}
	for _, raw := range value.natsURLs {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "tls" && !(value.allowInsecureDevelopment && parsed.Scheme == "nats")) {
			return errors.New("NATS_URLS contains an invalid endpoint")
		}
	}
	if !value.allowInsecureDevelopment && (value.databaseURLFile == "" || value.serverCertificateFile == "" || value.serverClientCAFile == "" || value.natsCAFile == "" || value.natsCredentialsFile == "" && value.natsCertFile == "" || value.otlpEndpoint == "" || value.otlpBearerTokenFile == "" && value.otlpCertFile == "") {
		return errors.New("production requires file-backed credentials and authenticated TLS dependencies")
	}
	return nil
}

func realtimeReadSecret(path string, limit int64) (string, error) {
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
		return "", errors.New("secret is empty, ambiguous, or too large")
	}
	return value, nil
}

func realtimeEnv(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func realtimeSplit(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func realtimeOptionalBool(name string, fallback bool) (bool, error) {
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

func realtimeOptionalInt(name string, fallback int) (int, error) {
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

func realtimeOptionalFloat(name string, fallback float64) (float64, error) {
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

func realtimeOptionalDuration(name string, fallback time.Duration) (time.Duration, error) {
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
