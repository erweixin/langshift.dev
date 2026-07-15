package firecracker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStagerVerifiesContentAndCreatesExclusiveJailedAssets(t *testing.T) {
	directory := t.TempDir()
	kernel := writeAsset(t, directory, "kernel", strings.Repeat("k", 1024))
	rootfs := writeAsset(t, directory, "rootfs", strings.Repeat("r", 2048))
	scratch := writeAsset(t, directory, "scratch", strings.Repeat("s", 1024))
	config := validJailerConfig()
	config.ChrootBaseDir = filepath.Join(directory, "jailer")
	config.UID, config.GID = uint32(os.Getuid()), uint32(os.Getgid())
	if err := os.Mkdir(config.ChrootBaseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stager := Stager{
		Jailer: config, Kernel: kernel, RootFS: rootfs, Scratch: scratch, HostOwnerUID: uint32(os.Getuid()), SourceOwnerUID: uint32(os.Getuid()),
		MaximumKernelBytes: 1 << 20, MaximumRootFSBytes: 64 << 20, MaximumScratchBytes: 1 << 20,
	}
	request := StageRequest{MachineID: "runtime-stage-1", GuestCID: 42, KernelDigest: kernel.Digest, RootFSDigest: rootfs.Digest, VCPUCount: 2, MemoryMiB: 512, DiskMiB: 64, ScratchRate: validSpec().ScratchLimit}
	machine, err := stager.Stage(context.Background(), request)
	if err != nil || machine.Spec.RootDrivePath != "/drives/rootfs.ext4" || machine.Spec.VSockPath != "/run/guest.vsock" {
		t.Fatalf("Stage() = %#v, %v", machine, err)
	}
	for path, size := range map[string]int64{
		filepath.Join(machine.Root, "kernel", "vmlinux"):      1024,
		filepath.Join(machine.Root, "drives", "rootfs.ext4"):  2048,
		filepath.Join(machine.Root, "drives", "scratch.ext4"): 64 << 20,
	} {
		info, statErr := os.Stat(path)
		if statErr != nil || info.Size() != size {
			t.Fatalf("staged %s = %v, %v", path, info, statErr)
		}
	}
	if _, err = stager.Stage(context.Background(), request); !errors.Is(err, ErrStageConflict) {
		t.Fatalf("duplicate Stage() = %v", err)
	}
	if err = machine.Cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestStagerRejectsDigestDriftAndSymlinkSources(t *testing.T) {
	directory := t.TempDir()
	kernel := writeAsset(t, directory, "kernel", strings.Repeat("k", 1024))
	rootfs := writeAsset(t, directory, "rootfs", strings.Repeat("r", 2048))
	scratch := writeAsset(t, directory, "scratch", strings.Repeat("s", 1024))
	config := validJailerConfig()
	config.ChrootBaseDir = filepath.Join(directory, "jailer")
	config.UID, config.GID = uint32(os.Getuid()), uint32(os.Getgid())
	if err := os.Mkdir(config.ChrootBaseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stager := Stager{Jailer: config, Kernel: kernel, RootFS: rootfs, Scratch: scratch, HostOwnerUID: uint32(os.Getuid()), SourceOwnerUID: uint32(os.Getuid()), MaximumKernelBytes: 1 << 20, MaximumRootFSBytes: 64 << 20, MaximumScratchBytes: 1 << 20}
	request := StageRequest{MachineID: "runtime-stage-2", GuestCID: 43, KernelDigest: kernel.Digest, RootFSDigest: rootfs.Digest, VCPUCount: 2, MemoryMiB: 512, DiskMiB: 64, ScratchRate: validSpec().ScratchLimit}
	stager.Kernel.Digest = "sha256:" + strings.Repeat("0", 64)
	if _, err := stager.Stage(context.Background(), request); !errors.Is(err, ErrAssetIntegrity) {
		t.Fatalf("digest drift = %v", err)
	}
	stager.Kernel = kernel
	link := filepath.Join(directory, "kernel-link")
	if err := os.Symlink(kernel.Path, link); err != nil {
		t.Fatal(err)
	}
	stager.Kernel.Path = link
	if _, err := stager.Stage(context.Background(), request); !errors.Is(err, ErrAssetIntegrity) {
		t.Fatalf("symlink source = %v", err)
	}
}

func writeAsset(t *testing.T, directory, name, contents string) Asset {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(contents), 0o400); err != nil {
		t.Fatal(err)
	}
	digest, err := AssetDigest(path)
	if err != nil {
		t.Fatal(err)
	}
	return Asset{Path: path, Digest: digest}
}
