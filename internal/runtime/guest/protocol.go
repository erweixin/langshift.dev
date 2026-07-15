// Package guest defines the authenticated host-to-guest execution protocol.
// The protocol is carried only after the Firecracker vsock CONNECT handshake;
// capabilities remain in the trusted host manager and are never exposed to
// untrusted guest processes.
package guest

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	ProtocolVersion = "lites.runtime.guest.v1"
	MaximumFrame    = 1 << 20
	MaximumChunk    = 64 << 10
)

var (
	ErrMalformedFrame = errors.New("runtime guest frame is malformed")
	ErrInvalidRequest = errors.New("runtime guest execution request is invalid")
	requestIDPattern  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{7,127}$`)
	environmentKey    = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
)

type FrameKind string

const (
	FrameExecute FrameKind = "execute"
	FrameStdout  FrameKind = "stdout"
	FrameStderr  FrameKind = "stderr"
	FrameResult  FrameKind = "result"
)

type EnvironmentVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type ExecuteRequest struct {
	Argv               []string              `json:"argv"`
	WorkingDirectory   string                `json:"working_directory"`
	Environment        []EnvironmentVariable `json:"environment"`
	Stdin              []byte                `json:"stdin,omitempty"`
	DeadlineUnixMillis int64                 `json:"deadline_unix_millis"`
	MaximumOutputBytes int64                 `json:"maximum_output_bytes"`
}

type ExecutionResult struct {
	ExitCode            int    `json:"exit_code"`
	FailureCode         string `json:"failure_code,omitempty"`
	TimedOut            bool   `json:"timed_out"`
	OutputTruncated     bool   `json:"output_truncated"`
	StdoutBytes         int64  `json:"stdout_bytes"`
	StderrBytes         int64  `json:"stderr_bytes"`
	UserCPUTimeMillis   int64  `json:"user_cpu_time_millis"`
	SystemCPUTimeMillis int64  `json:"system_cpu_time_millis"`
	StartedUnixMillis   int64  `json:"started_unix_millis"`
	FinishedUnixMillis  int64  `json:"finished_unix_millis"`
}

type Frame struct {
	Protocol  string           `json:"protocol"`
	Kind      FrameKind        `json:"kind"`
	RequestID string           `json:"request_id"`
	Sequence  uint64           `json:"sequence"`
	Execute   *ExecuteRequest  `json:"execute,omitempty"`
	Chunk     []byte           `json:"chunk,omitempty"`
	Result    *ExecutionResult `json:"result,omitempty"`
}

type Encoder struct{ writer io.Writer }
type Decoder struct{ reader *bufio.Reader }

func NewEncoder(writer io.Writer) *Encoder { return &Encoder{writer: writer} }
func NewDecoder(reader io.Reader) *Decoder { return &Decoder{reader: bufio.NewReader(reader)} }

func (encoder *Encoder) Encode(frame Frame) error {
	if encoder == nil || encoder.writer == nil || !validFrame(frame) {
		return ErrMalformedFrame
	}
	encoded, err := json.Marshal(frame)
	if err != nil || len(encoded) == 0 || len(encoded) > MaximumFrame {
		return ErrMalformedFrame
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(encoded)))
	if err = writeAll(encoder.writer, size[:]); err != nil {
		return err
	}
	return writeAll(encoder.writer, encoded)
}

func (decoder *Decoder) Decode() (Frame, error) {
	if decoder == nil || decoder.reader == nil {
		return Frame{}, ErrMalformedFrame
	}
	var size [4]byte
	if _, err := io.ReadFull(decoder.reader, size[:]); err != nil {
		return Frame{}, err
	}
	length := binary.BigEndian.Uint32(size[:])
	if length == 0 || length > MaximumFrame {
		return Frame{}, ErrMalformedFrame
	}
	encoded := make([]byte, length)
	if _, err := io.ReadFull(decoder.reader, encoded); err != nil {
		return Frame{}, err
	}
	var frame Frame
	strict := json.NewDecoder(bytes.NewReader(encoded))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&frame); err != nil || strict.Decode(&struct{}{}) != io.EOF || !validFrame(frame) {
		return Frame{}, ErrMalformedFrame
	}
	return frame, nil
}

func (request ExecuteRequest) Validate() error {
	if len(request.Argv) < 1 || len(request.Argv) > 256 || !filepath.IsAbs(request.Argv[0]) || filepath.Clean(request.Argv[0]) != request.Argv[0] || request.Argv[0] == "/" || len(request.Environment) > 64 || len(request.Stdin) > MaximumFrame || request.DeadlineUnixMillis <= 0 || request.MaximumOutputBytes < 1024 || request.MaximumOutputBytes > 16<<20 || !workspacePath(request.WorkingDirectory) {
		return ErrInvalidRequest
	}
	total := 0
	for _, argument := range request.Argv {
		total += len(argument)
		if argument == "" || len(argument) > 4096 || strings.ContainsRune(argument, 0) || total > 64<<10 {
			return ErrInvalidRequest
		}
	}
	previous := ""
	for _, variable := range request.Environment {
		if !environmentKey.MatchString(variable.Name) || !allowedEnvironment(variable.Name) || variable.Name <= previous || len(variable.Value) > 4096 || strings.ContainsRune(variable.Value, 0) {
			return ErrInvalidRequest
		}
		previous = variable.Name
	}
	return nil
}

func validFrame(frame Frame) bool {
	if frame.Protocol != ProtocolVersion || !requestIDPattern.MatchString(frame.RequestID) {
		return false
	}
	switch frame.Kind {
	case FrameExecute:
		return frame.Sequence == 0 && frame.Execute != nil && frame.Execute.Validate() == nil && len(frame.Chunk) == 0 && frame.Result == nil
	case FrameStdout, FrameStderr:
		return frame.Sequence > 0 && frame.Execute == nil && len(frame.Chunk) > 0 && len(frame.Chunk) <= MaximumChunk && frame.Result == nil
	case FrameResult:
		return frame.Sequence > 0 && frame.Execute == nil && len(frame.Chunk) == 0 && frame.Result != nil && validResult(*frame.Result)
	default:
		return false
	}
}

func validResult(result ExecutionResult) bool {
	knownFailure := result.FailureCode == "" || result.FailureCode == "process_failed" || result.FailureCode == "deadline_exceeded" || result.FailureCode == "cancelled" || result.FailureCode == "output_limit_exceeded" || result.FailureCode == "transport_failed"
	consistent := (result.FailureCode != "" || result.ExitCode == 0 && !result.TimedOut && !result.OutputTruncated) && (!result.TimedOut || result.FailureCode == "deadline_exceeded") && (!result.OutputTruncated || result.FailureCode == "output_limit_exceeded" || result.FailureCode == "transport_failed")
	return result.ExitCode >= -1 && result.StdoutBytes >= 0 && result.StderrBytes >= 0 && result.UserCPUTimeMillis >= 0 && result.SystemCPUTimeMillis >= 0 && result.StartedUnixMillis > 0 && result.FinishedUnixMillis >= result.StartedUnixMillis && knownFailure && consistent
}

func workspacePath(value string) bool {
	return value == "/workspace" || strings.HasPrefix(value, "/workspace/") && filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsRune(value, 0)
}

func allowedEnvironment(name string) bool {
	switch name {
	case "HOME", "LANG", "LC_ALL", "PATH", "TERM", "TZ", "LITES_REQUEST_ID":
		return true
	default:
		return false
	}
}

func writeAll(writer io.Writer, value []byte) error {
	for len(value) > 0 {
		written, err := writer.Write(value)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		value = value[written:]
	}
	return nil
}
