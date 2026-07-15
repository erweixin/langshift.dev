//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package firecracker

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const ownershipFileName = "controller.ownership.json"

var (
	ErrOwnershipConfiguration = errors.New("Firecracker ownership store configuration is invalid")
	ErrOwnershipConflict      = errors.New("Firecracker ownership record already exists")
	ErrOwnershipIntegrity     = errors.New("Firecracker ownership record integrity check failed")
)

// OwnershipRecord is host-only evidence binding one durable allocation to an
// exact kernel process identity. It intentionally contains no capability or
// provision lease material.
type OwnershipRecord struct {
	SchemaVersion      int             `json:"schema_version"`
	HostID             string          `json:"host_id"`
	TenantID           string          `json:"tenant_id"`
	SessionID          string          `json:"session_id"`
	AllocationID       string          `json:"allocation_id"`
	ProvisionAttemptID string          `json:"provision_attempt_id"`
	MachineID          string          `json:"machine_id"`
	GuestCID           uint32          `json:"guest_cid"`
	Root               string          `json:"root"`
	Process            ProcessIdentity `json:"process"`
	CreatedAt          time.Time       `json:"created_at"`
}

type ownershipEnvelope struct {
	Record OwnershipRecord `json:"record"`
	MAC    string          `json:"mac"`
}

type OwnershipStore struct {
	Jailer       JailerConfig
	HostID       string
	Key          []byte
	HostOwnerUID uint32
	Random       io.Reader
}

func (store OwnershipStore) Create(record OwnershipRecord) error {
	if err := store.validateRecord(record); err != nil {
		return err
	}
	machineDirectory := filepath.Dir(record.Root)
	if err := verifyTrustedDirectory(machineDirectory, store.HostOwnerUID); err != nil {
		return ErrOwnershipIntegrity
	}
	encoded, err := store.encode(record)
	if err != nil {
		return err
	}
	destination := filepath.Join(machineDirectory, ownershipFileName)
	if _, err = os.Lstat(destination); err == nil {
		return ErrOwnershipConflict
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	random := store.Random
	if random == nil {
		random = rand.Reader
	}
	nonce := make([]byte, 16)
	if _, err = io.ReadFull(random, nonce); err != nil {
		return err
	}
	temporary := filepath.Join(machineDirectory, ".ownership-"+hex.EncodeToString(nonce)+".tmp")
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return err
	}
	removeTemporary := true
	defer func() {
		_ = file.Close()
		if removeTemporary {
			_ = os.Remove(temporary)
		}
	}()
	if err = file.Chown(int(store.HostOwnerUID), -1); err != nil {
		return err
	}
	if written, writeErr := file.Write(encoded); writeErr != nil || written != len(encoded) {
		return errors.Join(writeErr, ErrOwnershipIntegrity)
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	// A hard link gives create-if-absent semantics on the same filesystem;
	// unlike Rename it cannot replace another controller's record.
	if err = os.Link(temporary, destination); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrOwnershipConflict
		}
		return err
	}
	if err = os.Remove(temporary); err != nil {
		_ = os.Remove(destination)
		return err
	}
	removeTemporary = false
	return syncDirectory(machineDirectory)
}

func (store OwnershipStore) Read(machineID string) (OwnershipRecord, error) {
	root, err := store.expectedRoot(machineID)
	if err != nil {
		return OwnershipRecord{}, err
	}
	path := filepath.Join(filepath.Dir(root), ownershipFileName)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm() != 0o400 || info.Size() < 1 || info.Size() > 32<<10 {
		return OwnershipRecord{}, ErrOwnershipIntegrity
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != store.HostOwnerUID || stat.Nlink != 1 {
		return OwnershipRecord{}, ErrOwnershipIntegrity
	}
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return OwnershipRecord{}, ErrOwnershipIntegrity
	}
	file := os.NewFile(uintptr(descriptor), ownershipFileName)
	if file == nil {
		_ = unix.Close(descriptor)
		return OwnershipRecord{}, ErrOwnershipIntegrity
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(info, after) {
		return OwnershipRecord{}, ErrOwnershipIntegrity
	}
	encoded, err := io.ReadAll(io.LimitReader(file, (32<<10)+1))
	if err != nil || len(encoded) > 32<<10 {
		return OwnershipRecord{}, ErrOwnershipIntegrity
	}
	record, err := store.decode(encoded)
	if err != nil || record.MachineID != machineID || record.Root != root {
		return OwnershipRecord{}, ErrOwnershipIntegrity
	}
	return record, nil
}

func (store OwnershipStore) Scan() ([]OwnershipRecord, error) {
	if err := store.validate(); err != nil {
		return nil, err
	}
	base := filepath.Join(store.Jailer.ChrootBaseDir, filepath.Base(store.Jailer.FirecrackerPath))
	if err := verifyTrustedDirectory(store.Jailer.ChrootBaseDir, store.HostOwnerUID); err != nil {
		return nil, ErrOwnershipIntegrity
	}
	if _, err := os.Stat(base); errors.Is(err, fs.ErrNotExist) {
		return []OwnershipRecord{}, nil
	}
	if err := verifyTrustedDirectory(base, store.HostOwnerUID); err != nil {
		return nil, ErrOwnershipIntegrity
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, err
	}
	records := make([]OwnershipRecord, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !validID(entry.Name()) {
			continue
		}
		record, readErr := store.Read(entry.Name())
		if readErr != nil {
			return nil, readErr
		}
		records = append(records, record)
	}
	return records, nil
}

func (store OwnershipStore) Remove(record OwnershipRecord) error {
	if err := store.Verify(record); err != nil {
		return ErrOwnershipIntegrity
	}
	directory := filepath.Dir(record.Root)
	if err := os.Remove(filepath.Join(directory, ownershipFileName)); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func (store OwnershipStore) Verify(record OwnershipRecord) error {
	current, err := store.Read(record.MachineID)
	if err != nil || !reflect.DeepEqual(current, record) {
		return ErrOwnershipIntegrity
	}
	return nil
}

func (store OwnershipStore) validate() error {
	if store.HostID == "" || len(store.HostID) > 128 || len(store.Key) < 32 || store.Jailer.ChrootBaseDir == "" || store.Jailer.FirecrackerPath == "" {
		return ErrOwnershipConfiguration
	}
	return nil
}

func (store OwnershipStore) validateRecord(record OwnershipRecord) error {
	if err := store.validate(); err != nil {
		return err
	}
	root, err := store.expectedRoot(record.MachineID)
	if err != nil || record.SchemaVersion != 1 || record.HostID != store.HostID || record.TenantID == "" || record.SessionID == "" || record.AllocationID == "" || record.ProvisionAttemptID == "" || record.GuestCID < 3 || record.Root != root || record.CreatedAt.IsZero() || record.CreatedAt.Location() != time.UTC || record.Process.Validate() != nil {
		return ErrOwnershipIntegrity
	}
	return nil
}

func (store OwnershipStore) expectedRoot(machineID string) (string, error) {
	if err := store.validate(); err != nil {
		return "", err
	}
	return store.Jailer.JailRoot(machineID)
}

func (store OwnershipStore) encode(record OwnershipRecord) ([]byte, error) {
	recordBytes, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, store.Key)
	_, _ = mac.Write(recordBytes)
	return json.Marshal(ownershipEnvelope{Record: record, MAC: base64.RawURLEncoding.EncodeToString(mac.Sum(nil))})
}

func (store OwnershipStore) decode(encoded []byte) (OwnershipRecord, error) {
	var envelope ownershipEnvelope
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(&struct{}{}) != io.EOF || envelope.MAC == "" {
		return OwnershipRecord{}, ErrOwnershipIntegrity
	}
	recordBytes, err := json.Marshal(envelope.Record)
	if err != nil {
		return OwnershipRecord{}, ErrOwnershipIntegrity
	}
	provided, err := base64.RawURLEncoding.DecodeString(envelope.MAC)
	mac := hmac.New(sha256.New, store.Key)
	_, _ = mac.Write(recordBytes)
	if err != nil || !hmac.Equal(provided, mac.Sum(nil)) || store.validateRecord(envelope.Record) != nil {
		return OwnershipRecord{}, ErrOwnershipIntegrity
	}
	return envelope.Record, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
