// Package firecracker implements the pinned, fail-closed Firecracker VMM
// boundary used for untrusted Lites runtime sessions.
package firecracker

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	CompatibleVersion = "1.15.1"
	// SwaggerSHA256 binds the client below to the upstream v1.15.1 API schema.
	SwaggerSHA256  = "7888a72d344acea478ae569b0cba8156f0d4b64f0e6ec11a50011a012c4893be"
	GuestAgentPort = 1070
	bootArguments  = "ro quiet loglevel=3 reboot=k panic=1 pci=off nomodule random.trust_cpu=on"
)

var (
	ErrInvalidSpec       = errors.New("invalid Firecracker machine specification")
	ErrIncompatibleVMM   = errors.New("incompatible Firecracker VMM version")
	ErrAPI               = errors.New("Firecracker API request failed")
	validMachineID       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}[a-z0-9]$`)
	validSingleMachineID = regexp.MustCompile(`^[a-z0-9]$`)
)

type TokenBucket struct {
	Size         int64
	RefillMillis int64
	Burst        int64
}

type RateLimit struct {
	Bandwidth  TokenBucket
	Operations TokenBucket
}

type Spec struct {
	MachineID       string
	KernelImagePath string
	RootDrivePath   string
	ScratchPath     string
	VSockPath       string
	GuestCID        uint32
	VCPUCount       int
	MemoryMiB       int
	ScratchLimit    RateLimit
}

func (spec Spec) Validate() error {
	if !validID(spec.MachineID) || !jailedPath(spec.KernelImagePath) || !jailedPath(spec.RootDrivePath) || !jailedPath(spec.ScratchPath) || !jailedPath(spec.VSockPath) || spec.KernelImagePath == spec.RootDrivePath || spec.KernelImagePath == spec.ScratchPath || spec.RootDrivePath == spec.ScratchPath || spec.GuestCID < 3 || spec.VCPUCount < 1 || spec.VCPUCount > 32 || spec.MemoryMiB < 128 || spec.MemoryMiB > 32768 || spec.MemoryMiB%2 != 0 || !validBucket(spec.ScratchLimit.Bandwidth) || !validBucket(spec.ScratchLimit.Operations) {
		return ErrInvalidSpec
	}
	return nil
}

func validID(value string) bool {
	return validMachineID.MatchString(value) || validSingleMachineID.MatchString(value)
}

func jailedPath(value string) bool {
	if value == "" || len(value) > 255 || !filepath.IsAbs(value) || filepath.Clean(value) != value || value == "/" || strings.ContainsRune(value, '\x00') {
		return false
	}
	for _, component := range strings.Split(value, string(filepath.Separator)) {
		if component == ".." {
			return false
		}
	}
	return true
}

func validBucket(bucket TokenBucket) bool {
	return bucket.Size > 0 && bucket.Size <= 1<<40 && bucket.RefillMillis >= 1 && bucket.RefillMillis <= 60_000 && bucket.Burst >= 0 && bucket.Burst <= bucket.Size
}
