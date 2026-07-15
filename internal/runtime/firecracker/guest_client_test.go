package firecracker

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/langshift/lites/internal/runtime/guest"
)

type connectionConnector struct{ connection net.Conn }

func (connector connectionConnector) ConnectGuestAgent(context.Context) (net.Conn, error) {
	return connector.connection, nil
}

func TestGuestClientBindsOrderedFramesAndResultToRequest(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	request := guest.ExecuteRequest{
		Argv: []string{executable, "-test.run=TestFirecrackerGuestHelper", "--", "success"}, WorkingDirectory: "/workspace",
		DeadlineUnixMillis: time.Now().Add(5 * time.Second).UnixMilli(), MaximumOutputBytes: 1 << 20,
	}
	clientConnection, serverConnection := net.Pipe()
	server := guest.Server{
		Executor:          guest.Executor{MaximumDuration: 10 * time.Second, WorkspaceRoot: t.TempDir()},
		MaximumConcurrent: 1, InitialRequestTimeout: time.Second, WriteTimeout: time.Second,
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.ServeConnection(context.Background(), serverConnection) }()
	var frames []guest.Frame
	result, err := (GuestClient{Connector: connectionConnector{clientConnection}, WriteTimeout: time.Second}).Execute(context.Background(), "request-00000006", request, func(frame guest.Frame) error {
		frames = append(frames, frame)
		return nil
	})
	if err != nil || result.ExitCode != 0 || len(frames) < 2 || frames[len(frames)-1].Kind != guest.FrameResult {
		t.Fatalf("Execute() = %#v, %v, frames=%#v", result, err, frames)
	}
	if err = <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestGuestClientRejectsCrossRequestAndOutOfOrderFrames(t *testing.T) {
	for _, mutate := range []func(*guest.Frame){
		func(frame *guest.Frame) { frame.RequestID = "request-cross-0001" },
		func(frame *guest.Frame) { frame.Sequence = 2 },
	} {
		clientConnection, serverConnection := net.Pipe()
		go func() {
			defer serverConnection.Close()
			request, _ := guest.NewDecoder(serverConnection).Decode()
			result := guest.ExecutionResult{ExitCode: 0, StartedUnixMillis: time.Now().UnixMilli(), FinishedUnixMillis: time.Now().UnixMilli()}
			frame := guest.Frame{Protocol: guest.ProtocolVersion, Kind: guest.FrameResult, RequestID: request.RequestID, Sequence: 1, Result: &result}
			mutate(&frame)
			_ = guest.NewEncoder(serverConnection).Encode(frame)
		}()
		request := guest.ExecuteRequest{Argv: []string{"/usr/bin/tool"}, WorkingDirectory: "/workspace", DeadlineUnixMillis: time.Now().Add(time.Second).UnixMilli(), MaximumOutputBytes: 1024}
		_, err := (GuestClient{Connector: connectionConnector{clientConnection}, WriteTimeout: time.Second}).Execute(context.Background(), "request-00000007", request, func(guest.Frame) error { return nil })
		if !errors.Is(err, ErrGuestProtocol) {
			t.Fatalf("malicious response = %v", err)
		}
	}
}

func TestGuestClientCancellationClosesBlockedGuestConnection(t *testing.T) {
	clientConnection, serverConnection := net.Pipe()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		defer serverConnection.Close()
		_, _ = guest.NewDecoder(serverConnection).Decode()
		var buffer [1]byte
		_, _ = serverConnection.Read(buffer[:])
	}()
	request := guest.ExecuteRequest{Argv: []string{"/usr/bin/tool"}, WorkingDirectory: "/workspace", DeadlineUnixMillis: time.Now().Add(time.Minute).UnixMilli(), MaximumOutputBytes: 1024}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (GuestClient{Connector: connectionConnector{clientConnection}, WriteTimeout: time.Second}).Execute(ctx, "request-00000008", request, func(guest.Frame) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Execute() = %v", err)
	}
	<-serverDone
}

func TestFirecrackerGuestHelper(t *testing.T) {
	separator := -1
	for index, value := range os.Args {
		if value == "--" {
			separator = index
			break
		}
	}
	if separator < 0 {
		return
	}
	_, _ = os.Stdout.WriteString("host-client-stdout")
	os.Exit(0)
}
