// Package client is the ToolWorker-side, mTLS transport for a node-local
// Runtime Host Agent. Callers must obtain fenced session authority before
// invoking these methods; this client never manufactures tenant identity.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/langshift/lites/internal/runtime/controller"
	"github.com/langshift/lites/internal/runtime/firecracker"
	"github.com/langshift/lites/internal/runtime/guest"
	runtimepostgres "github.com/langshift/lites/internal/runtime/postgres"
)

var (
	ErrConfiguration = errors.New("runtime client configuration is invalid")
	ErrRejected      = errors.New("runtime host rejected request")
	ErrResponse      = errors.New("runtime host response is invalid")
)

type Client struct {
	BaseURL                  string
	HTTPClient               *http.Client
	AllowInsecureDevelopment bool
	MaximumResponseBytes     int64
}

type ProvisionRequest struct {
	TenantID                string
	CapabilityToken         string
	HostID                  string
	MachineID               string
	GuestCID                uint32
	ProvisionLease          string
	ProvisionLeaseExpiresAt time.Time
	Payload                 runtimepostgres.PayloadPointer
	Actor                   json.RawMessage
	CorrelationID           string
	ScratchRate             firecracker.RateLimit
}

type ExecuteRequest struct {
	TenantID, SessionID, CapabilityToken, ProvisionLease string
	RequestID                                            string
	Command                                              guest.ExecuteRequest
}

type HTTPError struct {
	StatusCode int
}

func (failure HTTPError) Error() string {
	return fmt.Sprintf("%s: status %d", ErrRejected, failure.StatusCode)
}

func (failure HTTPError) Unwrap() error { return ErrRejected }

func (client Client) Provision(ctx context.Context, request ProvisionRequest) (controller.Provisioned, error) {
	wire := struct {
		TenantID                string                         `json:"tenant_id"`
		CapabilityToken         string                         `json:"capability_token"`
		HostID                  string                         `json:"host_id"`
		MachineID               string                         `json:"machine_id"`
		GuestCID                uint32                         `json:"guest_cid"`
		ProvisionLease          string                         `json:"provision_lease"`
		ProvisionLeaseExpiresAt time.Time                      `json:"provision_lease_expires_at"`
		Payload                 runtimepostgres.PayloadPointer `json:"payload"`
		Actor                   json.RawMessage                `json:"actor"`
		CorrelationID           string                         `json:"correlation_id"`
		ScratchRate             firecracker.RateLimit          `json:"scratch_rate"`
	}{request.TenantID, request.CapabilityToken, request.HostID, request.MachineID, request.GuestCID, request.ProvisionLease, request.ProvisionLeaseExpiresAt, request.Payload, request.Actor, request.CorrelationID, request.ScratchRate}
	var result controller.Provisioned
	if err := client.post(ctx, "/internal/v1/runtime/provisions", wire, &result, 1<<20); err != nil {
		return controller.Provisioned{}, err
	}
	if result.Provision.TenantID != request.TenantID || result.Provision.MachineID != request.MachineID || result.Provision.GuestCID != request.GuestCID || result.Provision.SessionID == "" || result.Ready.Status != "ready" {
		return controller.Provisioned{}, ErrResponse
	}
	return result, nil
}

func (client Client) Execute(ctx context.Context, request ExecuteRequest) (controller.Executed, error) {
	wire := struct {
		TenantID        string               `json:"tenant_id"`
		SessionID       string               `json:"session_id"`
		CapabilityToken string               `json:"capability_token"`
		ProvisionLease  string               `json:"provision_lease"`
		RequestID       string               `json:"request_id"`
		Command         guest.ExecuteRequest `json:"command"`
	}{request.TenantID, request.SessionID, request.CapabilityToken, request.ProvisionLease, request.RequestID, request.Command}
	var result controller.Executed
	if err := client.post(ctx, "/internal/v1/runtime/executions", wire, &result, client.maximumResponse()); err != nil {
		return controller.Executed{}, err
	}
	if result.Execution.TenantID != request.TenantID || result.Execution.SessionID != request.SessionID || result.Execution.RequestID != request.RequestID || result.Execution.Status != "completed" || result.Result.StdoutBytes != int64(len(result.Stdout)) && !result.Result.OutputTruncated || result.Result.StderrBytes != int64(len(result.Stderr)) && !result.Result.OutputTruncated {
		return controller.Executed{}, ErrResponse
	}
	return result, nil
}

func (client Client) Terminate(ctx context.Context, sessionID, reason string) (runtimepostgres.LifecycleResult, error) {
	var result runtimepostgres.LifecycleResult
	if sessionID == "" || reason == "" {
		return result, ErrConfiguration
	}
	if err := client.post(ctx, "/internal/v1/runtime/terminations", struct {
		SessionID string `json:"session_id"`
		Reason    string `json:"reason"`
	}{sessionID, reason}, &result, 1<<20); err != nil {
		return runtimepostgres.LifecycleResult{}, err
	}
	if result.SessionID != sessionID || result.Status != "terminated" {
		return runtimepostgres.LifecycleResult{}, ErrResponse
	}
	return result, nil
}

func (client Client) post(ctx context.Context, path string, input, output any, maximum int64) error {
	endpoint, err := client.endpoint(path)
	if err != nil || client.HTTPClient == nil || maximum <= 0 {
		return ErrConfiguration
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return ErrConfiguration
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return ErrConfiguration
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := client.HTTPClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	contents, readErr := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if readErr != nil {
		return readErr
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return HTTPError{StatusCode: response.StatusCode}
	}
	if int64(len(contents)) > maximum || !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "application/json") {
		return ErrResponse
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if decoder.Decode(output) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return ErrResponse
	}
	return nil
}

func (client Client) endpoint(path string) (string, error) {
	parsed, err := url.Parse(client.BaseURL)
	if err != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil || parsed.Path != "" && parsed.Path != "/" {
		return "", ErrConfiguration
	}
	if parsed.Scheme != "https" {
		host, _, splitErr := net.SplitHostPort(parsed.Host)
		if splitErr != nil {
			host = parsed.Hostname()
		}
		ip := net.ParseIP(host)
		if !client.AllowInsecureDevelopment || parsed.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return "", ErrConfiguration
		}
	}
	parsed.Path = path
	return parsed.String(), nil
}

func (client Client) maximumResponse() int64 {
	if client.MaximumResponseBytes > 0 && client.MaximumResponseBytes <= 32<<20 {
		return client.MaximumResponseBytes
	}
	return 24 << 20
}
