//go:build linux

package firecracker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestKillMachineCgroupUsesDeterministicV2KillAndWaitsForEmpty(t *testing.T) {
	base := t.TempDir()
	config := validJailerConfig()
	config.CgroupBaseDir = base
	path, err := config.CgroupPath("runtime-cgroup-1")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(path, "cgroup.kill"), []byte("0"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(path, "cgroup.events"), []byte("populated 0\nfrozen 0\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	removed := false
	if err = killMachineCgroup(context.Background(), config, "runtime-cgroup-1", time.Millisecond, cgroupRecoveryOps{
		validateMount: func(string) error { return nil },
		remove:        func(string) error { removed = true; return nil },
	}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(path, "cgroup.kill"))
	if err != nil || len(contents) == 0 || contents[0] != '1' {
		t.Fatalf("cgroup.kill=%q err=%v", contents, err)
	}
	if !removed {
		t.Fatal("empty machine cgroup was not removed")
	}
}

func TestKillMachineCgroupRejectsLookalikeOutsideCgroup2(t *testing.T) {
	base := t.TempDir()
	config := validJailerConfig()
	config.CgroupBaseDir = base
	path, err := config.CgroupPath("runtime-cgroup-2")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = KillMachineCgroup(context.Background(), config, "runtime-cgroup-2", time.Millisecond); !errors.Is(err, ErrCgroupRecovery) {
		t.Fatalf("lookalike cgroup filesystem accepted: %v", err)
	}
}
