package firecracker

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

type fakeManagedProcess struct {
	mu       sync.Mutex
	started  bool
	signaled bool
	killed   bool
	done     chan error
}

func newFakeManagedProcess() *fakeManagedProcess {
	return &fakeManagedProcess{done: make(chan error, 1)}
}
func (process *fakeManagedProcess) Start() error {
	process.mu.Lock()
	defer process.mu.Unlock()
	process.started = true
	return nil
}
func (process *fakeManagedProcess) Signal(os.Signal) error {
	process.mu.Lock()
	process.signaled = true
	process.mu.Unlock()
	process.done <- errors.New("terminated")
	return nil
}
func (process *fakeManagedProcess) Kill() error {
	process.mu.Lock()
	defer process.mu.Unlock()
	process.killed = true
	return nil
}
func (process *fakeManagedProcess) Wait() error { return <-process.done }

func TestRunnerWaitsForSecurePinnedAPIThenConfiguresAndStops(t *testing.T) {
	process := newFakeManagedProcess()
	probes := 0
	configured := false
	runner := Runner{
		Jailer: validJailerConfig(), StartupTimeout: time.Second, PollInterval: time.Millisecond, APITimeout: time.Second, StopGrace: time.Second,
		processFactory: func(context.Context, JailerConfig, string) (managedProcess, error) { return process, nil },
		socketReady:    func(string, uint32, uint32) (bool, error) { probes++; return probes >= 2, nil },
		apiReady:       func(context.Context, string, time.Duration) error { return nil },
		configure:      func(context.Context, string, time.Duration, Spec) error { configured = true; return nil },
	}
	machine, err := runner.Start(context.Background(), validSpec())
	if err != nil || !configured || !process.started {
		t.Fatalf("Start() = %#v, %v", machine, err)
	}
	if err = machine.Stop(context.Background()); err != nil || !process.signaled || process.killed {
		t.Fatalf("Stop() = %v, signaled=%v killed=%v", err, process.signaled, process.killed)
	}
	select {
	case <-machine.Done():
	default:
		t.Fatal("stopped VMM did not publish process exit")
	}
	if machine.ExitError() == nil {
		t.Fatal("VMM exit result was not observable")
	}
	if err = machine.Stop(context.Background()); err != nil {
		t.Fatalf("idempotent Stop() = %v", err)
	}
}

func TestRunnerFailsClosedAndKillsVMMWhenConfigurationFails(t *testing.T) {
	process := newFakeManagedProcess()
	runner := Runner{
		Jailer: validJailerConfig(), StartupTimeout: time.Second, PollInterval: time.Millisecond, APITimeout: time.Second, StopGrace: time.Second,
		processFactory: func(context.Context, JailerConfig, string) (managedProcess, error) { return process, nil },
		socketReady:    func(string, uint32, uint32) (bool, error) { return true, nil },
		apiReady:       func(context.Context, string, time.Duration) error { return nil },
		configure:      func(context.Context, string, time.Duration, Spec) error { return ErrAPI },
	}
	if machine, err := runner.Start(context.Background(), validSpec()); machine != nil || !errors.Is(err, ErrAPI) || !process.signaled {
		t.Fatalf("Start() = %#v, %v, signaled=%v", machine, err, process.signaled)
	}
}

func TestSecureSocketProbeRejectsPermissionsOwnerAndSymlink(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "lites-fc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	path := filepath.Join(directory, "firecracker.socket")
	listener, err := net.Listen("unix", path)
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skip("test sandbox forbids Unix socket creation")
		}
		t.Fatal(err)
	}
	defer listener.Close()
	if err = os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("socket stat has no unix ownership metadata")
	}
	ready, err := secureSocketReady(path, owner.Uid, owner.Gid)
	if err != nil || !ready {
		t.Fatalf("secureSocketReady() = %v, %v", ready, err)
	}
	if _, err = secureSocketReady(path, owner.Uid+1, owner.Gid); !errors.Is(err, ErrUntrustedHostPath) {
		t.Fatalf("wrong owner = %v", err)
	}
	link := filepath.Join(directory, "linked.socket")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err = secureSocketReady(link, owner.Uid, owner.Gid); !errors.Is(err, ErrUntrustedHostPath) {
		t.Fatalf("socket symlink = %v", err)
	}
}
