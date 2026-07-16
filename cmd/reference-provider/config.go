package main

import (
	"errors"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var errConfiguration = errors.New("reference provider configuration is invalid")

type config struct {
	serverAddress, healthAddress   string
	tlsCertificateFile, tlsKeyFile string
	model, bearerTokenFile         string
	localHold, replayHold          time.Duration
	runtimeArtifactBytes           int
	runtimeHoldMilliseconds        int
	maximumRequestBytes            int64
	maximumConcurrentRequests      int64
	replayRateNumerator            uint8
	version, environment, region   string
}

func loadConfig() (config, error) {
	configuration := config{
		serverAddress: env("SERVER_ADDRESS", ":8443"), healthAddress: env("HEALTH_ADDRESS", ":8081"),
		tlsCertificateFile: os.Getenv("SERVER_TLS_CERT_FILE"), tlsKeyFile: os.Getenv("SERVER_TLS_KEY_FILE"),
		model: os.Getenv("REFERENCE_PROVIDER_MODEL"), bearerTokenFile: os.Getenv("REFERENCE_PROVIDER_BEARER_TOKEN_FILE"),
		version: os.Getenv("LITES_VERSION"), environment: os.Getenv("LITES_ENVIRONMENT"), region: os.Getenv("LITES_REGION"),
	}
	var err error
	if configuration.localHold, err = time.ParseDuration(env("REFERENCE_LOCAL_HOLD", "400ms")); err != nil {
		return config{}, errConfiguration
	}
	if configuration.replayHold, err = time.ParseDuration(env("REFERENCE_REPLAY_HOLD", "0s")); err != nil {
		return config{}, errConfiguration
	}
	configuration.runtimeArtifactBytes, err = strconv.Atoi(env("REFERENCE_RUNTIME_ARTIFACT_BYTES", "1048576"))
	if err != nil {
		return config{}, errConfiguration
	}
	configuration.runtimeHoldMilliseconds, err = strconv.Atoi(env("REFERENCE_RUNTIME_HOLD_MILLISECONDS", "1000"))
	if err != nil {
		return config{}, errConfiguration
	}
	configuration.maximumRequestBytes, err = strconv.ParseInt(env("REFERENCE_MAXIMUM_REQUEST_BYTES", "8388608"), 10, 64)
	if err != nil {
		return config{}, errConfiguration
	}
	configuration.maximumConcurrentRequests, err = strconv.ParseInt(env("REFERENCE_MAXIMUM_CONCURRENT_REQUESTS", "4096"), 10, 64)
	if err != nil {
		return config{}, errConfiguration
	}
	replay, err := strconv.ParseUint(env("REFERENCE_REPLAY_RATE_NUMERATOR", "153"), 10, 8)
	if err != nil || replay == 0 {
		return config{}, errConfiguration
	}
	configuration.replayRateNumerator = uint8(replay)
	if !validAddress(configuration.serverAddress) || !validAddress(configuration.healthAddress) || configuration.serverAddress == configuration.healthAddress || !absoluteFile(configuration.tlsCertificateFile) || !absoluteFile(configuration.tlsKeyFile) || !absoluteFile(configuration.bearerTokenFile) || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`).MatchString(configuration.model) || configuration.version == "" || configuration.environment != "stage3-reference-production-v1" || configuration.region == "" {
		return config{}, errConfiguration
	}
	return configuration, nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func validAddress(value string) bool {
	host, port, err := net.SplitHostPort(value)
	if err != nil || port == "" || strings.ContainsAny(host, "\x00\r\n") {
		return false
	}
	number, err := strconv.Atoi(port)
	return err == nil && number > 0 && number <= 65535
}

func absoluteFile(value string) bool {
	return strings.HasPrefix(value, "/") && value != "/" && !strings.ContainsAny(value, "\x00\r\n")
}
