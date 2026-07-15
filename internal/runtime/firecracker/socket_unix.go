//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package firecracker

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

func secureSocketReady(path string, expectedUID, expectedGID uint32) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o077 != 0 || stat.Uid != expectedUID || stat.Gid != expectedGID {
		return false, ErrUntrustedHostPath
	}
	return true, nil
}
