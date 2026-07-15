package firecracker

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestVSockConnectorPerformsStrictHostInitiatedHandshake(t *testing.T) {
	host, guest := net.Pipe()
	defer guest.Close()
	done := make(chan error, 1)
	go func() {
		request, err := readHandshakeLine(guest, 64)
		if err == nil && request != "CONNECT 1070\n" {
			err = errors.New("unexpected CONNECT request")
		}
		if err == nil {
			_, err = io.WriteString(guest, "OK 1073741824\n")
		}
		done <- err
	}()
	connector := VSockConnector{SocketPath: "/run/guest.vsock", Timeout: time.Second, Dial: func(context.Context, string, string) (net.Conn, error) { return host, nil }}
	connection, err := connector.ConnectGuestAgent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

func TestVSockConnectorRejectsMalformedOrOversizedAcknowledgement(t *testing.T) {
	for _, response := range []string{"NO 1\n", "OK 0\n", "OK not-a-port\n", "OK 1\r\n", "OK 1234567890123456789012345678901234567890123456789012345678901234\n"} {
		t.Run(response, func(t *testing.T) {
			host, guest := net.Pipe()
			defer guest.Close()
			go func() {
				_, _ = readHandshakeLine(guest, 64)
				_, _ = io.WriteString(guest, response)
			}()
			connector := VSockConnector{SocketPath: "/run/guest.vsock", Timeout: time.Second, Dial: func(context.Context, string, string) (net.Conn, error) { return host, nil }}
			if connection, err := connector.ConnectGuestAgent(context.Background()); !errors.Is(err, ErrVSockHandshake) || connection != nil {
				t.Fatalf("ConnectGuestAgent() = %#v, %v", connection, err)
			}
		})
	}
}

func TestVSockConnectorHonorsHandshakeDeadline(t *testing.T) {
	host, guest := net.Pipe()
	defer guest.Close()
	connector := VSockConnector{SocketPath: "/run/guest.vsock", Timeout: 15 * time.Millisecond, Dial: func(context.Context, string, string) (net.Conn, error) { return host, nil }}
	if connection, err := connector.ConnectGuestAgent(context.Background()); !errors.Is(err, ErrVSockHandshake) || connection != nil {
		t.Fatalf("ConnectGuestAgent() = %#v, %v", connection, err)
	}
}
