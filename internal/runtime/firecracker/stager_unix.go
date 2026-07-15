//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

var (
	ErrAssetIntegrity = errors.New("Firecracker asset integrity verification failed")
	ErrStageConflict  = errors.New("Firecracker machine staging path already exists")
)

type Asset struct {
	Path   string
	Digest string
}

type Stager struct {
	Jailer                  JailerConfig
	Kernel, RootFS, Scratch Asset
	HostOwnerUID            uint32
	SourceOwnerUID          uint32
	MaximumKernelBytes      int64
	MaximumRootFSBytes      int64
	MaximumScratchBytes     int64
}

type StageRequest struct {
	MachineID    string
	GuestCID     uint32
	KernelDigest string
	RootFSDigest string
	VCPUCount    int
	MemoryMiB    int
	DiskMiB      int
	ScratchRate  RateLimit
}

type StagedMachine struct {
	Root string
	Spec Spec
}

func (stager Stager) Stage(ctx context.Context, request StageRequest) (StagedMachine, error) {
	if stager.MaximumKernelBytes < 1<<20 || stager.MaximumKernelBytes > 1<<30 || stager.MaximumRootFSBytes < 64<<20 || stager.MaximumRootFSBytes > 64<<30 || stager.MaximumScratchBytes < 1<<20 || stager.MaximumScratchBytes > 1<<30 || request.DiskMiB < 64 || request.DiskMiB > 262144 || !validAsset(stager.Kernel) || !validAsset(stager.RootFS) || !validAsset(stager.Scratch) {
		return StagedMachine{}, ErrInvalidSpec
	}
	if request.KernelDigest != stager.Kernel.Digest || request.RootFSDigest != stager.RootFS.Digest {
		return StagedMachine{}, ErrAssetIntegrity
	}
	if _, err := stager.Jailer.Arguments(request.MachineID); err != nil {
		return StagedMachine{}, err
	}
	inspector := stager.Jailer.PathInspector
	if inspector == nil {
		inspector = OSHostPathInspector{}
	}
	if err := stager.Jailer.VerifyHostPaths(inspector); err != nil {
		return StagedMachine{}, err
	}
	root, err := prepareJailRoot(stager.Jailer, request.MachineID, stager.HostOwnerUID)
	if err != nil {
		return StagedMachine{}, err
	}
	failed := true
	defer func() {
		if failed {
			_ = os.RemoveAll(filepath.Dir(root))
		}
	}()
	for _, directory := range []string{"kernel", "drives", "run"} {
		path := filepath.Join(root, directory)
		if err = os.Mkdir(path, 0o700); err != nil || os.Chown(path, int(stager.Jailer.UID), int(stager.Jailer.GID)) != nil {
			return StagedMachine{}, ErrAssetIntegrity
		}
	}
	if err = os.Chown(root, int(stager.Jailer.UID), int(stager.Jailer.GID)); err != nil {
		return StagedMachine{}, ErrAssetIntegrity
	}
	targets := []struct {
		asset   Asset
		path    string
		maximum int64
		mode    fs.FileMode
	}{
		{asset: stager.Kernel, path: filepath.Join(root, "kernel", "vmlinux"), maximum: stager.MaximumKernelBytes, mode: 0o400},
		{asset: stager.RootFS, path: filepath.Join(root, "drives", "rootfs.ext4"), maximum: stager.MaximumRootFSBytes, mode: 0o400},
		{asset: stager.Scratch, path: filepath.Join(root, "drives", "scratch.ext4"), maximum: stager.MaximumScratchBytes, mode: 0o600},
	}
	for _, target := range targets {
		if err = copyVerifiedAsset(ctx, target.asset, target.path, target.maximum, target.mode, stager.SourceOwnerUID, stager.Jailer.UID, stager.Jailer.GID); err != nil {
			return StagedMachine{}, err
		}
	}
	scratchPath := targets[2].path
	requestedDiskBytes := int64(request.DiskMiB) << 20
	info, err := os.Stat(scratchPath)
	if err != nil || info.Size() > requestedDiskBytes || os.Truncate(scratchPath, requestedDiskBytes) != nil {
		return StagedMachine{}, ErrAssetIntegrity
	}
	spec := Spec{
		MachineID: request.MachineID, KernelImagePath: "/kernel/vmlinux", RootDrivePath: "/drives/rootfs.ext4",
		ScratchPath: "/drives/scratch.ext4", VSockPath: "/run/guest.vsock", GuestCID: request.GuestCID,
		VCPUCount: request.VCPUCount, MemoryMiB: request.MemoryMiB, ScratchLimit: request.ScratchRate,
	}
	if err = spec.Validate(); err != nil {
		return StagedMachine{}, err
	}
	failed = false
	return StagedMachine{Root: root, Spec: spec}, nil
}

func (machine StagedMachine) Cleanup() error {
	if machine.Root == "" || !filepath.IsAbs(machine.Root) || filepath.Clean(machine.Root) != machine.Root || filepath.Base(machine.Root) != "root" || !validID(machine.Spec.MachineID) || filepath.Base(filepath.Dir(machine.Root)) != machine.Spec.MachineID {
		return ErrInvalidSpec
	}
	machineDirectory := filepath.Dir(machine.Root)
	if err := os.RemoveAll(machineDirectory); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(machineDirectory))
}

func prepareJailRoot(config JailerConfig, machineID string, ownerUID uint32) (string, error) {
	root, err := config.JailRoot(machineID)
	if err != nil {
		return "", err
	}
	if err = verifyTrustedDirectory(config.ChrootBaseDir, ownerUID); err != nil {
		return "", err
	}
	executableDirectory := filepath.Join(config.ChrootBaseDir, filepath.Base(config.FirecrackerPath))
	if err = os.Mkdir(executableDirectory, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	if err = verifyTrustedDirectory(executableDirectory, ownerUID); err != nil {
		return "", err
	}
	machineDirectory := filepath.Dir(root)
	if err = os.Mkdir(machineDirectory, 0o700); errors.Is(err, fs.ErrExist) {
		return "", ErrStageConflict
	} else if err != nil {
		return "", err
	}
	if err = verifyTrustedDirectory(machineDirectory, ownerUID); err != nil {
		_ = os.Remove(machineDirectory)
		return "", err
	}
	if err = os.Mkdir(root, 0o700); err != nil {
		_ = os.Remove(machineDirectory)
		return "", err
	}
	return root, nil
}

func verifyTrustedDirectory(path string, ownerUID uint32) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsDir() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return ErrUntrustedHostPath
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != ownerUID {
		return ErrUntrustedHostPath
	}
	return nil
}

func copyVerifiedAsset(ctx context.Context, asset Asset, destination string, maximum int64, mode fs.FileMode, sourceOwner, targetUID, targetGID uint32) error {
	info, err := os.Lstat(asset.Path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 || info.Size() < 1 || info.Size() > maximum {
		return ErrAssetIntegrity
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != sourceOwner {
		return ErrAssetIntegrity
	}
	descriptor, err := unix.Open(asset.Path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return ErrAssetIntegrity
	}
	source := os.NewFile(uintptr(descriptor), filepath.Base(asset.Path))
	if source == nil {
		_ = unix.Close(descriptor)
		return ErrAssetIntegrity
	}
	defer source.Close()
	afterOpen, err := source.Stat()
	if err != nil {
		return ErrAssetIntegrity
	}
	afterStat, statOK := afterOpen.Sys().(*syscall.Stat_t)
	if !os.SameFile(info, afterOpen) || !afterOpen.Mode().IsRegular() || afterOpen.Mode().Perm()&0o022 != 0 || !statOK || afterStat.Uid != sourceOwner {
		return ErrAssetIntegrity
	}
	destinationFile, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return ErrAssetIntegrity
	}
	removeDestination := true
	defer func() {
		_ = destinationFile.Close()
		if removeDestination {
			_ = os.Remove(destination)
		}
	}()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(destinationFile, hash), io.LimitReader(contextReader{ctx: ctx, reader: source}, maximum+1))
	if err != nil || written != info.Size() || written > maximum || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != asset.Digest || destinationFile.Sync() != nil || destinationFile.Chmod(mode) != nil || destinationFile.Chown(int(targetUID), int(targetGID)) != nil || destinationFile.Close() != nil {
		return ErrAssetIntegrity
	}
	removeDestination = false
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(value []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(value)
}

func validAsset(asset Asset) bool {
	if !trustedAbsolute(asset.Path) || len(asset.Digest) != len("sha256:")+64 || asset.Digest[:len("sha256:")] != "sha256:" {
		return false
	}
	_, err := hex.DecodeString(asset.Digest[len("sha256:"):])
	return err == nil
}

func AssetDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err = io.Copy(digest, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", digest.Sum(nil)), nil
}
