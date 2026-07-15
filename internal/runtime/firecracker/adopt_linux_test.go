//go:build linux

package firecracker

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

func TestAdoptUsesExactKernelIdentityAndPidfdSignals(t *testing.T) {
	command := exec.Command("sleep", "30")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	})
	identity, err := readProcessIdentity(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	wrong := identity
	wrong.StartTicks++
	if alive, aliveErr := ProcessAlive(wrong, []string{identity.Executable}); aliveErr != nil || alive {
		t.Fatalf("recycled process status = %v, %v", alive, aliveErr)
	}
	if _, err = Adopt(wrong, []string{identity.Executable}, time.Second); !errors.Is(err, ErrProcessIdentity) {
		t.Fatalf("recycled identity accepted: %v", err)
	}
	machine, err := Adopt(identity, []string{identity.Executable}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if alive, aliveErr := ProcessAlive(identity, []string{identity.Executable}); aliveErr != nil || !alive {
		t.Fatalf("live process status = %v, %v", alive, aliveErr)
	}
	if err = machine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-machine.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("pidfd did not observe process exit")
	}
}
