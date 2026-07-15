//go:build !linux

package firecracker

import (
	"context"
	"errors"
	"time"
)

var ErrCgroupRecovery = errors.New("Firecracker cgroup recovery is unsupported")

func KillMachineCgroup(context.Context, JailerConfig, string, time.Duration) error {
	return ErrCgroupRecovery
}
