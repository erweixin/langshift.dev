//go:build linux

package firecracker

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func readProcessIdentity(pid int) (ProcessIdentity, error) {
	if pid < 2 {
		return ProcessIdentity{}, ErrProcessIdentity
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ProcessIdentity{}, ErrProcessIdentity
	}
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return ProcessIdentity{}, ErrProcessIdentity
	}
	// comm is parenthesized and may contain spaces or right parentheses. The
	// fields after the final ") " start at proc field 3; starttime is field 22.
	end := strings.LastIndex(string(stat), ") ")
	if end < 0 {
		return ProcessIdentity{}, ErrProcessIdentity
	}
	fields := strings.Fields(string(stat)[end+2:])
	if len(fields) < 20 {
		return ProcessIdentity{}, ErrProcessIdentity
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || start == 0 {
		return ProcessIdentity{}, ErrProcessIdentity
	}
	executable, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	if err != nil {
		return ProcessIdentity{}, ErrProcessIdentity
	}
	executable = strings.TrimSuffix(executable, " (deleted)")
	identity := ProcessIdentity{PID: pid, StartTicks: start, BootID: strings.TrimSpace(string(boot)), Executable: executable}
	return identity, identity.Validate()
}
