package main

import "testing"

func TestConfigurationCannotEscapeEngineeringTest(t *testing.T) {
	valid := config{environment: "engineering-test", address: "127.0.0.1:8443", certificateFile: "cert", keyFile: "key", tokenFile: "token", model: "lites-macos-fixture-v1", maximumRequestBytes: 8192}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	production := valid
	production.environment = "production"
	if production.validate() == nil {
		t.Fatal("production environment accepted")
	}
	external := valid
	external.address = "0.0.0.0:8443"
	if external.validate() == nil {
		t.Fatal("wildcard listener accepted outside local Compose")
	}
	compose := valid
	compose.address = "0.0.0.0:443"
	compose.localCompose = true
	if err := compose.validate(); err != nil {
		t.Fatalf("local Compose rejected: %v", err)
	}
}
