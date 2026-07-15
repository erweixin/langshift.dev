package guest

import (
	"context"
	"net"
	"os"
	"testing"
	"time"
)

func TestServerExecutesExactlyOneStrictRequestOverConnection(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	request := validRequest()
	request.Argv = []string{executable, "-test.run=TestGuestExecutorHelper", "--", "success"}
	request.WorkingDirectory = "/workspace"
	request.Environment = nil
	request.DeadlineUnixMillis = time.Now().Add(5 * time.Second).UnixMilli()
	client, serverConnection := net.Pipe()
	server := Server{
		Executor:          Executor{MaximumDuration: 10 * time.Second, WorkspaceRoot: t.TempDir()},
		MaximumConcurrent: 1, InitialRequestTimeout: time.Second, WriteTimeout: time.Second,
	}
	done := make(chan error, 1)
	go func() { done <- server.ServeConnection(context.Background(), serverConnection) }()
	if err = NewEncoder(client).Encode(Frame{Protocol: ProtocolVersion, Kind: FrameExecute, RequestID: "request-00000005", Execute: &request}); err != nil {
		t.Fatal(err)
	}
	decoder := NewDecoder(client)
	seenResult := false
	for !seenResult {
		frame, decodeErr := decoder.Decode()
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		seenResult = frame.Kind == FrameResult
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
}
