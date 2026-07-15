// Package api exposes the node-local, mTLS-only RuntimeManager control API.
// It accepts already-fenced capability material from ToolWorker and never
// derives tenant or host authority from client-controlled headers.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/langshift/lites/internal/runtime/controller"
	"github.com/langshift/lites/internal/runtime/firecracker"
	"github.com/langshift/lites/internal/runtime/guest"
	runtimepostgres "github.com/langshift/lites/internal/runtime/postgres"
)

type Controller interface {
	Provision(context.Context, controller.ProvisionRequest) (controller.Provisioned, error)
	Execute(context.Context, controller.ExecuteRequest) (controller.Executed, error)
	Terminate(context.Context, string, string) (runtimepostgres.LifecycleResult, error)
}

type Handler struct {
	Controller                       Controller
	HostID                           string
	RequireVerifiedClientCertificate bool
	AllowedClientSPIFFEID            string
	RequestTimeout                   time.Duration
	MaximumExecutionDuration         time.Duration
}

type provisionRequest struct {
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
}

type terminateRequest struct {
	SessionID string `json:"session_id"`
	Reason    string `json:"reason"`
}

type executeRequest struct {
	TenantID        string               `json:"tenant_id"`
	SessionID       string               `json:"session_id"`
	CapabilityToken string               `json:"capability_token"`
	ProvisionLease  string               `json:"provision_lease"`
	RequestID       string               `json:"request_id"`
	Command         guest.ExecuteRequest `json:"command"`
}

func (handler Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if handler.Controller == nil || handler.HostID == "" || handler.RequestTimeout <= 0 || handler.RequestTimeout > time.Minute || handler.RequireVerifiedClientCertificate && handler.AllowedClientSPIFFEID == "" {
		http.Error(writer, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	if handler.RequireVerifiedClientCertificate && !verifiedSPIFFEClient(request, handler.AllowedClientSPIFFEID) {
		http.Error(writer, "client certificate required", http.StatusUnauthorized)
		return
	}
	switch {
	case request.Method == http.MethodPost && request.URL.Path == "/internal/v1/runtime/provisions":
		ctx, cancel := context.WithTimeout(request.Context(), handler.RequestTimeout)
		defer cancel()
		handler.provision(ctx, writer, request)
	case request.Method == http.MethodPost && request.URL.Path == "/internal/v1/runtime/executions":
		if handler.MaximumExecutionDuration <= 0 || handler.MaximumExecutionDuration > time.Hour {
			http.Error(writer, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		handler.execute(request.Context(), writer, request)
	case request.Method == http.MethodPost && request.URL.Path == "/internal/v1/runtime/terminations":
		ctx, cancel := context.WithTimeout(request.Context(), handler.RequestTimeout)
		defer cancel()
		handler.terminate(ctx, writer, request)
	default:
		http.NotFound(writer, request)
	}
}

func verifiedSPIFFEClient(request *http.Request, expected string) bool {
	if request.TLS == nil || len(request.TLS.VerifiedChains) == 0 || len(request.TLS.VerifiedChains[0]) == 0 {
		return false
	}
	for _, identity := range request.TLS.VerifiedChains[0][0].URIs {
		if identity != nil && identity.String() == expected {
			return true
		}
	}
	return false
}

func (handler Handler) provision(ctx context.Context, writer http.ResponseWriter, request *http.Request) {
	var input provisionRequest
	if !decodeStrict(request, &input, 128<<10) || input.HostID != handler.HostID {
		http.Error(writer, "invalid provision request", http.StatusBadRequest)
		return
	}
	result, err := handler.Controller.Provision(ctx, controller.ProvisionRequest{Command: runtimepostgres.ProvisionCommand{
		TenantID: input.TenantID, CapabilityToken: input.CapabilityToken, HostID: input.HostID, MachineID: input.MachineID, GuestCID: input.GuestCID,
		ProvisionLease: input.ProvisionLease, ProvisionLeaseExpiresAt: input.ProvisionLeaseExpiresAt, Payload: input.Payload, Actor: input.Actor, CorrelationID: input.CorrelationID,
	}, ScratchRate: input.ScratchRate})
	if err != nil {
		writeControllerError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, result)
}

func (handler Handler) execute(ctx context.Context, writer http.ResponseWriter, request *http.Request) {
	var input executeRequest
	if !decodeStrict(request, &input, 24<<20) || input.TenantID == "" || input.SessionID == "" || input.CapabilityToken == "" || input.ProvisionLease == "" || input.RequestID == "" || input.Command.Validate() != nil {
		http.Error(writer, "invalid execution request", http.StatusBadRequest)
		return
	}
	deadline := time.UnixMilli(input.Command.DeadlineUnixMillis)
	if !deadline.After(time.Now()) || deadline.After(time.Now().Add(handler.MaximumExecutionDuration)) {
		http.Error(writer, "invalid execution deadline", http.StatusBadRequest)
		return
	}
	result, err := handler.Controller.Execute(ctx, controller.ExecuteRequest{
		TenantID: input.TenantID, SessionID: input.SessionID, CapabilityToken: input.CapabilityToken,
		ProvisionLease: input.ProvisionLease, RequestID: input.RequestID, Command: input.Command,
	})
	if err != nil {
		writeControllerError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler Handler) terminate(ctx context.Context, writer http.ResponseWriter, request *http.Request) {
	var input terminateRequest
	if !decodeStrict(request, &input, 128<<10) || input.SessionID == "" || input.Reason == "" {
		http.Error(writer, "invalid termination request", http.StatusBadRequest)
		return
	}
	result, err := handler.Controller.Terminate(ctx, input.SessionID, input.Reason)
	if err != nil {
		writeControllerError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func decodeStrict(request *http.Request, target any, maximum int64) bool {
	if request.Body == nil || !strings.HasPrefix(strings.ToLower(request.Header.Get("Content-Type")), "application/json") {
		return false
	}
	contents, err := io.ReadAll(io.LimitReader(request.Body, maximum+1))
	if err != nil || len(contents) == 0 || int64(len(contents)) > maximum {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return false
	}
	return true
}

func writeControllerError(writer http.ResponseWriter, err error) {
	status := http.StatusConflict
	if errors.Is(err, controller.ErrConfiguration) {
		status = http.StatusServiceUnavailable
	} else if errors.Is(err, runtimepostgres.ErrInvalidCommand) {
		status = http.StatusBadRequest
	} else if errors.Is(err, runtimepostgres.ErrCapabilityBinding) || errors.Is(err, runtimepostgres.ErrApproval) {
		status = http.StatusForbidden
	}
	http.Error(writer, http.StatusText(status), status)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		http.Error(writer, "internal error", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write(encoded)
}
