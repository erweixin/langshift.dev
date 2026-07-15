//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package firecracker

import (
	"fmt"
	"os"
	"syscall"
)

type OSHostPathInspector struct{}

func (OSHostPathInspector) Lstat(path string) (HostPathInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return HostPathInfo{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return HostPathInfo{}, fmt.Errorf("unsupported host stat metadata")
	}
	return HostPathInfo{Mode: info.Mode(), UID: stat.Uid}, nil
}
