package firecracker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

var ErrVSockHandshake = errors.New("Firecracker vsock handshake failed")

type VSockConnector struct {
	SocketPath string
	Timeout    time.Duration
	Dial       func(context.Context, string, string) (net.Conn, error)
}

func (connector VSockConnector) ConnectGuestAgent(ctx context.Context) (net.Conn, error) {
	if !jailedPath(connector.SocketPath) || connector.Timeout <= 0 || connector.Timeout > 30*time.Second {
		return nil, ErrInvalidSpec
	}
	dial := connector.Dial
	if dial == nil {
		dialer := &net.Dialer{Timeout: connector.Timeout, KeepAlive: -1}
		dial = dialer.DialContext
	}
	connection, err := dial(ctx, "unix", connector.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("%w: connect", ErrVSockHandshake)
	}
	failed := true
	defer func() {
		if failed {
			_ = connection.Close()
		}
	}()
	deadline := time.Now().Add(connector.Timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err = connection.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("%w: deadline", ErrVSockHandshake)
	}
	if _, err = io.WriteString(connection, fmt.Sprintf("CONNECT %d\n", GuestAgentPort)); err != nil {
		return nil, fmt.Errorf("%w: request", ErrVSockHandshake)
	}
	line, err := readHandshakeLine(connection, 64)
	if err != nil {
		return nil, fmt.Errorf("%w: response", ErrVSockHandshake)
	}
	parts := strings.Fields(strings.TrimSuffix(line, "\n"))
	if len(parts) != 2 || parts[0] != "OK" {
		return nil, ErrVSockHandshake
	}
	port, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil || port == 0 {
		return nil, ErrVSockHandshake
	}
	if err = connection.SetDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("%w: clear deadline", ErrVSockHandshake)
	}
	failed = false
	return connection, nil
}

func readHandshakeLine(reader io.Reader, maximum int) (string, error) {
	if maximum < 1 {
		return "", ErrVSockHandshake
	}
	buffer := make([]byte, 0, maximum)
	var next [1]byte
	for len(buffer) < maximum {
		if _, err := io.ReadFull(reader, next[:]); err != nil {
			return "", err
		}
		if next[0] == 0 || next[0] == '\r' || next[0] < 0x20 && next[0] != '\n' || next[0] == 0x7f {
			return "", ErrVSockHandshake
		}
		buffer = append(buffer, next[0])
		if next[0] == '\n' {
			return string(buffer), nil
		}
	}
	return "", ErrVSockHandshake
}
