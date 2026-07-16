package main

import (
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/langshift/lites/internal/capacity/reference"
)

type config struct {
	listenAddress, healthAddress                               string
	serverCertificateFile, serverKeyFile, serverClientCAFile   string
	bearerTokenFile, profileFile, datasetFile, sourceCommit    string
	gatewayURL, gatewayCAFile, gatewayCertFile, gatewayKeyFile string
	gatewayTLSServerName, publicOrigin                         string
	environment, version, region                               string
	otlpEndpoint, otlpCAFile, otlpCertFile, otlpKeyFile        string
	otlpTLSName, otlpTokenFile                                 string
	warmupDuration, requestTimeout                             time.Duration
	maximumInflight                                            int
	maximumErrorRatio, traceRatio                              float64
}

func loadConfig() (config, error) {
	warmup, err := durationEnv("CAPACITY_WARMUP_DURATION", 5*time.Minute)
	if err != nil {
		return config{}, err
	}
	requestTimeout, err := durationEnv("CAPACITY_REQUEST_TIMEOUT", 30*time.Second)
	if err != nil {
		return config{}, err
	}
	maximumInflight, err := intEnv("CAPACITY_MAX_INFLIGHT", 4000)
	if err != nil {
		return config{}, err
	}
	maximumErrorRatio, err := floatEnv("CAPACITY_MAX_ERROR_RATIO", .0005)
	if err != nil {
		return config{}, err
	}
	traceRatio, err := floatEnv("TRACE_SAMPLE_RATIO", .01)
	if err != nil {
		return config{}, err
	}
	value := config{
		listenAddress: env("LISTEN_ADDRESS", ":8443"), healthAddress: env("HEALTH_ADDRESS", "127.0.0.1:8092"),
		serverCertificateFile: os.Getenv("SERVER_TLS_CERT_FILE"), serverKeyFile: os.Getenv("SERVER_TLS_KEY_FILE"), serverClientCAFile: os.Getenv("SERVER_CLIENT_CA_FILE"), bearerTokenFile: os.Getenv("CAPACITY_CONTROLLER_BEARER_TOKEN_FILE"),
		profileFile: os.Getenv("CAPACITY_PROFILE_FILE"), datasetFile: os.Getenv("CAPACITY_DATASET_FILE"), sourceCommit: os.Getenv("LITES_SOURCE_COMMIT"),
		gatewayURL: os.Getenv("GATEWAY_URL"), gatewayCAFile: os.Getenv("GATEWAY_ROOT_CA_FILE"), gatewayCertFile: os.Getenv("GATEWAY_CLIENT_CERT_FILE"), gatewayKeyFile: os.Getenv("GATEWAY_CLIENT_KEY_FILE"), gatewayTLSServerName: os.Getenv("GATEWAY_TLS_SERVER_NAME"), publicOrigin: os.Getenv("PUBLIC_ORIGIN"),
		environment: os.Getenv("LITES_ENVIRONMENT"), version: os.Getenv("LITES_VERSION"), region: os.Getenv("LITES_REGION"),
		otlpEndpoint: os.Getenv("OTLP_GRPC_ENDPOINT"), otlpCAFile: os.Getenv("OTLP_ROOT_CA_FILE"), otlpCertFile: os.Getenv("OTLP_CLIENT_CERT_FILE"), otlpKeyFile: os.Getenv("OTLP_CLIENT_KEY_FILE"), otlpTLSName: os.Getenv("OTLP_TLS_SERVER_NAME"), otlpTokenFile: os.Getenv("OTLP_BEARER_TOKEN_FILE"),
		warmupDuration: warmup, requestTimeout: requestTimeout, maximumInflight: maximumInflight, maximumErrorRatio: maximumErrorRatio, traceRatio: traceRatio,
	}
	return value, value.validate()
}

func (value config) validate() error {
	required := []string{value.listenAddress, value.healthAddress, value.serverCertificateFile, value.serverKeyFile, value.serverClientCAFile, value.bearerTokenFile, value.profileFile, value.datasetFile, value.sourceCommit, value.gatewayURL, value.gatewayCAFile, value.gatewayCertFile, value.gatewayKeyFile, value.gatewayTLSServerName, value.publicOrigin, value.environment, value.version, value.region, value.otlpEndpoint, value.otlpCAFile}
	for _, item := range required {
		if item == "" {
			return errors.New("required reference capacity controller configuration is missing")
		}
	}
	if value.environment != reference.ProfileID || (value.otlpTokenFile == "") == (value.otlpCertFile == "") || (value.otlpCertFile == "") != (value.otlpKeyFile == "") || value.warmupDuration < 30*time.Second || value.warmupDuration > 15*time.Minute || value.requestTimeout < time.Second || value.requestTimeout > time.Minute || value.maximumInflight < 500 || value.maximumInflight > 100000 || value.maximumErrorRatio < 0 || value.maximumErrorRatio > .001 || value.traceRatio < 0 || value.traceRatio > 1 {
		return errors.New("reference capacity controller configuration is invalid")
	}
	for _, path := range []string{value.serverCertificateFile, value.serverKeyFile, value.serverClientCAFile, value.bearerTokenFile, value.profileFile, value.datasetFile, value.gatewayCAFile, value.gatewayCertFile, value.gatewayKeyFile, value.otlpCAFile, value.otlpCertFile, value.otlpKeyFile, value.otlpTokenFile} {
		if path != "" && !strings.HasPrefix(path, "/") {
			return errors.New("reference capacity controller file paths must be absolute")
		}
	}
	for _, endpoint := range []string{value.gatewayURL, value.publicOrigin} {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (endpoint == value.publicOrigin && parsed.Path != "") || (endpoint == value.gatewayURL && parsed.Path != "" && parsed.Path != "/") {
			return errors.New("reference capacity controller endpoint is invalid")
		}
	}
	return nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func durationEnv(name string, fallback time.Duration) (time.Duration, error) {
	if value := os.Getenv(name); value != "" {
		return time.ParseDuration(value)
	}
	return fallback, nil
}

func intEnv(name string, fallback int) (int, error) {
	if value := os.Getenv(name); value != "" {
		return strconv.Atoi(value)
	}
	return fallback, nil
}

func floatEnv(name string, fallback float64) (float64, error) {
	if value := os.Getenv(name); value != "" {
		return strconv.ParseFloat(value, 64)
	}
	return fallback, nil
}
