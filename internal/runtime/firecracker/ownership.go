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
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	ownershipFileName          = "controller.ownership.json"
	ownershipEvidenceDirectory = ".controller-ownership"
	cleanupEvidenceDirectory   = ".controller-cleanup"
	OwnershipPhaseReserved     = "reserved"
	OwnershipPhaseActive       = "active"
)

var (
	ErrOwnershipConfiguration = errors.New("Firecracker ownership store configuration is invalid")
	ErrOwnershipConflict      = errors.New("Firecracker ownership record already exists")
	ErrOwnershipIntegrity     = errors.New("Firecracker ownership record integrity check failed")
)

// OwnershipRecord is host-only evidence binding one durable allocation first
// to a reserved machine identity and then to an exact kernel process. It
// intentionally contains no capability or provision lease material.
type OwnershipRecord struct {
	SchemaVersion      int             `json:"schema_version"`
	Phase              string          `json:"phase,omitempty"`
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
	directory, err := store.ensureEvidenceDirectory(ownershipEvidenceDirectory)
	if err != nil {
		return err
	}
	return store.createFile(filepath.Join(directory, record.MachineID+".json"), record)
}

// ClaimVacant recovers the only cross-system gap: the database reservation
// committed but the host journal did not. It is permitted only when neither a
// jail nor the deterministic cgroup exists, proving host mutation never began.
func (store OwnershipStore) ClaimVacant(record OwnershipRecord) error {
	if record.SchemaVersion != 2 || record.Phase != OwnershipPhaseReserved || record.Process != (ProcessIdentity{}) {
		return ErrOwnershipIntegrity
	}
	machineDirectory := filepath.Dir(record.Root)
	cgroup, err := store.Jailer.CgroupPath(record.MachineID)
	if err != nil {
		return ErrOwnershipIntegrity
	}
	for _, path := range []string{machineDirectory, cgroup} {
		if _, statErr := os.Lstat(path); statErr == nil || !errors.Is(statErr, fs.ErrNotExist) {
			return ErrOwnershipIntegrity
		}
	}
	return store.Create(record)
}

func (store OwnershipStore) Activate(reserved OwnershipRecord, identity ProcessIdentity) (OwnershipRecord, error) {
	if reserved.SchemaVersion != 2 || reserved.Phase != OwnershipPhaseReserved || reserved.Process != (ProcessIdentity{}) || identity.Validate() != nil || store.Verify(reserved) != nil {
		return OwnershipRecord{}, ErrOwnershipIntegrity
	}
	path, archived, err := store.recordPath(reserved.MachineID)
	if err != nil || archived || filepath.Dir(path) != store.evidencePath(ownershipEvidenceDirectory) {
		return OwnershipRecord{}, ErrOwnershipIntegrity
	}
	active := reserved
	active.Phase = OwnershipPhaseActive
	active.Process = identity
	if err = store.replaceFile(path, active); err != nil {
		return OwnershipRecord{}, err
	}
	return active, nil
}

func (store OwnershipStore) Read(machineID string) (OwnershipRecord, error) {
	root, err := store.ExpectedRoot(machineID)
	if err != nil {
		return OwnershipRecord{}, err
	}
	path, _, err := store.recordPath(machineID)
	if err != nil {
		return OwnershipRecord{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return OwnershipRecord{}, ErrOwnershipIntegrity
	}
	return store.readPath(path, machineID, root, info)
}

func (store OwnershipStore) recordPath(machineID string) (string, bool, error) {
	root, err := store.ExpectedRoot(machineID)
	if err != nil {
		return "", false, err
	}
	candidates := []struct {
		path     string
		archived bool
	}{
		{store.activePath(machineID), false},
		{store.cleanupPath(machineID), true},
		{filepath.Join(filepath.Dir(root), ownershipFileName), false}, // schema-v1 compatibility
	}
	var found string
	archived := false
	for _, candidate := range candidates {
		if _, statErr := os.Lstat(candidate.path); statErr == nil {
			if found != "" {
				return "", false, ErrOwnershipIntegrity
			}
			found, archived = candidate.path, candidate.archived
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			return "", false, ErrOwnershipIntegrity
		}
	}
	if found == "" {
		return "", false, ErrOwnershipIntegrity
	}
	return found, archived, nil
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
	if err := verifyTrustedDirectory(store.Jailer.ChrootBaseDir, store.HostOwnerUID); err != nil {
		return nil, ErrOwnershipIntegrity
	}
	base := store.basePath()
	if _, err := os.Stat(base); errors.Is(err, fs.ErrNotExist) {
		return []OwnershipRecord{}, nil
	}
	if err := verifyTrustedDirectory(base, store.HostOwnerUID); err != nil {
		return nil, ErrOwnershipIntegrity
	}
	records := []OwnershipRecord{}
	seen := map[string]struct{}{}
	for _, directory := range []string{ownershipEvidenceDirectory, cleanupEvidenceDirectory} {
		current, err := store.scanEvidenceDirectory(directory, seen)
		if err != nil {
			return nil, err
		}
		records = append(records, current...)
	}
	// Read legacy schema-v1 records from machine directories during a rolling
	// host-agent upgrade. Unjournaled directories are handled only through the
	// database inventory + ClaimVacant proof, never trusted on their own.
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validID(entry.Name()) {
			continue
		}
		path := filepath.Join(base, entry.Name(), ownershipFileName)
		info, statErr := os.Lstat(path)
		if errors.Is(statErr, fs.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return nil, ErrOwnershipIntegrity
		}
		if _, duplicate := seen[entry.Name()]; duplicate {
			return nil, ErrOwnershipIntegrity
		}
		record, readErr := store.readPath(path, entry.Name(), filepath.Join(base, entry.Name(), "root"), info)
		if readErr != nil {
			return nil, readErr
		}
		seen[entry.Name()] = struct{}{}
		records = append(records, record)
	}
	return records, nil
}

func (store OwnershipStore) scanEvidenceDirectory(name string, seen map[string]struct{}) ([]OwnershipRecord, error) {
	directory := store.evidencePath(name)
	entries, err := os.ReadDir(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil || verifyTrustedDirectory(directory, store.HostOwnerUID) != nil {
		return nil, ErrOwnershipIntegrity
	}
	if err = store.recoverTemporaryFiles(directory, entries); err != nil {
		return nil, err
	}
	entries, err = os.ReadDir(directory)
	if err != nil {
		return nil, ErrOwnershipIntegrity
	}
	records := make([]OwnershipRecord, 0, len(entries))
	for _, entry := range entries {
		filename := entry.Name()
		machineID := filename[:len(filename)-len(filepath.Ext(filename))]
		if entry.IsDir() || filepath.Ext(filename) != ".json" || !validID(machineID) {
			return nil, ErrOwnershipIntegrity
		}
		if _, duplicate := seen[machineID]; duplicate {
			return nil, ErrOwnershipIntegrity
		}
		path := filepath.Join(directory, filename)
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return nil, ErrOwnershipIntegrity
		}
		record, readErr := store.readPath(path, machineID, filepath.Join(store.basePath(), machineID, "root"), info)
		if readErr != nil {
			return nil, readErr
		}
		seen[machineID] = struct{}{}
		records = append(records, record)
	}
	return records, nil
}

func (store OwnershipStore) recoverTemporaryFiles(directory string, entries []os.DirEntry) error {
	removed := false
	for _, entry := range entries {
		if !validOwnershipTemporaryName(entry.Name()) {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm() != 0o400 || info.Size() < 0 || info.Size() > 32<<10 {
			return ErrOwnershipIntegrity
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != store.HostOwnerUID || stat.Nlink < 1 || stat.Nlink > 2 {
			return ErrOwnershipIntegrity
		}
		if stat.Nlink == 2 {
			links := 0
			for _, candidate := range entries {
				if candidate.Name() == entry.Name() || filepath.Ext(candidate.Name()) != ".json" {
					continue
				}
				candidateInfo, candidateErr := os.Lstat(filepath.Join(directory, candidate.Name()))
				if candidateErr != nil {
					return ErrOwnershipIntegrity
				}
				if os.SameFile(info, candidateInfo) {
					links++
				}
			}
			if links != 1 {
				return ErrOwnershipIntegrity
			}
		}
		if err = os.Remove(path); err != nil {
			return err
		}
		removed = true
	}
	if removed {
		return syncDirectory(directory)
	}
	return nil
}

func validOwnershipTemporaryName(name string) bool {
	const prefix, suffix = ".ownership-", ".tmp"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return false
	}
	nonce := name[len(prefix) : len(name)-len(suffix)]
	if len(nonce) != 32 {
		return false
	}
	_, err := hex.DecodeString(nonce)
	return err == nil && nonce == strings.ToLower(nonce)
}

func (store OwnershipStore) Remove(record OwnershipRecord) error {
	if err := store.Verify(record); err != nil {
		return ErrOwnershipIntegrity
	}
	path, _, err := store.recordPath(record.MachineID)
	if err != nil {
		return err
	}
	if err = os.Remove(path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
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
	path, archived, err := store.recordPath(record.MachineID)
	if err != nil {
		return err
	}
	if !archived {
		cleanupDirectory, prepareErr := store.ensureEvidenceDirectory(cleanupEvidenceDirectory)
		if prepareErr != nil {
			return prepareErr
		}
		destination := store.cleanupPath(record.MachineID)
		if _, statErr := os.Lstat(destination); statErr == nil || !errors.Is(statErr, fs.ErrNotExist) {
			return ErrOwnershipConflict
		}
		if err = os.Rename(path, destination); err != nil {
			return err
		}
		if err = syncDirectory(filepath.Dir(path)); err != nil {
			return err
		}
		if err = syncDirectory(cleanupDirectory); err != nil {
			return err
		}
	}
	machineDirectory := filepath.Dir(record.Root)
	if err = os.RemoveAll(machineDirectory); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(machineDirectory))
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

func (store OwnershipStore) ExpectedRoot(machineID string) (string, error) {
	if err := store.validate(); err != nil {
		return "", err
	}
	return store.Jailer.JailRoot(machineID)
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
	root, err := store.ExpectedRoot(record.MachineID)
	commonInvalid := err != nil || record.HostID != store.HostID || record.TenantID == "" || record.SessionID == "" || record.AllocationID == "" || record.ProvisionAttemptID == "" || record.GuestCID < 3 || record.Root != root || record.CreatedAt.IsZero() || record.CreatedAt.Location() != time.UTC
	if commonInvalid {
		return ErrOwnershipIntegrity
	}
	switch record.SchemaVersion {
	case 1:
		if record.Phase != "" || record.Process.Validate() != nil {
			return ErrOwnershipIntegrity
		}
	case 2:
		if record.Phase == OwnershipPhaseReserved && record.Process != (ProcessIdentity{}) {
			return ErrOwnershipIntegrity
		}
		if record.Phase == OwnershipPhaseActive && record.Process.Validate() != nil {
			return ErrOwnershipIntegrity
		}
		if record.Phase != OwnershipPhaseReserved && record.Phase != OwnershipPhaseActive {
			return ErrOwnershipIntegrity
		}
	default:
		return ErrOwnershipIntegrity
	}
	return nil
}

func (store OwnershipStore) createFile(destination string, record OwnershipRecord) error {
	if _, err := os.Lstat(destination); err == nil {
		return ErrOwnershipConflict
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	encoded, err := store.encode(record)
	if err != nil {
		return err
	}
	temporary, file, err := store.newTemporary(filepath.Dir(destination))
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
	if err = writeOwnershipFile(file, encoded, store.HostOwnerUID); err != nil {
		return err
	}
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
	return syncDirectory(filepath.Dir(destination))
}

func (store OwnershipStore) replaceFile(destination string, record OwnershipRecord) error {
	encoded, err := store.encode(record)
	if err != nil {
		return err
	}
	temporary, file, err := store.newTemporary(filepath.Dir(destination))
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(temporary)
	}()
	if err = writeOwnershipFile(file, encoded, store.HostOwnerUID); err != nil {
		return err
	}
	if err = os.Rename(temporary, destination); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(destination))
}

func (store OwnershipStore) newTemporary(directory string) (string, *os.File, error) {
	random := store.Random
	if random == nil {
		random = rand.Reader
	}
	nonce := make([]byte, 16)
	if _, err := io.ReadFull(random, nonce); err != nil {
		return "", nil, err
	}
	path := filepath.Join(directory, ".ownership-"+hex.EncodeToString(nonce)+".tmp")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	return path, file, err
}

func writeOwnershipFile(file *os.File, encoded []byte, owner uint32) error {
	if file == nil || file.Chown(int(owner), -1) != nil {
		return ErrOwnershipIntegrity
	}
	if written, err := file.Write(encoded); err != nil || written != len(encoded) {
		return errors.Join(err, ErrOwnershipIntegrity)
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Close()
}

func (store OwnershipStore) ensureEvidenceDirectory(name string) (string, error) {
	if err := verifyTrustedDirectory(store.Jailer.ChrootBaseDir, store.HostOwnerUID); err != nil {
		return "", ErrOwnershipIntegrity
	}
	base := store.basePath()
	for _, directory := range []string{base, filepath.Join(base, name)} {
		if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", err
		}
		if err := os.Chown(directory, int(store.HostOwnerUID), -1); err != nil {
			return "", err
		}
		if err := os.Chmod(directory, 0o700); err != nil || verifyTrustedDirectory(directory, store.HostOwnerUID) != nil {
			return "", ErrOwnershipIntegrity
		}
	}
	if err := syncDirectory(store.Jailer.ChrootBaseDir); err != nil {
		return "", err
	}
	if err := syncDirectory(base); err != nil {
		return "", err
	}
	return filepath.Join(base, name), nil
}

func (store OwnershipStore) basePath() string {
	return filepath.Join(store.Jailer.ChrootBaseDir, filepath.Base(store.Jailer.FirecrackerPath))
}
func (store OwnershipStore) evidencePath(name string) string {
	return filepath.Join(store.basePath(), name)
}
func (store OwnershipStore) activePath(machineID string) string {
	return filepath.Join(store.evidencePath(ownershipEvidenceDirectory), machineID+".json")
}
func (store OwnershipStore) cleanupPath(machineID string) string {
	return filepath.Join(store.evidencePath(cleanupEvidenceDirectory), machineID+".json")
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
