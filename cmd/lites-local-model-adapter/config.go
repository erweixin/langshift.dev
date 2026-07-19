package main

import (
	"errors"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var adapterModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type config struct {
	environment, address, certificateFile, keyFile, tokenFile, model string
	localCompose                                                     bool
	maximumRequestBytes                                              int64
}

func loadConfig() (config, error) {
	localCompose, err := strconv.ParseBool(value("LITES_LOCAL_COMPOSE", "false"))
	if err != nil {
		return config{}, err
	}
	maximum, err := strconv.ParseInt(value("MODEL_ADAPTER_MAX_REQUEST_BYTES", "8388608"), 10, 64)
	if err != nil {
		return config{}, err
	}
	result := config{
		environment: os.Getenv("LITES_ENVIRONMENT"), address: value("MODEL_ADAPTER_ADDRESS", "127.0.0.1:8443"),
		certificateFile: os.Getenv("MODEL_ADAPTER_TLS_CERT_FILE"), keyFile: os.Getenv("MODEL_ADAPTER_TLS_KEY_FILE"),
		tokenFile: os.Getenv("MODEL_ADAPTER_BEARER_TOKEN_FILE"), model: value("MODEL_ADAPTER_MODEL", "lites-macos-fixture-v1"),
		localCompose: localCompose, maximumRequestBytes: maximum,
	}
	return result, result.validate()
}

func (configuration config) validate() error {
	if configuration.environment != "engineering-test" || configuration.certificateFile == "" || configuration.keyFile == "" || configuration.tokenFile == "" || !adapterModelPattern.MatchString(configuration.model) || configuration.maximumRequestBytes < 1024 || configuration.maximumRequestBytes > 64<<20 {
		return errors.New("local model adapter requires an explicit engineering-test configuration")
	}
	host, port, err := net.SplitHostPort(configuration.address)
	if err != nil || port == "" {
		return errors.New("local model adapter listener is invalid")
	}
	if !isLoopback(host) && !(configuration.localCompose && (host == "0.0.0.0" || host == "")) {
		return errors.New("local model adapter listener is not local")
	}
	if configuration.localCompose && port != "443" {
		return errors.New("local Compose model adapter must use TLS port 443")
	}
	return nil
}

func isLoopback(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func value(name, fallback string) string {
	if selected := os.Getenv(name); selected != "" {
		return selected
	}
	return fallback
}
