package firecracker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

var (
	ErrVMMStartup = errors.New("Firecracker VMM startup failed")
	ErrVMMExited  = errors.New("Firecracker VMM exited unexpectedly")
)

type managedProcess interface {
	Start() error
	PID() int
	Signal(os.Signal) error
	Kill() error
	Wait() error
}

type Runner struct {
	Jailer          JailerConfig
	StartupTimeout  time.Duration
	PollInterval    time.Duration
	APITimeout      time.Duration
	StopGrace       time.Duration
	processFactory  func(context.Context, JailerConfig, string) (managedProcess, error)
	processIdentity func(int) (ProcessIdentity, error)
	socketReady     func(string, uint32, uint32) (bool, error)
	apiReady        func(context.Context, string, time.Duration) error
	configure       func(context.Context, string, time.Duration, Spec) error
}

type Machine struct {
	process   managedProcess
	exit      *processExit
	identity  ProcessIdentity
	stopGrace time.Duration
	stopOnce  sync.Once
	stopErr   error
}

func (machine *Machine) Identity() (ProcessIdentity, error) {
	if machine == nil || machine.identity.Validate() != nil {
		return ProcessIdentity{}, ErrProcessIdentity
	}
	return machine.identity, nil
}

func (machine *Machine) Done() <-chan struct{} {
	if machine == nil || machine.exit == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return machine.exit.done
}

func (machine *Machine) ExitError() error {
	if machine == nil || machine.exit == nil {
		return ErrInvalidSpec
	}
	select {
	case <-machine.exit.done:
		return machine.exit.value()
	default:
		return nil
	}
}

type processExit struct {
	done chan struct{}
	mu   sync.Mutex
	err  error
}

func (exit *processExit) complete(err error) {
	exit.mu.Lock()
	exit.err = err
	exit.mu.Unlock()
	close(exit.done)
}

func (exit *processExit) value() error {
	exit.mu.Lock()
	defer exit.mu.Unlock()
	return exit.err
}

func (runner Runner) Start(ctx context.Context, spec Spec) (*Machine, error) {
	if spec.Validate() != nil || runner.StartupTimeout <= 0 || runner.StartupTimeout > time.Minute || runner.PollInterval <= 0 || runner.PollInterval > time.Second || runner.APITimeout <= 0 || runner.APITimeout > 10*time.Second || runner.StopGrace <= 0 || runner.StopGrace > 30*time.Second {
		return nil, ErrInvalidSpec
	}
	root, err := runner.Jailer.JailRoot(spec.MachineID)
	if err != nil {
		return nil, err
	}
	socketPath := filepath.Join(root, "run", "firecracker.socket")
	factory := runner.processFactory
	if factory == nil {
		factory = defaultProcessFactory
	}
	process, err := factory(ctx, runner.Jailer, spec.MachineID)
	if err != nil {
		return nil, err
	}
	if err = process.Start(); err != nil {
		return nil, fmt.Errorf("%w: process start", ErrVMMStartup)
	}
	identityReader := runner.processIdentity
	if identityReader == nil {
		identityReader = readProcessIdentity
	}
	identity, err := identityReader(process.PID())
	if err != nil || identity.Validate() != nil {
		_ = process.Kill()
		return nil, fmt.Errorf("%w: process identity", ErrVMMStartup)
	}
	exit := &processExit{done: make(chan struct{})}
	go func() { exit.complete(process.Wait()) }()
	machine := &Machine{process: process, exit: exit, identity: identity, stopGrace: runner.StopGrace}
	failed := true
	defer func() {
		if failed {
			_ = machine.Stop(context.Background())
		}
	}()

	probe := runner.socketReady
	if probe == nil {
		probe = secureSocketReady
	}
	apiProbe := runner.apiReady
	if apiProbe == nil {
		apiProbe = defaultAPIReady
	}
	configure := runner.configure
	if configure == nil {
		configure = defaultConfigure
	}
	startupContext, cancel := context.WithTimeout(ctx, runner.StartupTimeout)
	defer cancel()
	ticker := time.NewTicker(runner.PollInterval)
	defer ticker.Stop()
	for {
		ready, probeErr := probe(socketPath, runner.Jailer.UID, runner.Jailer.GID)
		if probeErr != nil {
			return nil, probeErr
		}
		if ready {
			probeErr = apiProbe(startupContext, socketPath, runner.APITimeout)
			if probeErr == nil {
				break
			}
			if errors.Is(probeErr, ErrIncompatibleVMM) {
				return nil, probeErr
			}
		}
		select {
		case <-exit.done:
			return nil, fmt.Errorf("%w: %v", ErrVMMExited, exit.value())
		case <-startupContext.Done():
			return nil, fmt.Errorf("%w: readiness deadline", ErrVMMStartup)
		case <-ticker.C:
		}
	}
	if err = configure(startupContext, socketPath, runner.APITimeout, spec); err != nil {
		return nil, err
	}
	failed = false
	return machine, nil
}

func (machine *Machine) Stop(ctx context.Context) error {
	if machine == nil || machine.process == nil || machine.exit == nil || machine.stopGrace <= 0 {
		return ErrInvalidSpec
	}
	machine.stopOnce.Do(func() {
		select {
		case <-machine.exit.done:
			machine.stopErr = ErrVMMExited
			return
		default:
		}
		if err := machine.process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
			machine.stopErr = err
		}
		timer := time.NewTimer(machine.stopGrace)
		defer timer.Stop()
		select {
		case <-machine.exit.done:
			return
		case <-ctx.Done():
			machine.stopErr = errors.Join(machine.stopErr, ctx.Err())
		case <-timer.C:
		}
		if err := machine.process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			machine.stopErr = errors.Join(machine.stopErr, err)
		}
		select {
		case <-machine.exit.done:
		case <-time.After(machine.stopGrace):
			machine.stopErr = errors.Join(machine.stopErr, ErrVMMExited)
		}
	})
	return machine.stopErr
}

type execProcess struct{ command *exec.Cmd }

func (process execProcess) Start() error { return process.command.Start() }
func (process execProcess) PID() int {
	if process.command == nil || process.command.Process == nil {
		return 0
	}
	return process.command.Process.Pid
}
func (process execProcess) Signal(signal os.Signal) error {
	return process.command.Process.Signal(signal)
}
func (process execProcess) Kill() error { return process.command.Process.Kill() }
func (process execProcess) Wait() error { return process.command.Wait() }

func defaultProcessFactory(ctx context.Context, config JailerConfig, machineID string) (managedProcess, error) {
	// The caller context bounds startup only. A successfully started VMM is
	// owned by RuntimeManager and must survive the delivery/request context.
	command, err := config.Command(context.Background(), machineID)
	if err != nil {
		return nil, err
	}
	command.Stdin, command.Stdout, command.Stderr = nil, nil, nil
	return execProcess{command: command}, nil
}

func defaultAPIReady(ctx context.Context, socketPath string, timeout time.Duration) error {
	version, err := (Client{SocketPath: socketPath, Timeout: timeout}).Version(ctx)
	if err != nil {
		return err
	}
	if version != CompatibleVersion {
		return fmt.Errorf("%w: got %q", ErrIncompatibleVMM, version)
	}
	return nil
}

func defaultConfigure(ctx context.Context, socketPath string, timeout time.Duration, spec Spec) error {
	return (Client{SocketPath: socketPath, Timeout: timeout}).ConfigureAndStart(ctx, spec)
}
