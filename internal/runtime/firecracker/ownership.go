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

const (
	ownershipFileName        = "controller.ownership.json"
	cleanupEvidenceDirectory = ".controller-cleanup"
)

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
	active := filepath.Join(filepath.Dir(root), ownershipFileName)
	archived := store.cleanupPath(machineID)
	activeInfo, activeErr := os.Lstat(active)
	archivedInfo, archivedErr := os.Lstat(archived)
	if (activeErr == nil && archivedErr == nil) || (activeErr != nil && !errors.Is(activeErr, fs.ErrNotExist)) || (archivedErr != nil && !errors.Is(archivedErr, fs.ErrNotExist)) {
		return OwnershipRecord{}, ErrOwnershipIntegrity
	}
	path := active
	if archivedErr == nil {
		path = archived
	} else if activeErr != nil {
		return OwnershipRecord{}, ErrOwnershipIntegrity
	}
	info := activeInfo
	if path == archived {
		info = archivedInfo
	}
	return store.readPath(path, machineID, root, info)
}

func (store OwnershipStore) readPath(path, machineID, root string, info os.FileInfo) (OwnershipRecord, error) {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm() != 0o400 || info.Size() < 1 || info.Size() > 32<<10 {
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
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.Name() == cleanupEvidenceDirectory || !entry.IsDir() || !validID(entry.Name()) {
			continue
		}
		path := filepath.Join(base, entry.Name(), ownershipFileName)
		info, statErr := os.Lstat(path)
		if errors.Is(statErr, fs.ErrNotExist) {
			return nil, ErrOwnershipIntegrity
		}
		record, readErr := store.readPath(path, entry.Name(), filepath.Join(base, entry.Name(), "root"), info)
		if readErr != nil {
			return nil, readErr
		}
		seen[record.MachineID] = struct{}{}
		records = append(records, record)
	}
	cleanupDirectory := filepath.Join(base, cleanupEvidenceDirectory)
	cleanupEntries, cleanupErr := os.ReadDir(cleanupDirectory)
	if cleanupErr != nil && !errors.Is(cleanupErr, fs.ErrNotExist) {
		return nil, cleanupErr
	}
	if cleanupErr == nil {
		if err := verifyTrustedDirectory(cleanupDirectory, store.HostOwnerUID); err != nil {
			return nil, ErrOwnershipIntegrity
		}
		for _, entry := range cleanupEntries {
			name := entry.Name()
			machineID := name[:len(name)-len(filepath.Ext(name))]
			if entry.IsDir() || filepath.Ext(name) != ".json" || !validID(machineID) {
				return nil, ErrOwnershipIntegrity
			}
			if _, duplicate := seen[machineID]; duplicate {
				return nil, ErrOwnershipIntegrity
			}
			path := filepath.Join(cleanupDirectory, name)
			info, statErr := os.Lstat(path)
			if statErr != nil {
				return nil, ErrOwnershipIntegrity
			}
			record, readErr := store.readPath(path, machineID, filepath.Join(base, machineID, "root"), info)
			if readErr != nil {
				return nil, readErr
			}
			seen[machineID] = struct{}{}
			records = append(records, record)
		}
	}
	return records, nil
}

func (store OwnershipStore) Remove(record OwnershipRecord) error {
	if err := store.Verify(record); err != nil {
		return ErrOwnershipIntegrity
	}
	active := filepath.Join(filepath.Dir(record.Root), ownershipFileName)
	archived := store.cleanupPath(record.MachineID)
	path := active
	parent := filepath.Dir(record.Root)
	if _, err := os.Lstat(archived); err == nil {
		path = archived
		parent = filepath.Dir(archived)
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDirectory(parent)
}

func (store OwnershipStore) Verify(record OwnershipRecord) error {
	current, err := store.Read(record.MachineID)
	if err != nil || !reflect.DeepEqual(current, record) {
		return ErrOwnershipIntegrity
	}
	return nil
}

func (store OwnershipStore) Cleanup(record OwnershipRecord) error {
	if err := store.Verify(record); err != nil {
		return err
	}
	directory := filepath.Dir(record.Root)
	archived := store.cleanupPath(record.MachineID)
	if _, err := os.Lstat(archived); errors.Is(err, fs.ErrNotExist) {
		cleanupDirectory, prepareErr := store.ensureCleanupDirectory()
		if prepareErr != nil {
			return prepareErr
		}
		active := filepath.Join(directory, ownershipFileName)
		if err = os.Rename(active, archived); err != nil {
			return err
		}
		if err = syncDirectory(cleanupDirectory); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := os.RemoveAll(directory); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(directory))
}

func (store OwnershipStore) CleanupPrepared(record OwnershipRecord) (bool, error) {
	info, err := os.Lstat(store.cleanupPath(record.MachineID))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	current, err := store.readPath(store.cleanupPath(record.MachineID), record.MachineID, record.Root, info)
	if err != nil || !reflect.DeepEqual(current, record) {
		return false, ErrOwnershipIntegrity
	}
	return true, nil
}

func (store OwnershipStore) cleanupPath(machineID string) string {
	base := filepath.Join(store.Jailer.ChrootBaseDir, filepath.Base(store.Jailer.FirecrackerPath))
	return filepath.Join(base, cleanupEvidenceDirectory, machineID+".json")
}

func (store OwnershipStore) ensureCleanupDirectory() (string, error) {
	base := filepath.Join(store.Jailer.ChrootBaseDir, filepath.Base(store.Jailer.FirecrackerPath))
	if err := verifyTrustedDirectory(base, store.HostOwnerUID); err != nil {
		return "", ErrOwnershipIntegrity
	}
	directory := filepath.Join(base, cleanupEvidenceDirectory)
	if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	if err := os.Chown(directory, int(store.HostOwnerUID), -1); err != nil {
		return "", err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return "", err
	}
	if err := verifyTrustedDirectory(directory, store.HostOwnerUID); err != nil {
		return "", ErrOwnershipIntegrity
	}
	if err := syncDirectory(base); err != nil {
		return "", err
	}
	return directory, nil
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
