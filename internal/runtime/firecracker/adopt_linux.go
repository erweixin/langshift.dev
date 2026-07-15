//go:build linux

package firecracker

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Adopt opens a pidfd only after an exact boot-id/start-ticks match and then
// revalidates through /proc. All later signals use the pidfd, never a bare PID.
func Adopt(identity ProcessIdentity, allowedExecutables []string, stopGrace time.Duration) (*Machine, error) {
	if identity.Validate() != nil || stopGrace <= 0 || stopGrace > 30*time.Second || !allowedExecutable(identity.Executable, allowedExecutables) {
		return nil, ErrProcessIdentity
	}
	current, err := readProcessIdentity(identity.PID)
	if err != nil || !sameKernelProcess(identity, current) || !allowedExecutable(current.Executable, allowedExecutables) {
		return nil, ErrProcessIdentity
	}
	descriptor, err := unix.PidfdOpen(identity.PID, unix.PIDFD_NONBLOCK)
	if err != nil {
		return nil, ErrProcessIdentity
	}
	process := &pidfdProcess{pid: identity.PID, descriptor: descriptor}
	failed := true
	defer func() {
		if failed {
			_ = process.close()
		}
	}()
	current, err = readProcessIdentity(identity.PID)
	if err != nil || !sameKernelProcess(identity, current) || !allowedExecutable(current.Executable, allowedExecutables) {
		return nil, ErrProcessIdentity
	}
	exit := &processExit{done: make(chan struct{})}
	go func() { exit.complete(process.Wait()) }()
	failed = false
	return &Machine{process: process, exit: exit, identity: identity, stopGrace: stopGrace}, nil
}

func sameKernelProcess(expected, current ProcessIdentity) bool {
	return expected.PID == current.PID && expected.StartTicks == current.StartTicks && expected.BootID == current.BootID
}

func allowedExecutable(value string, allowed []string) bool {
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value || len(allowed) == 0 {
		return false
	}
	for _, candidate := range allowed {
		if candidate != "" && filepath.IsAbs(candidate) && filepath.Clean(candidate) == candidate && value == candidate {
			return true
		}
	}
	return false
}

type pidfdProcess struct {
	pid        int
	mu         sync.Mutex
	descriptor int
	closed     bool
}

func (process *pidfdProcess) Start() error { return ErrProcessIdentity }
func (process *pidfdProcess) PID() int     { return process.pid }

func (process *pidfdProcess) Signal(signal os.Signal) error {
	value, ok := signal.(syscall.Signal)
	if !ok {
		return ErrProcessIdentity
	}
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.closed {
		return os.ErrProcessDone
	}
	if err := unix.PidfdSendSignal(process.descriptor, value, nil, 0); err != nil {
		if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.EBADF) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

func (process *pidfdProcess) Kill() error { return process.Signal(syscall.SIGKILL) }

func (process *pidfdProcess) Wait() error {
	process.mu.Lock()
	if process.closed {
		process.mu.Unlock()
		return os.ErrProcessDone
	}
	descriptor := process.descriptor
	process.mu.Unlock()
	for {
		poll := []unix.PollFd{{Fd: int32(descriptor), Events: unix.POLLIN}}
		_, err := unix.Poll(poll, -1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		_ = process.close()
		if err != nil {
			return err
		}
		return nil
	}
}

func (process *pidfdProcess) close() error {
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.closed {
		return nil
	}
	process.closed = true
	return unix.Close(process.descriptor)
}
