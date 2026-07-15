package postgres

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestUsageManifestIsVersionedStrictAndNonNegative(t *testing.T) {
	valid := []json.RawMessage{
		json.RawMessage(`{"schema_version":1,"vcpu_millis":0}`),
		json.RawMessage(`{"schema_version":1,"vcpu_millis":12,"wall_millis":20,"peak_memory_mib":128,"disk_read_bytes":1,"disk_write_bytes":2,"network_ingress_bytes":3,"network_egress_bytes":4,"exit_code":0}`),
	}
	for _, value := range valid {
		if !validUsageManifest(value) {
			t.Fatalf("valid manifest rejected: %s", value)
		}
	}
	invalid := []json.RawMessage{
		nil,
		json.RawMessage(`{}`),
		json.RawMessage(`{"schema_version":2,"vcpu_millis":0}`),
		json.RawMessage(`{"schema_version":1,"vcpu_millis":-1}`),
		json.RawMessage(`{"schema_version":1,"vcpu_millis":0,"secret":"leak"}`),
		json.RawMessage(`{"schema_version":1,"vcpu_millis":0} trailing`),
	}
	for _, value := range invalid {
		if validUsageManifest(value) {
			t.Fatalf("invalid manifest accepted: %s", value)
		}
	}
}

func TestTerminationMayUseExpiredAllocationIdentityButExecutionMayNot(t *testing.T) {
	at := time.Date(2026, 7, 15, 15, 0, 0, 0, time.UTC)
	digest := bytes.Repeat([]byte{0x42}, 32)
	session := lifecycleSession{ProvisionAttemptID: "attempt", ProvisionFence: 3}
	allocation := lifecycleAllocation{LeaseHash: digest}
	allocation.LeaseExpiresAt.Valid = true
	allocation.LeaseExpiresAt.Time = at.Add(-time.Second)
	command := LifecycleCommand{ProvisionAttemptID: "attempt", ProvisionFence: 3, LeaseToken: "opaque"}
	if validAllocationAuthority(session, &allocation, command, digest, at) {
		t.Fatal("expired allocation authorized continued execution")
	}
	if !validAllocationIdentity(session, &allocation, command, digest) {
		t.Fatal("expired allocation identity could not authorize cleanup")
	}
	wrong := append([]byte(nil), digest...)
	wrong[0]++
	if validAllocationIdentity(session, &allocation, command, wrong) {
		t.Fatal("wrong allocation token authorized cleanup")
	}
}
