package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/langshift/lites/internal/runtime/controller"
	"github.com/langshift/lites/internal/runtime/guest"
	runtimepostgres "github.com/langshift/lites/internal/runtime/postgres"
)

func TestClientExecuteUsesStrictBoundLoopbackTransport(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/internal/v1/runtime/executions" || request.Header.Get("Content-Type") != "application/json" {
			return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader("invalid")), Header: http.Header{}}, nil
		}
		var input map[string]any
		if json.NewDecoder(request.Body).Decode(&input) != nil || input["capability_token"] != "signed-capability" {
			return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader("invalid")), Header: http.Header{}}, nil
		}
		encoded, err := json.Marshal(controller.Executed{
			Execution: runtimepostgres.RuntimeExecution{TenantID: "tenant-1", SessionID: "session-1", RequestID: "runtime-request-0001", Status: "completed"},
			Result:    guest.ExecutionResult{ExitCode: 0, StdoutBytes: 2, StartedUnixMillis: 1, FinishedUnixMillis: 2}, Stdout: []byte(`{}`),
		})
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(encoded))), Header: http.Header{"Content-Type": []string{"application/json"}}}, err
	})
	client := Client{BaseURL: "http://127.0.0.1:8443", HTTPClient: &http.Client{Transport: transport}, AllowInsecureDevelopment: true}
	result, err := client.Execute(context.Background(), ExecuteRequest{
		TenantID: "tenant-1", SessionID: "session-1", CapabilityToken: "signed-capability", ProvisionLease: "lease", RequestID: "runtime-request-0001",
		Command: guest.ExecuteRequest{Argv: []string{"/usr/bin/tool"}, WorkingDirectory: "/workspace", DeadlineUnixMillis: time.Now().Add(time.Minute).UnixMilli(), MaximumOutputBytes: 1 << 20},
	})
	if err != nil || result.Execution.Status != "completed" || string(result.Stdout) != `{}` {
		t.Fatalf("Execute()=%#v err=%v", result, err)
	}
	client.BaseURL = "http://example.com"
	if _, err = client.Execute(context.Background(), ExecuteRequest{}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("non-loopback plaintext endpoint error=%v", err)
	}
}

func TestClientRejectsStatusAndResponseSchemaDrift(t *testing.T) {
	client := Client{BaseURL: "http://127.0.0.1:8443", HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusConflict, Body: io.NopCloser(strings.NewReader("conflict")), Header: http.Header{}}, nil
	})}, AllowInsecureDevelopment: true}
	_, err := client.Terminate(context.Background(), "session-1", "test")
	var failure HTTPError
	if !errors.Is(err, ErrRejected) || !errors.As(err, &failure) || failure.StatusCode != http.StatusConflict {
		t.Fatalf("Terminate() error=%v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
