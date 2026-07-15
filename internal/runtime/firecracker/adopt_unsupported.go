//go:build !linux

package firecracker

import "time"

func Adopt(ProcessIdentity, []string, time.Duration) (*Machine, error) {
	return nil, ErrProcessIdentity
}
