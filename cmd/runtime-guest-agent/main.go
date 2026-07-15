package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/langshift/lites/internal/runtime/firecracker"
	"github.com/langshift/lites/internal/runtime/guest"
)

const guestUserID = 1000
const guestGroupID = 1000

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "runtime guest agent failed")
		os.Exit(1)
	}
}

func run() error {
	if err := validateIdentity(os.Geteuid(), os.Getegid()); err != nil {
		return err
	}
	workspace, err := os.Lstat("/workspace")
	if err != nil || !workspace.IsDir() || workspace.Mode()&os.ModeSymlink != 0 {
		return errors.New("invalid workspace mount")
	}
	listener, err := guest.ListenVSock(firecracker.GuestAgentPort)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	server := guest.Server{
		Executor:          guest.Executor{MaximumDuration: time.Hour, WorkspaceRoot: "/workspace"},
		MaximumConcurrent: 1, InitialRequestTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second,
	}
	if err = server.Serve(ctx, listener); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

func validateIdentity(userID, groupID int) error {
	if userID != guestUserID || groupID != guestGroupID {
		return errors.New("runtime guest agent must run as the dedicated unprivileged identity")
	}
	return nil
}
