package firecracker

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestOwnershipStoreAuthenticatesScansAndRemovesExactRecord(t *testing.T) {
	base := t.TempDir()
	owner := uint32(os.Getuid())
	config := validJailerConfig()
	config.ChrootBaseDir = base
	root, err := prepareJailRoot(config, "runtime-machine-1", owner)
	if err != nil {
		t.Fatal(err)
	}
	store := OwnershipStore{Jailer: config, HostID: "runtime-host-1", HostOwnerUID: owner, Key: bytes.Repeat([]byte{0x42}, 32), Random: bytes.NewReader(bytes.Repeat([]byte{0x24}, 16))}
	record := OwnershipRecord{
		SchemaVersion: 1, HostID: store.HostID, TenantID: "20000000-0000-4000-8000-000000000001",
		SessionID: "80000000-0000-4000-8000-000000000001", AllocationID: "80000000-0000-4000-8000-000000000002",
		ProvisionAttemptID: "60000000-0000-4000-8000-000000000001", MachineID: "runtime-machine-1", GuestCID: 42,
		Root: root, Process: validProcessIdentity(), CreatedAt: time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC),
	}
	if err = store.Create(record); err != nil {
		t.Fatal(err)
	}
	if err = store.Create(record); !errors.Is(err, ErrOwnershipConflict) {
		t.Fatalf("duplicate Create() = %v", err)
	}
	got, err := store.Read(record.MachineID)
	if err != nil || got.SessionID != record.SessionID {
		t.Fatalf("Read() = %#v, %v", got, err)
	}
	records, err := store.Scan()
	if err != nil || len(records) != 1 || records[0].MachineID != record.MachineID {
		t.Fatalf("Scan() = %#v, %v", records, err)
	}
	wrong := record
	wrong.SessionID = "80000000-0000-4000-8000-000000000099"
	if err = store.Remove(wrong); !errors.Is(err, ErrOwnershipIntegrity) {
		t.Fatalf("wrong Remove() = %v", err)
	}
	if err = store.Remove(record); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Read(record.MachineID); !errors.Is(err, ErrOwnershipIntegrity) {
		t.Fatalf("removed Read() = %v", err)
	}
}

func TestOwnershipStoreRejectsTamperingAndSymlinks(t *testing.T) {
	base := t.TempDir()
	owner := uint32(os.Getuid())
	config := validJailerConfig()
	config.ChrootBaseDir = base
	root, err := prepareJailRoot(config, "runtime-machine-2", owner)
	if err != nil {
		t.Fatal(err)
	}
	store := OwnershipStore{Jailer: config, HostID: "runtime-host-1", HostOwnerUID: owner, Key: bytes.Repeat([]byte{0x43}, 32), Random: bytes.NewReader(bytes.Repeat([]byte{0x25}, 16))}
	record := OwnershipRecord{SchemaVersion: 1, HostID: store.HostID, TenantID: "tenant", SessionID: "session", AllocationID: "allocation", ProvisionAttemptID: "attempt", MachineID: "runtime-machine-2", GuestCID: 43, Root: root, Process: validProcessIdentity(), CreatedAt: time.Now().UTC()}
	if err = store.Create(record); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(root), ownershipFileName)
	if err = os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Read(record.MachineID); !errors.Is(err, ErrOwnershipIntegrity) {
		t.Fatalf("writable manifest accepted: %v", err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "target")
	if err = os.WriteFile(target, []byte(`{}`), 0o400); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, path); err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skip("sandbox forbids symlinks")
		}
		t.Fatal(err)
	}
	if _, err = store.Read(record.MachineID); !errors.Is(err, ErrOwnershipIntegrity) {
		t.Fatalf("symlink manifest accepted: %v", err)
	}
}

func TestOwnershipCleanupArchivesEvidenceUntilDurableCompletion(t *testing.T) {
	base := t.TempDir()
	owner := uint32(os.Getuid())
	config := validJailerConfig()
	config.ChrootBaseDir = base
	root, err := prepareJailRoot(config, "runtime-machine-3", owner)
	if err != nil {
		t.Fatal(err)
	}
	store := OwnershipStore{Jailer: config, HostID: "runtime-host-1", HostOwnerUID: owner, Key: bytes.Repeat([]byte{0x44}, 32), Random: bytes.NewReader(bytes.Repeat([]byte{0x26}, 16))}
	record := OwnershipRecord{
		SchemaVersion: 1, HostID: store.HostID, TenantID: "tenant", SessionID: "session", AllocationID: "allocation", ProvisionAttemptID: "attempt",
		MachineID: "runtime-machine-3", GuestCID: 44, Root: root, Process: validProcessIdentity(), CreatedAt: time.Now().UTC(),
	}
	if err = store.Create(record); err != nil {
		t.Fatal(err)
	}
	if err = store.Cleanup(record); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Dir(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("machine directory survived cleanup: %v", err)
	}
	prepared, err := store.CleanupPrepared(record)
	if err != nil || !prepared {
		t.Fatalf("CleanupPrepared()=%v err=%v", prepared, err)
	}
	if current, readErr := store.Read(record.MachineID); readErr != nil || current.SessionID != record.SessionID {
		t.Fatalf("archived Read()=%#v err=%v", current, readErr)
	}
	if records, scanErr := store.Scan(); scanErr != nil || len(records) != 1 || records[0].MachineID != record.MachineID {
		t.Fatalf("archived Scan()=%#v err=%v", records, scanErr)
	}
	if err = store.Cleanup(record); err != nil {
		t.Fatalf("idempotent Cleanup()=%v", err)
	}
	if err = store.Remove(record); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Read(record.MachineID); !errors.Is(err, ErrOwnershipIntegrity) {
		t.Fatalf("completed evidence survived: %v", err)
	}
}
