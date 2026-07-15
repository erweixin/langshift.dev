package guest

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"
)

func validRequest() ExecuteRequest {
	return ExecuteRequest{
		Argv: []string{"/usr/bin/tool", "--safe"}, WorkingDirectory: "/workspace/project",
		Environment:        []EnvironmentVariable{{Name: "HOME", Value: "/workspace"}, {Name: "LANG", Value: "C.UTF-8"}},
		DeadlineUnixMillis: time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC).UnixMilli(), MaximumOutputBytes: 1 << 20,
	}
}

func TestFrameCodecIsBoundedStrictAndRoundTrips(t *testing.T) {
	frame := Frame{Protocol: ProtocolVersion, Kind: FrameExecute, RequestID: "request-00000001", Execute: pointer(validRequest())}
	var encoded bytes.Buffer
	if err := NewEncoder(&encoded).Encode(frame); err != nil {
		t.Fatal(err)
	}
	decoded, err := NewDecoder(&encoded).Decode()
	if err != nil || decoded.Execute == nil || decoded.Execute.Argv[0] != "/usr/bin/tool" {
		t.Fatalf("Decode() = %#v, %v", decoded, err)
	}

	for _, body := range []string{
		`{"protocol":"lites.runtime.guest.v1","kind":"stdout","request_id":"request-00000001","sequence":1,"chunk":"YQ==","unknown":true}`,
		`{"protocol":"lites.runtime.guest.v1","kind":"result","request_id":"request-00000001","sequence":1}`,
	} {
		var wire bytes.Buffer
		_ = binary.Write(&wire, binary.BigEndian, uint32(len(body)))
		wire.WriteString(body)
		if _, err = NewDecoder(&wire).Decode(); !errors.Is(err, ErrMalformedFrame) {
			t.Fatalf("ambiguous frame accepted: %v", err)
		}
	}
	var oversized bytes.Buffer
	_ = binary.Write(&oversized, binary.BigEndian, uint32(MaximumFrame+1))
	if _, err = NewDecoder(&oversized).Decode(); !errors.Is(err, ErrMalformedFrame) {
		t.Fatalf("oversized frame = %v", err)
	}
}

func TestProbeAndAttestationRequireExactChallengeAndGuestIdentity(t *testing.T) {
	challenge := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	probe := Frame{Protocol: ProtocolVersion, Kind: FrameProbe, RequestID: "request-probe-0001", Probe: &ProbeRequest{Challenge: challenge}}
	attestation := Frame{Protocol: ProtocolVersion, Kind: FrameAttest, RequestID: "request-probe-0001", Sequence: 1, Attest: &Attestation{Challenge: challenge, GuestAgentBuild: "lites-runtime-guest-agent.v1", UserID: 1000, GroupID: 1000, BootUnixMillis: time.Now().UnixMilli()}}
	for _, frame := range []Frame{probe, attestation} {
		var encoded bytes.Buffer
		if err := NewEncoder(&encoded).Encode(frame); err != nil {
			t.Fatal(err)
		}
		if _, err := NewDecoder(&encoded).Decode(); err != nil {
			t.Fatal(err)
		}
	}
	probe.Probe.Challenge = "short"
	if err := NewEncoder(&bytes.Buffer{}).Encode(probe); !errors.Is(err, ErrMalformedFrame) {
		t.Fatalf("short probe challenge = %v", err)
	}
	attestation.Attest.UserID = 0
	if err := NewEncoder(&bytes.Buffer{}).Encode(attestation); !errors.Is(err, ErrMalformedFrame) {
		t.Fatalf("root attestation = %v", err)
	}
}

func TestExecutionRequestRejectsShellAmbiguityWorkspaceEscapeAndDangerousEnvironment(t *testing.T) {
	tests := []func(*ExecuteRequest){
		func(value *ExecuteRequest) { value.Argv[0] = "tool" },
		func(value *ExecuteRequest) { value.Argv[1] = strings.Repeat("x", 4097) },
		func(value *ExecuteRequest) { value.WorkingDirectory = "/workspace/../etc" },
		func(value *ExecuteRequest) {
			value.Environment = []EnvironmentVariable{{Name: "LD_PRELOAD", Value: "/workspace/evil.so"}}
		},
		func(value *ExecuteRequest) {
			value.Environment = []EnvironmentVariable{{Name: "LANG", Value: "C"}, {Name: "HOME", Value: "/workspace"}}
		},
		func(value *ExecuteRequest) { value.MaximumOutputBytes = 17 << 20 },
	}
	for index, edit := range tests {
		request := validRequest()
		edit(&request)
		if err := request.Validate(); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("case %d: %v", index, err)
		}
	}
}

func pointer[T any](value T) *T { return &value }
