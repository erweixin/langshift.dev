package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/security/transport"
)

type WorkloadAuthorizer func(*http.Request) (identity string, allowed bool)

type InternalUsageHandler struct {
	Service   InternalUsageService
	Authorize WorkloadAuthorizer
}

func (handler InternalUsageHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if handler.Service == nil || handler.Authorize == nil {
		handler.problem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", true)
		return
	}
	identity, allowed := handler.Authorize(request)
	if !allowed || identity == "" || request.Header.Get("Cookie") != "" || request.Header.Get(transport.CSRFHeader) != "" {
		handler.problem(writer, request, http.StatusUnauthorized, "authentication_required", false)
		return
	}
	if request.Method != http.MethodPost || len(request.URL.Query()) != 0 {
		handler.problem(writer, request, http.StatusNotFound, "resource_not_found", false)
		return
	}
	keyValues := request.Header.Values(transport.IdempotencyHeader)
	if len(keyValues) != 1 || idempotency.ValidateRawKey(keyValues[0]) != nil {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	path := strings.TrimPrefix(request.URL.Path, "/v1/internal/usage/reservations")
	switch {
	case path == "":
		handler.reserve(writer, request, identity, keyValues[0])
	case strings.HasSuffix(path, "/settle"):
		handler.settle(writer, request, identity, keyValues[0], strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/settle"))
	case strings.HasSuffix(path, "/release"):
		handler.release(writer, request, identity, keyValues[0], strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/release"))
	default:
		handler.problem(writer, request, http.StatusNotFound, "resource_not_found", false)
	}
}

func (handler InternalUsageHandler) reserve(writer http.ResponseWriter, request *http.Request, identity, key string) {
	var body struct {
		RequestID      string `json:"request_id"`
		TenantID       string `json:"tenant_id"`
		UserID         string `json:"user_id"`
		OperationKey   string `json:"operation_key"`
		RequestedUnits uint64 `json:"requested_units"`
		ResourceKind   string `json:"resource_kind"`
		SubjectID      string `json:"subject_id"`
		SubjectVersion uint64 `json:"subject_version"`
		BYOK           bool   `json:"byok"`
	}
	if !decodeInternalUsage(writer, request, &body) || !validRequestID(body.RequestID) || uuid.Validate(body.TenantID) != nil || uuid.Validate(body.UserID) != nil || len(body.OperationKey) < 16 || len(body.OperationKey) > 200 || body.RequestedUnits < 1 || body.RequestedUnits > 1_000_000_000 || body.ResourceKind != "provider_attempt" && body.ResourceKind != "tool_call" || uuid.Validate(body.SubjectID) != nil || body.SubjectVersion < 1 {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	result, err := handler.Service.Reserve(request.Context(), InternalUsageReserveCommand{RequestID: body.RequestID, IdempotencyKey: key, WorkloadIdentity: identity, TenantID: body.TenantID, UserID: body.UserID, OperationKey: body.OperationKey, SubjectKind: body.ResourceKind, SubjectID: body.SubjectID, SubjectVersion: body.SubjectVersion, RequestedUnits: body.RequestedUnits, BYOK: body.BYOK})
	handler.finish(writer, request, result, err)
}

func (handler InternalUsageHandler) settle(writer http.ResponseWriter, request *http.Request, identity, key, reservationID string) {
	if uuid.Validate(reservationID) != nil {
		handler.problem(writer, request, http.StatusNotFound, "resource_not_found", false)
		return
	}
	var body struct {
		RequestID                  string  `json:"request_id"`
		TenantID                   string  `json:"tenant_id"`
		ActualUnits                uint64  `json:"actual_units"`
		ProviderCostMicrounits     uint64  `json:"provider_cost_microunits"`
		ProviderAttemptID          *string `json:"provider_attempt_id"`
		ExpectedReservationVersion uint64  `json:"expected_reservation_version"`
	}
	if !decodeInternalUsage(writer, request, &body) || !validRequestID(body.RequestID) || uuid.Validate(body.TenantID) != nil || body.ProviderAttemptID == nil || uuid.Validate(*body.ProviderAttemptID) != nil || body.ExpectedReservationVersion != 1 {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	if !requireInternalIfMatch(writer, request, 1, handler.problem) {
		return
	}
	result, err := handler.Service.Settle(request.Context(), InternalUsageSettleCommand{RequestID: body.RequestID, IdempotencyKey: key, WorkloadIdentity: identity, TenantID: body.TenantID, ReservationID: reservationID, ProviderAttemptID: *body.ProviderAttemptID, ActualUnits: body.ActualUnits, ProviderCostMicrounits: body.ProviderCostMicrounits, ExpectedVersion: body.ExpectedReservationVersion})
	handler.finish(writer, request, result, err)
}

func (handler InternalUsageHandler) release(writer http.ResponseWriter, request *http.Request, identity, key, reservationID string) {
	if uuid.Validate(reservationID) != nil {
		handler.problem(writer, request, http.StatusNotFound, "resource_not_found", false)
		return
	}
	var body struct {
		RequestID                  string `json:"request_id"`
		TenantID                   string `json:"tenant_id"`
		Reason                     string `json:"reason"`
		ExpectedReservationVersion uint64 `json:"expected_reservation_version"`
	}
	if !decodeInternalUsage(writer, request, &body) || !validRequestID(body.RequestID) || uuid.Validate(body.TenantID) != nil || strings.TrimSpace(body.Reason) == "" || len(strings.TrimSpace(body.Reason)) > 200 || body.ExpectedReservationVersion != 1 {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	if !requireInternalIfMatch(writer, request, 1, handler.problem) {
		return
	}
	result, err := handler.Service.Release(request.Context(), InternalUsageReleaseCommand{RequestID: body.RequestID, IdempotencyKey: key, WorkloadIdentity: identity, TenantID: body.TenantID, ReservationID: reservationID, Reason: strings.TrimSpace(body.Reason), ExpectedVersion: body.ExpectedReservationVersion})
	handler.finish(writer, request, result, err)
}

func (handler InternalUsageHandler) finish(writer http.ResponseWriter, request *http.Request, result InternalUsageResult, err error) {
	if err != nil {
		handler.finishError(writer, request, err)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(writer).Encode(result)
}

func (handler InternalUsageHandler) finishError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, ErrValidation):
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
	case errors.Is(err, ErrPermissionDenied):
		handler.problem(writer, request, http.StatusForbidden, "permission_denied", false)
	case errors.Is(err, ErrResourceNotFound):
		handler.problem(writer, request, http.StatusNotFound, "resource_not_found", false)
	case errors.Is(err, ErrStateConflict), errors.Is(err, ErrIdempotencyConflict):
		handler.problem(writer, request, http.StatusConflict, "state_conflict", true)
	default:
		handler.problem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", true)
	}
}

func (handler InternalUsageHandler) problem(writer http.ResponseWriter, request *http.Request, status int, code string, retry bool) {
	(Handler{}).writeProblem(writer, request, status, code, retry)
}

func decodeInternalUsage(writer http.ResponseWriter, request *http.Request, target any) bool {
	if request.Header.Get("Content-Type") != "application/json" {
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 32<<10))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil && errors.Is(decoder.Decode(&struct{}{}), io.EOF)
}

type internalProblemWriter func(http.ResponseWriter, *http.Request, int, string, bool)

func requireInternalIfMatch(writer http.ResponseWriter, request *http.Request, expected uint64, problem internalProblemWriter) bool {
	value := request.Header.Get("If-Match")
	if value == "" {
		problem(writer, request, http.StatusPreconditionRequired, "precondition_required", false)
		return false
	}
	if value != `"`+strconv.FormatUint(expected, 10)+`"` {
		problem(writer, request, http.StatusPreconditionFailed, "precondition_failed", false)
		return false
	}
	return true
}
