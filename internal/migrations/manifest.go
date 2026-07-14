// Package migrations applies the production database contract with a session
// advisory lock, source checksums, and transactionally recorded versions.
package migrations

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	ErrManifest       = errors.New("migration manifest is invalid")
	ErrChecksum       = errors.New("migration source checksum mismatch")
	ErrState          = errors.New("database migration state is invalid")
	ErrIrreversible   = errors.New("migration is irreversible")
	ErrUntracked      = errors.New("database schema exists without migration history")
	ErrInvalidCommand = errors.New("migration command is invalid")
)

var (
	namePattern   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}$`)
	digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Manifest struct {
	ManifestVersion string      `json:"manifest_version"`
	Migrations      []Migration `json:"migrations"`
}

type Migration struct {
	Version    int64  `json:"version"`
	Name       string `json:"name"`
	Up         string `json:"up"`
	UpSHA256   string `json:"up_sha256"`
	Down       string `json:"down,omitempty"`
	DownSHA256 string `json:"down_sha256,omitempty"`
	Reversible bool   `json:"reversible"`
	upSQL      string
	downSQL    string
}

func LoadManifest(root, manifestPath string) (Manifest, error) {
	rootPath, err := filepath.Abs(root)
	if err != nil {
		return Manifest{}, ErrManifest
	}
	rootPath, err = filepath.EvalSymlinks(rootPath)
	if err != nil {
		return Manifest{}, ErrManifest
	}
	encoded, err := readBoundedFile(rootPath, manifestPath, 64*1024)
	if err != nil {
		return Manifest{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err = decoder.Decode(&manifest); err != nil {
		return Manifest{}, ErrManifest
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || manifest.ManifestVersion != "1.0.0" || len(manifest.Migrations) < 1 || len(manifest.Migrations) > 10000 {
		return Manifest{}, ErrManifest
	}
	for index := range manifest.Migrations {
		migration := &manifest.Migrations[index]
		if migration.Version != int64(index+1) || !namePattern.MatchString(migration.Name) || !digestPattern.MatchString(migration.UpSHA256) || migration.Up == "" || (migration.Reversible && (migration.Down == "" || !digestPattern.MatchString(migration.DownSHA256))) || (!migration.Reversible && (migration.Down != "" || migration.DownSHA256 != "")) {
			return Manifest{}, ErrManifest
		}
		up, readErr := readBoundedFile(rootPath, migration.Up, 16*1024*1024)
		if readErr != nil || digest(up) != migration.UpSHA256 {
			return Manifest{}, ErrChecksum
		}
		migration.upSQL, err = normalizeSQL(up)
		if err != nil {
			return Manifest{}, err
		}
		if migration.Reversible {
			down, readErr := readBoundedFile(rootPath, migration.Down, 16*1024*1024)
			if readErr != nil || digest(down) != migration.DownSHA256 {
				return Manifest{}, ErrChecksum
			}
			migration.downSQL, err = normalizeSQL(down)
			if err != nil {
				return Manifest{}, err
			}
		}
	}
	return manifest, nil
}

func readBoundedFile(root, relative string, maximum int64) ([]byte, error) {
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
		return nil, ErrManifest
	}
	path := filepath.Join(root, relative)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, ErrManifest
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, ErrManifest
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, ErrManifest
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(contents)) > maximum {
		return nil, ErrManifest
	}
	return contents, nil
}

func normalizeSQL(encoded []byte) (string, error) {
	if len(encoded) == 0 || bytes.IndexByte(encoded, 0) >= 0 {
		return "", ErrManifest
	}
	value := strings.TrimSpace(string(encoded))
	if strings.HasPrefix(value, "BEGIN;") && strings.HasSuffix(value, "COMMIT;") {
		value = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(value, "BEGIN;")), "COMMIT;"))
	}
	if value == "" {
		return "", ErrManifest
	}
	for _, line := range strings.Split(value, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "\\") {
			return "", ErrManifest
		}
	}
	return value, nil
}

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
