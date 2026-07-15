package postgres

import (
	"testing"
	"time"
)

func TestHostRegistrationRequiresBoundedCapacityAndHeartbeat(t *testing.T) {
	at := time.Date(2026, 7, 15, 22, 0, 0, 0, time.UTC)
	command := HostRegistration{HostID: "host", PoolKey: "runtime-untrusted", Architecture: "x86_64", AvailabilityZone: "zone-a", KernelCatalogHash: "hash", RootFSCatalogHash: "hash", ScratchDigest: "digest", ControlTokenHash: make([]byte, 32), CapacityVCPU: 2, CapacityMemoryMiB: 512, CapacityDiskMiB: 4096, CapacitySessions: 1, ObservedAt: at, HeartbeatDeadline: at.Add(time.Minute)}
	if !validHostRegistration(command) {
		t.Fatal("valid host registration rejected")
	}
	command.HeartbeatDeadline = at
	if validHostRegistration(command) {
		t.Fatal("non-advancing heartbeat accepted")
	}
}
