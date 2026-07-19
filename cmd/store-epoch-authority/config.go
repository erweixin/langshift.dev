package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
)

type config struct {
	listenAddress, healthAddress                         string
	epochFile, bearerTokenFile                           string
	serverCertificateFile, serverKeyFile, serverClientCA string
	environment, serviceVersion, region                  string
	otlpEndpoint, otlpCAFile, otlpCertFile               string
	otlpKeyFile, otlpTLSName, otlpBearerTokenFile        string
	allowInsecureDevelopment                             bool
	traceSampleRatio                                     float64
}

func loadConfig() (config, error) {
	allowInsecure, err := optionalBool("ALLOW_INSECURE_DEVELOPMENT", false)
	if err != nil {
		return config{}, err
	}
	traceRatio, err := optionalFloat("TRACE_SAMPLE_RATIO", 0.1)
	if err != nil {
		return config{}, err
	}
	value := config{
		listenAddress: envString("LISTEN_ADDRESS", ":8443"), healthAddress: envString("HEALTH_ADDRESS", "127.0.0.1:8084"),
		epochFile: os.Getenv("STORE_EPOCH_FILE"), bearerTokenFile: os.Getenv("STORE_EPOCH_BEARER_TOKEN_FILE"),
		serverCertificateFile: os.Getenv("SERVER_TLS_CERT_FILE"), serverKeyFile: os.Getenv("SERVER_TLS_KEY_FILE"), serverClientCA: os.Getenv("SERVER_CLIENT_CA_FILE"),
		environment: os.Getenv("LITES_ENVIRONMENT"), serviceVersion: os.Getenv("LITES_VERSION"), region: os.Getenv("LITES_REGION"),
		otlpEndpoint: os.Getenv("OTLP_GRPC_ENDPOINT"), otlpCAFile: os.Getenv("OTLP_ROOT_CA_FILE"), otlpCertFile: os.Getenv("OTLP_CLIENT_CERT_FILE"), otlpKeyFile: os.Getenv("OTLP_CLIENT_KEY_FILE"), otlpTLSName: os.Getenv("OTLP_TLS_SERVER_NAME"), otlpBearerTokenFile: os.Getenv("OTLP_BEARER_TOKEN_FILE"),
		allowInsecureDevelopment: allowInsecure, traceSampleRatio: traceRatio,
	}
	return value, value.validate()
}

func (value config) validate() error {
	if value.listenAddress == "" || value.healthAddress == "" || value.epochFile == "" || value.bearerTokenFile == "" || value.environment == "" || value.serviceVersion == "" || value.region == "" || value.traceSampleRatio < 0 || value.traceSampleRatio > 1 || (value.serverCertificateFile == "") != (value.serverKeyFile == "") || (value.otlpCertFile == "") != (value.otlpKeyFile == "") {
		return errors.New("required store epoch authority configuration is missing or invalid")
	}
	healthHost, _, err := net.SplitHostPort(value.healthAddress)
	if err != nil || !isLoopbackHost(healthHost) {
		return errors.New("store epoch health listener must use a loopback address")
	}
	if value.allowInsecureDevelopment {
		if value.serverCertificateFile == "" {
			host, _, err := net.SplitHostPort(value.listenAddress)
			if err != nil || !isLoopbackHost(host) {
				return errors.New("insecure development listener must use a loopback address")
			}
			return nil
		}
		if value.serverClientCA == "" {
			return errors.New("network-reachable development listener requires mTLS")
		}
		return nil
	}
	if value.serverCertificateFile == "" || value.serverClientCA == "" || value.otlpEndpoint == "" || (value.otlpBearerTokenFile == "" && value.otlpCertFile == "") {
		return errors.New("production requires mTLS and authenticated telemetry")
	}
	return nil
}

func (value config) serverTLS() (*tls.Config, error) {
	if value.allowInsecureDevelopment && value.serverCertificateFile == "" {
		return nil, nil
	}
	certificate, err := tls.LoadX509KeyPair(value.serverCertificateFile, value.serverKeyFile)
	if err != nil {
		return nil, errors.New("store epoch server certificate is invalid")
	}
	contents, err := os.ReadFile(value.serverClientCA)
	if err != nil {
		return nil, errors.New("store epoch client CA is unavailable")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(contents) {
		return nil, errors.New("store epoch client CA is invalid")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert}, nil
}

func readCredential(path string, limit int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, limit+1))
	value := strings.TrimSpace(string(contents))
	if err != nil || int64(len(contents)) > limit || value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("credential is empty, ambiguous, or too large")
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

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
