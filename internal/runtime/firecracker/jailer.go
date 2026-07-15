package firecracker

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type JailerConfig struct {
	JailerPath, FirecrackerPath, ChrootBaseDir string
	CgroupBaseDir                              string
	UID, GID                                   uint32
	ParentCgroup                               string
	CPUQuotaMicros, CPUPeriodMicros            int64
	MemoryMaxBytes                             int64
	PidsMax                                    int64
	FileSizeMaxBytes                           int64
	NoFileMax                                  int64
	NetworkNamespace                           string
	PathInspector                              HostPathInspector
}

func (config JailerConfig) Command(ctx context.Context, machineID string) (*exec.Cmd, error) {
	args, err := config.Arguments(machineID)
	if err != nil {
		return nil, err
	}
	inspector := config.PathInspector
	if inspector == nil {
		inspector = OSHostPathInspector{}
	}
	if err = config.VerifyHostPaths(inspector); err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, config.JailerPath, args...)
	command.Env = []string{}
	return command, nil
}

func (config JailerConfig) Arguments(machineID string) ([]string, error) {
	if !validID(machineID) || !trustedAbsolute(config.JailerPath) || !trustedAbsolute(config.FirecrackerPath) || !trustedAbsolute(config.ChrootBaseDir) || config.CgroupBaseDir != "" && !trustedAbsolute(config.CgroupBaseDir) || config.JailerPath == config.FirecrackerPath || config.UID == 0 || config.GID == 0 || !validCgroupName(config.ParentCgroup) || config.CPUQuotaMicros < 1_000 || config.CPUPeriodMicros < 1_000 || config.CPUQuotaMicros > config.CPUPeriodMicros*32 || config.MemoryMaxBytes < 128<<20 || config.MemoryMaxBytes > 64<<30 || config.PidsMax < 1 || config.PidsMax > 4096 || config.FileSizeMaxBytes < 1<<20 || config.FileSizeMaxBytes > 1<<40 || config.NoFileMax < 64 || config.NoFileMax > 65536 || config.NetworkNamespace != "" && !trustedAbsolute(config.NetworkNamespace) {
		return nil, ErrInvalidSpec
	}
	args := []string{
		"--id", machineID,
		"--exec-file", config.FirecrackerPath,
		"--uid", strconv.FormatUint(uint64(config.UID), 10),
		"--gid", strconv.FormatUint(uint64(config.GID), 10),
		"--cgroup-version", "2",
		"--parent-cgroup", config.ParentCgroup,
		"--cgroup", fmt.Sprintf("cpu.max=%d %d", config.CPUQuotaMicros, config.CPUPeriodMicros),
		"--cgroup", fmt.Sprintf("memory.max=%d", config.MemoryMaxBytes),
		"--cgroup", "memory.swap.max=0",
		"--cgroup", fmt.Sprintf("pids.max=%d", config.PidsMax),
		"--resource-limit", fmt.Sprintf("fsize=%d", config.FileSizeMaxBytes),
		"--resource-limit", fmt.Sprintf("no-file=%d", config.NoFileMax),
		"--chroot-base-dir", config.ChrootBaseDir,
		"--new-pid-ns",
	}
	if config.NetworkNamespace != "" {
		args = append(args, "--netns", config.NetworkNamespace)
	}
	args = append(args, "--", "--api-sock", "/run/firecracker.socket", "--http-api-max-payload-size", "4096")
	return args, nil
}

func (config JailerConfig) CgroupPath(machineID string) (string, error) {
	if _, err := config.Arguments(machineID); err != nil {
		return "", err
	}
	base := config.CgroupBaseDir
	if base == "" {
		base = "/sys/fs/cgroup"
	}
	return filepath.Join(base, filepath.FromSlash(config.ParentCgroup), machineID), nil
}

func (config JailerConfig) JailRoot(machineID string) (string, error) {
	if _, err := config.Arguments(machineID); err != nil {
		return "", err
	}
	return filepath.Join(config.ChrootBaseDir, filepath.Base(config.FirecrackerPath), machineID, "root"), nil
}

func trustedAbsolute(value string) bool {
	return value != "" && len(value) <= 4096 && filepath.IsAbs(value) && filepath.Clean(value) == value && value != "/" && !strings.ContainsRune(value, '\x00')
}

func validCgroupName(value string) bool {
	if value == "" || len(value) > 128 || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.Contains(value, "..") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || !validID(component) {
			return false
		}
	}
	return true
}
