package contentcatalog

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadRepositoryRelease(t *testing.T) {
	release, err := Load(repositoryRelease(t))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := len(release.Transitions.Transitions), 30; got != want {
		t.Fatalf("transition count = %d, want %d", got, want)
	}
	if got, want := len(release.Tasks.Tasks), 60; got != want {
		t.Fatalf("task count = %d, want %d", got, want)
	}
	if release.Identity() != "1.0.0:4fe31c6be2c879be4a40591e9cb6daf90bd2dcacc48a2b74385080767862cebc" {
		t.Fatalf("unexpected identity %q", release.Identity())
	}
}

func TestLoadRejectsRelativeDirectory(t *testing.T) {
	if _, err := Load("product-content/releases/1.0.0"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Load() error = %v, want ErrInvalid", err)
	}
}

func TestLoadRejectsTamperedContent(t *testing.T) {
	directory := copyRelease(t)
	path := filepath.Join(directory, "task-templates.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body[100] ^= 1
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(directory); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Load() error = %v, want ErrInvalid", err)
	}
}

func TestLoadRejectsUnknownManifestField(t *testing.T) {
	directory := copyRelease(t)
	path := filepath.Join(directory, "manifest.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body = append(body[:len(body)-2], []byte(",\n  \"unexpected\": true\n}\n")...)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(directory); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Load() error = %v, want ErrInvalid", err)
	}
}

func TestLoadRejectsSymlinkedArtifact(t *testing.T) {
	directory := copyRelease(t)
	path := filepath.Join(directory, "rubric-versions.json")
	target := filepath.Join(directory, "rubric-target.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(directory); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Load() error = %v, want ErrInvalid", err)
	}
}

func repositoryRelease(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "product-content", "releases", "1.0.0"))
}

func copyRelease(t *testing.T) string {
	t.Helper()
	source := repositoryRelease(t)
	target := filepath.Join(t.TempDir(), "release")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, entry.Name()), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return target
}
