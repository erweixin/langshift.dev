package firecracker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/langshift/lites/internal/runtime/guest"
)

var ErrGuestProtocol = errors.New("Firecracker guest protocol failed")

type GuestConnector interface {
	ConnectGuestAgent(context.Context) (net.Conn, error)
}

type GuestClient struct {
	Connector    GuestConnector
	WriteTimeout time.Duration
}

func (client GuestClient) Probe(ctx context.Context, requestID, challenge string) (guest.Attestation, error) {
	if client.Connector == nil || client.WriteTimeout <= 0 || client.WriteTimeout > 30*time.Second {
		return guest.Attestation{}, ErrInvalidSpec
	}
	connection, err := client.Connector.ConnectGuestAgent(ctx)
	if err != nil {
		return guest.Attestation{}, err
	}
	defer connection.Close()
	deadline := time.Now().Add(client.WriteTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err = connection.SetDeadline(deadline); err != nil {
		return guest.Attestation{}, fmt.Errorf("%w: probe deadline", ErrGuestProtocol)
	}
	probe := guest.ProbeRequest{Challenge: challenge}
	if err = guest.NewEncoder(connection).Encode(guest.Frame{Protocol: guest.ProtocolVersion, Kind: guest.FrameProbe, RequestID: requestID, Probe: &probe}); err != nil {
		return guest.Attestation{}, fmt.Errorf("%w: probe request", ErrGuestProtocol)
	}
	frame, err := guest.NewDecoder(connection).Decode()
	if err != nil || frame.Kind != guest.FrameAttest || frame.RequestID != requestID || frame.Sequence != 1 || frame.Attest == nil || frame.Attest.Challenge != challenge {
		return guest.Attestation{}, ErrGuestProtocol
	}
	bootedAt := time.UnixMilli(frame.Attest.BootUnixMillis)
	now := time.Now()
	if bootedAt.After(now.Add(5*time.Second)) || bootedAt.Before(now.Add(-5*time.Minute)) {
		return guest.Attestation{}, ErrGuestProtocol
	}
	return *frame.Attest, nil
}

func (client GuestClient) Execute(ctx context.Context, requestID string, request guest.ExecuteRequest, sink guest.FrameSink) (guest.ExecutionResult, error) {
	if client.Connector == nil || client.WriteTimeout <= 0 || client.WriteTimeout > 30*time.Second || request.Validate() != nil || sink == nil {
		return guest.ExecutionResult{}, ErrInvalidSpec
	}
	connection, err := client.Connector.ConnectGuestAgent(ctx)
	if err != nil {
		return guest.ExecutionResult{}, err
	}
	defer connection.Close()
	if err = ctx.Err(); err != nil {
		return guest.ExecutionResult{}, err
	}
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.Close()
		case <-watchDone:
		}
	}()
	deadline := time.UnixMilli(request.DeadlineUnixMillis)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if !deadline.After(time.Now()) {
		return guest.ExecutionResult{}, ErrGuestProtocol
	}
	writeDeadline := time.Now().Add(client.WriteTimeout)
	if deadline.Before(writeDeadline) {
		writeDeadline = deadline
	}
	if err = connection.SetWriteDeadline(writeDeadline); err != nil {
		return guest.ExecutionResult{}, fmt.Errorf("%w: request deadline", ErrGuestProtocol)
	}
	if err = guest.NewEncoder(connection).Encode(guest.Frame{Protocol: guest.ProtocolVersion, Kind: guest.FrameExecute, RequestID: requestID, Execute: &request}); err != nil {
		return guest.ExecutionResult{}, fmt.Errorf("%w: request", ErrGuestProtocol)
	}
	if err = connection.SetDeadline(deadline); err != nil {
		return guest.ExecutionResult{}, fmt.Errorf("%w: response deadline", ErrGuestProtocol)
	}
	decoder := guest.NewDecoder(connection)
	var sequence uint64
	var deliveredStdout, deliveredStderr int64
	for {
		frame, decodeErr := decoder.Decode()
		if decodeErr != nil {
			if ctx.Err() != nil {
				return guest.ExecutionResult{}, ctx.Err()
			}
			return guest.ExecutionResult{}, fmt.Errorf("%w: response", ErrGuestProtocol)
		}
		if frame.RequestID != requestID || frame.Sequence != sequence+1 || frame.Kind != guest.FrameStdout && frame.Kind != guest.FrameStderr && frame.Kind != guest.FrameResult {
			return guest.ExecutionResult{}, ErrGuestProtocol
		}
		sequence = frame.Sequence
		switch frame.Kind {
		case guest.FrameStdout:
			deliveredStdout += int64(len(frame.Chunk))
		case guest.FrameStderr:
			deliveredStderr += int64(len(frame.Chunk))
		case guest.FrameResult:
			result := *frame.Result
			if deliveredStdout+deliveredStderr > request.MaximumOutputBytes || deliveredStdout > result.StdoutBytes || deliveredStderr > result.StderrBytes || !result.OutputTruncated && (deliveredStdout != result.StdoutBytes || deliveredStderr != result.StderrBytes) {
				return guest.ExecutionResult{}, ErrGuestProtocol
			}
			if err = sink(frame); err != nil {
				return guest.ExecutionResult{}, err
			}
			return result, nil
		}
		if deliveredStdout+deliveredStderr > request.MaximumOutputBytes {
			return guest.ExecutionResult{}, ErrGuestProtocol
		}
		if err = sink(frame); err != nil {
			return guest.ExecutionResult{}, err
		}
	}
}
