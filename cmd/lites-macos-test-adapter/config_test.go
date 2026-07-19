package main

import "testing"

func TestConfigIsFailClosedOutsideEngineeringTest(t *testing.T) {
	valid := config{databaseURL: "postgres://local@127.0.0.1:5432/lites", healthAddress: "127.0.0.1:8099", environment: "engineering-test", behaviorEnvironment: "production", storeEpoch: "epoch", publicTenantID: "20000000-0000-4000-8000-000000000001", contentReleaseDir: "/app/product-content/releases/1.0.0", pollInterval: 100_000_000}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	invalid := []config{valid, valid, valid}
	invalid[0].environment = "production"
	invalid[1].databaseURL = "postgres://local@database.example.com/lites"
	invalid[2].healthAddress = "0.0.0.0:8099"
	for _, candidate := range invalid {
		if candidate.validate() == nil {
			t.Fatalf("unsafe test adapter configuration accepted: %#v", candidate)
		}
	}
	compose := valid
	compose.databaseURL = "postgres://local@postgres:5432/lites"
	compose.healthAddress = "0.0.0.0:8099"
	compose.localCompose = true
	if err := compose.validate(); err != nil {
		t.Fatalf("local Compose configuration rejected: %v", err)
	}
}
