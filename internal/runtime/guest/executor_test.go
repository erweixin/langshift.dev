package guest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestExecutorStreamsOutputAndReportsExactResultWithoutShell(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	request := validRequest()
	request.Argv = []string{executable, "-test.run=TestGuestExecutorHelper", "--", "success"}
	request.WorkingDirectory = "/workspace"
	request.Environment = nil
	request.DeadlineUnixMillis = now.Add(5 * time.Second).UnixMilli()
	var frames []Frame
	result, err := (Executor{MaximumDuration: 10 * time.Second, WorkspaceRoot: t.TempDir()}).Execute(context.Background(), "request-00000001", request, func(frame Frame) error {
		frames = append(frames, frame)
		return nil
	})
	if err != nil || result.ExitCode != 0 || result.FailureCode != "" || len(frames) < 2 || frames[len(frames)-1].Kind != FrameResult {
		t.Fatalf("Execute() = %#v, %v, frames=%#v", result, err, frames)
	}
	var stdout, stderr string
	for _, frame := range frames {
		if frame.Kind == FrameStdout {
			stdout += string(frame.Chunk)
		}
		if frame.Kind == FrameStderr {
			stderr += string(frame.Chunk)
		}
	}
	if stdout != "guest-stdout" || stderr != "guest-stderr" {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestExecutorKillsProcessOnOutputLimitAndTransportFailure(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	request := validRequest()
	request.Argv = []string{executable, "-test.run=TestGuestExecutorHelper", "--", "overflow"}
	request.WorkingDirectory = "/workspace"
	request.Environment = nil
	request.DeadlineUnixMillis = time.Now().Add(5 * time.Second).UnixMilli()
	request.MaximumOutputBytes = 1024
	executor := Executor{MaximumDuration: 10 * time.Second, WorkspaceRoot: t.TempDir()}
	result, err := executor.Execute(context.Background(), "request-00000002", request, func(Frame) error { return nil })
	if err != nil || !result.OutputTruncated || result.FailureCode != "output_limit_exceeded" {
		t.Fatalf("output limit = %#v, %v", result, err)
	}

	request.Argv[len(request.Argv)-1] = "success"
	_, err = executor.Execute(context.Background(), "request-00000003", request, func(Frame) error { return errors.New("connection lost") })
	if !errors.Is(err, ErrFrameSink) {
		t.Fatalf("transport failure = %v", err)
	}
}

func TestExecutorEnforcesGuestDeadline(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	request := validRequest()
	request.Argv = []string{executable, "-test.run=TestGuestExecutorHelper", "--", "sleep"}
	request.WorkingDirectory = "/workspace"
	request.Environment = nil
	request.DeadlineUnixMillis = time.Now().Add(100 * time.Millisecond).UnixMilli()
	result, err := (Executor{MaximumDuration: 10 * time.Second, WorkspaceRoot: t.TempDir()}).Execute(context.Background(), "request-00000004", request, func(Frame) error { return nil })
	if err != nil || !result.TimedOut || result.FailureCode != "deadline_exceeded" || result.ExitCode != -1 {
		t.Fatalf("deadline result = %#v, %v", result, err)
	}
}

func TestGuestExecutorHelper(t *testing.T) {
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator == -1 || separator+1 >= len(os.Args) {
		return
	}
	switch os.Args[separator+1] {
	case "success":
		_, _ = fmt.Fprint(os.Stdout, "guest-stdout")
		_, _ = fmt.Fprint(os.Stderr, "guest-stderr")
	case "overflow":
		_, _ = fmt.Fprint(os.Stdout, strings.Repeat("x", 4096))
	case "sleep":
		time.Sleep(5 * time.Second)
	default:
		os.Exit(9)
	}
	os.Exit(0)
}
