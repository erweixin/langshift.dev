package main

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type config struct {
	databaseURL         string
	healthAddress       string
	environment         string
	behaviorEnvironment string
	storeEpoch          string
	publicTenantID      string
	contentReleaseDir   string
	pollInterval        time.Duration
	localCompose        bool
}

func loadConfig() (config, error) {
	localCompose, err := strconv.ParseBool(value("LITES_LOCAL_COMPOSE", "false"))
	if err != nil {
		return config{}, err
	}
	poll, err := time.ParseDuration(value("TEST_ADAPTER_POLL_INTERVAL", "250ms"))
	if err != nil {
		return config{}, err
	}
	result := config{
		databaseURL: os.Getenv("DATABASE_URL"), healthAddress: value("HEALTH_ADDRESS", "127.0.0.1:8099"),
		environment: os.Getenv("LITES_ENVIRONMENT"), behaviorEnvironment: value("ROUTE_BEHAVIOR_ENVIRONMENT", "production"),
		storeEpoch: os.Getenv("STORE_EPOCH"), publicTenantID: os.Getenv("IDENTITY_PUBLIC_TENANT_ID"), contentReleaseDir: value("PRODUCT_CONTENT_RELEASE_DIR", "/app/product-content/releases/1.0.0"), pollInterval: poll, localCompose: localCompose,
	}
	return result, result.validate()
}

func (configuration config) validate() error {
	if configuration.environment != "engineering-test" || configuration.databaseURL == "" || configuration.storeEpoch == "" || configuration.publicTenantID != "20000000-0000-4000-8000-000000000001" || configuration.contentReleaseDir == "" || configuration.pollInterval < 50*time.Millisecond || configuration.pollInterval > 10*time.Second || configuration.behaviorEnvironment != "staging" && configuration.behaviorEnvironment != "production" {
		return errors.New("macOS test adapter requires an explicit engineering-test configuration")
	}
	endpoint, err := url.Parse(configuration.databaseURL)
	if err != nil || endpoint.Scheme != "postgres" && endpoint.Scheme != "postgresql" || endpoint.Hostname() == "" {
		return errors.New("test adapter database endpoint is invalid")
	}
	if !loopback(endpoint.Hostname()) && !(configuration.localCompose && endpoint.Hostname() == "postgres") {
		return errors.New("test adapter database must be loopback or the local Compose postgres service")
	}
	host, _, err := net.SplitHostPort(configuration.healthAddress)
	if err != nil || !loopback(host) && !(configuration.localCompose && (host == "0.0.0.0" || host == "")) {
		return errors.New("test adapter health listener is not local")
	}
	return nil
}

func loopback(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func value(name, fallback string) string {
	if selected := os.Getenv(name); selected != "" {
		return selected
	}
	return fallback
}
