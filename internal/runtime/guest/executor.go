package guest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	ErrExecutionPolicy = errors.New("runtime guest execution policy rejected the request")
	ErrFrameSink       = errors.New("runtime guest frame sink failed")
)

type FrameSink func(Frame) error

type Executor struct {
	MaximumDuration time.Duration
	WorkspaceRoot   string
	Now             func() time.Time
}

func (executor Executor) Execute(ctx context.Context, requestID string, request ExecuteRequest, sink FrameSink) (ExecutionResult, error) {
	workspaceRoot := executor.WorkspaceRoot
	if workspaceRoot == "" {
		workspaceRoot = "/workspace"
	}
	if !requestIDPattern.MatchString(requestID) || request.Validate() != nil || sink == nil || executor.MaximumDuration < time.Second || executor.MaximumDuration > time.Hour || !filepath.IsAbs(workspaceRoot) || filepath.Clean(workspaceRoot) != workspaceRoot || workspaceRoot == "/" {
		return ExecutionResult{}, ErrExecutionPolicy
	}
	now := time.Now().UTC()
	if executor.Now != nil {
		now = executor.Now().UTC()
	}
	deadline := time.UnixMilli(request.DeadlineUnixMillis).UTC()
	if !deadline.After(now) || deadline.After(now.Add(executor.MaximumDuration)) {
		return ExecutionResult{}, ErrExecutionPolicy
	}
	executionContext, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	command := exec.CommandContext(executionContext, request.Argv[0], request.Argv[1:]...)
	relativeWorkingDirectory := strings.TrimPrefix(strings.TrimPrefix(request.WorkingDirectory, "/workspace"), "/")
	command.Dir = filepath.Join(workspaceRoot, relativeWorkingDirectory)
	command.Env = make([]string, 0, len(request.Environment))
	for _, variable := range request.Environment {
		command.Env = append(command.Env, variable.Name+"="+variable.Value)
	}
	command.Stdin = bytes.NewReader(request.Stdin)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return ExecutionResult{}, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return ExecutionResult{}, err
	}
	started := time.Now().UTC()
	if err = command.Start(); err != nil {
		return ExecutionResult{}, err
	}

	stream := &streamState{requestID: requestID, sink: sink, remaining: request.MaximumOutputBytes, cancel: cancel}
	var readers sync.WaitGroup
	readers.Add(2)
	go func() { defer readers.Done(); stream.copy(FrameStdout, stdout) }()
	go func() { defer readers.Done(); stream.copy(FrameStderr, stderr) }()
	waitErr := command.Wait()
	readers.Wait()
	finished := time.Now().UTC()

	result := ExecutionResult{
		ExitCode: command.ProcessState.ExitCode(), OutputTruncated: stream.truncated,
		StdoutBytes: stream.stdoutBytes, StderrBytes: stream.stderrBytes,
		UserCPUTimeMillis: command.ProcessState.UserTime().Milliseconds(), SystemCPUTimeMillis: command.ProcessState.SystemTime().Milliseconds(),
		StartedUnixMillis: started.UnixMilli(), FinishedUnixMillis: finished.UnixMilli(),
	}
	if stream.sinkErr != nil {
		result.FailureCode = "transport_failed"
	} else if stream.truncated {
		result.FailureCode = "output_limit_exceeded"
	} else if errors.Is(executionContext.Err(), context.DeadlineExceeded) {
		result.FailureCode, result.TimedOut = "deadline_exceeded", true
	} else if errors.Is(executionContext.Err(), context.Canceled) {
		result.FailureCode = "cancelled"
	} else if waitErr != nil {
		result.FailureCode = "process_failed"
	}
	if result.ExitCode < -1 {
		result.ExitCode = -1
	}
	if stream.sinkErr != nil {
		return result, errors.Join(ErrFrameSink, stream.sinkErr)
	}
	if err = stream.emit(Frame{Protocol: ProtocolVersion, Kind: FrameResult, RequestID: requestID, Result: &result}); err != nil {
		return result, errors.Join(ErrFrameSink, err)
	}
	return result, nil
}

type streamState struct {
	mu          sync.Mutex
	requestID   string
	sink        FrameSink
	cancel      context.CancelFunc
	sequence    uint64
	remaining   int64
	stdoutBytes int64
	stderrBytes int64
	truncated   bool
	sinkErr     error
}

func (state *streamState) copy(kind FrameKind, reader io.Reader) {
	buffer := make([]byte, MaximumChunk)
	for {
		count, err := reader.Read(buffer)
		if count > 0 && !state.write(kind, buffer[:count]) {
			return
		}
		if err != nil {
			return
		}
	}
}

func (state *streamState) write(kind FrameKind, chunk []byte) bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.sinkErr != nil || state.truncated {
		return false
	}
	if kind == FrameStdout {
		state.stdoutBytes += int64(len(chunk))
	} else {
		state.stderrBytes += int64(len(chunk))
	}
	deliver := chunk
	if int64(len(deliver)) > state.remaining {
		deliver = deliver[:state.remaining]
		state.truncated = true
	}
	state.remaining -= int64(len(deliver))
	if len(deliver) == 0 {
		state.cancel()
		return false
	}
	state.sequence++
	copyOfChunk := append([]byte(nil), deliver...)
	if err := state.sink(Frame{Protocol: ProtocolVersion, Kind: kind, RequestID: state.requestID, Sequence: state.sequence, Chunk: copyOfChunk}); err != nil {
		state.sinkErr = err
		state.cancel()
		return false
	}
	if state.truncated {
		state.cancel()
		return false
	}
	return true
}

func (state *streamState) emit(frame Frame) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.sequence++
	frame.Sequence = state.sequence
	return state.sink(frame)
}
