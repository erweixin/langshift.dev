//go:build linux

package firecracker

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

var ErrCgroupRecovery = errors.New("Firecracker cgroup recovery failed")

func KillMachineCgroup(ctx context.Context, config JailerConfig, machineID string, pollInterval time.Duration) error {
	return killMachineCgroup(ctx, config, machineID, pollInterval, cgroupRecoveryOps{
		validateMount: validateCgroup2Mount,
		remove:        os.Remove,
	})
}

type cgroupRecoveryOps struct {
	validateMount func(string) error
	remove        func(string) error
}

func killMachineCgroup(ctx context.Context, config JailerConfig, machineID string, pollInterval time.Duration, operations cgroupRecoveryOps) error {
	if pollInterval <= 0 || pollInterval > time.Second {
		return ErrCgroupRecovery
	}
	path, err := config.CgroupPath(machineID)
	if err != nil {
		return errors.Join(err, ErrCgroupRecovery)
	}
	if _, err = os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil || verifyTrustedDirectory(path, 0) != nil || operations.validateMount == nil || operations.validateMount(path) != nil || operations.remove == nil {
		return ErrCgroupRecovery
	}
	descriptor, err := unix.Open(path+"/cgroup.kill", unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return errors.Join(err, ErrCgroupRecovery)
	}
	file := os.NewFile(uintptr(descriptor), "cgroup.kill")
	if file == nil {
		_ = unix.Close(descriptor)
		return ErrCgroupRecovery
	}
	if _, err = file.Write([]byte("1")); err != nil {
		_ = file.Close()
		return errors.Join(err, ErrCgroupRecovery)
	}
	if err = file.Close(); err != nil {
		return errors.Join(err, ErrCgroupRecovery)
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		events, readErr := readCgroupEvents(path + "/cgroup.events")
		if readErr != nil {
			return errors.Join(readErr, ErrCgroupRecovery)
		}
		if strings.Contains("\n"+events+"\n", "\npopulated 0\n") {
			if removeErr := operations.remove(path); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
				return errors.Join(removeErr, ErrCgroupRecovery)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), ErrCgroupRecovery)
		case <-ticker.C:
		}
	}
}

func validateCgroup2Mount(path string) error {
	var statistics unix.Statfs_t
	if err := unix.Statfs(path, &statistics); err != nil || uint64(statistics.Type) != uint64(unix.CGROUP2_SUPER_MAGIC) {
		return ErrCgroupRecovery
	}
	return nil
}

func readCgroupEvents(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(contents) > 4096 {
		return "", ErrCgroupRecovery
	}
	return strings.TrimSpace(string(contents)), nil
}
