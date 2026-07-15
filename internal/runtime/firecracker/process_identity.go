package firecracker

import (
	"errors"
	"path/filepath"
)

var ErrProcessIdentity = errors.New("Firecracker process identity is invalid or unavailable")

// ProcessIdentity is the minimum host-kernel identity needed to distinguish a
// live VMM from a recycled PID after a host-agent restart. StartTicks is the
// process start time from /proc/<pid>/stat, not wall-clock time.
type ProcessIdentity struct {
	PID        int    `json:"pid"`
	StartTicks uint64 `json:"start_ticks"`
	BootID     string `json:"boot_id"`
	Executable string `json:"executable"`
}

func (identity ProcessIdentity) Validate() error {
	if identity.PID < 2 || identity.StartTicks == 0 || identity.BootID == "" || len(identity.BootID) > 128 ||
		identity.Executable == "" || !filepath.IsAbs(identity.Executable) || filepath.Clean(identity.Executable) != identity.Executable {
		return ErrProcessIdentity
	}
	return nil
}
