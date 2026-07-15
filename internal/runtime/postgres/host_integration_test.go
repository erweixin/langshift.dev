//go:build integration

package postgres

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestRuntimeServiceCanRegisterAndRecoverDrainingHost(t *testing.T) {
	ctx := context.Background()
	service := integrationPool(t, ctx, "LITES_TEST_RUNTIME_DATABASE_URL")
	defer service.Close()

	const epoch = "90000000-0000-0000-0000-000000009236"
	now := time.Date(2026, 7, 15, 14, 0, 0, 0, time.UTC)
	control := bytes.Repeat([]byte{0x36}, 32)
	digest := "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	store := Store{
		Pool: service, Epochs: fixedEpoch(epoch), StoreEpoch: epoch,
		IDKey: bytes.Repeat([]byte{0x37}, 32), TokenPepper: bytes.Repeat([]byte{0x38}, 32),
	}
	registration := HostRegistration{
		HostID: "runtime-host-restart-9236", PoolKey: "runtime-untrusted", Architecture: "x86_64", AvailabilityZone: "zone-a",
		KernelCatalogHash: digest, RootFSCatalogHash: digest, ScratchDigest: digest, ControlTokenHash: control,
		CapacityVCPU: 2, CapacityMemoryMiB: 512, CapacityDiskMiB: 4096, CapacitySessions: 1,
		ObservedAt: now, HeartbeatDeadline: now.Add(30 * time.Second),
	}
	version, err := store.RegisterHost(ctx, registration)
	if err != nil || version != 1 {
		t.Fatalf("RegisterHost() version=%d err=%v", version, err)
	}
	version, err = store.SetHostStatus(ctx, registration.HostID, version, control, "active", now.Add(time.Second), now.Add(31*time.Second))
	if err != nil || version != 2 {
		t.Fatalf("activate version=%d err=%v", version, err)
	}
	version, err = store.SetHostStatus(ctx, registration.HostID, version, control, "draining", now.Add(2*time.Second), now.Add(32*time.Second))
	if err != nil || version != 3 {
		t.Fatalf("drain version=%d err=%v", version, err)
	}
	registration.ObservedAt = now.Add(3 * time.Second)
	registration.HeartbeatDeadline = now.Add(33 * time.Second)
	version, err = store.RegisterHost(ctx, registration)
	if err != nil || version != 4 {
		t.Fatalf("recover registration version=%d err=%v", version, err)
	}
}
